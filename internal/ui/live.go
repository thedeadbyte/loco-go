package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/thedeadbyte/loco-go/internal/toolcall"
)

// ---------------------------------------------------------------- markdown

var (
	rendMu    sync.Mutex
	rend      *glamour.TermRenderer
	rendWidth int
)

// resetRenderer drops the cached renderer, e.g. after a theme change.
func resetRenderer() {
	rendMu.Lock()
	defer rendMu.Unlock()
	rend = nil
}

// styleConfig is glamour's default style with the document margin removed, so
// markdown starts at column 0 the way the Python version's rich output did.
func styleConfig() gansi.StyleConfig {
	sc := styles.DarkStyleConfig
	if !lipgloss.HasDarkBackground() {
		sc = styles.LightStyleConfig
	}
	zero := uint(0)
	sc.Document.Margin = &zero
	sc.Document.BlockPrefix = ""
	sc.Document.BlockSuffix = ""
	return sc
}

// RenderMarkdown renders md to ANSI, wrapped to width. On any renderer failure
// it falls back to the raw text — a markdown edge case must never swallow the
// model's answer.
func RenderMarkdown(md string, width int) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	// Expand tabs before rendering, not after: glamour pads every line out to
	// the wrap width counting a tab as one column, so a tab left in the source
	// makes the rendered line wider than it was measured to be.
	md = expandTabs(md)
	rendMu.Lock()
	if rend == nil || rendWidth != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(styleConfig()),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			rendMu.Unlock()
			return md
		}
		rend, rendWidth = r, width
	}
	r := rend
	out, err := r.Render(md)
	rendMu.Unlock()
	if err != nil {
		return md
	}
	return expandTabs(strings.Trim(out, "\n"))
}

// tabStop is the column interval terminals advance a tab to.
const tabStop = 8

// expandTabs replaces tabs with spaces, tracking the visible column so ANSI
// sequences cost nothing. Without this a rendered line's measured width and its
// on-screen width disagree, the live region's erase comes up short, and every
// repaint leaves a stale row behind.
func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	col := 0
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\n':
			b.WriteByte('\n')
			col = 0
			i++
		case s[i] == '\t':
			n := tabStop - col%tabStop
			b.WriteString(strings.Repeat(" ", n))
			col += n
			i++
		case s[i] == 0x1b:
			end := escapeEnd(s, i)
			b.WriteString(s[i:end])
			i = end
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+size])
			col += ansi.StringWidth(string(r))
			i += size
		}
	}
	return b.String()
}

// escapeEnd returns the offset just past the escape sequence starting at s[i].
func escapeEnd(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return len(s)
	}
	switch s[j] {
	case '[': // CSI: parameters then a final byte in @-~
		for j++; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	case ']': // OSC: runs to BEL or ST
		for j++; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	}
	return j + 1
}

// displayRows counts the terminal rows an already-rendered string occupies.
func displayRows(s string, width int) int {
	if s == "" {
		return 0
	}
	rows := 0
	for _, line := range strings.Split(s, "\n") {
		w := ansi.StringWidth(line)
		if w == 0 || width <= 0 {
			rows++
			continue
		}
		rows += (w + width - 1) / width
	}
	return rows
}

// lastBlockBoundary returns the offset just past the last blank-line block
// separator that is not inside a fenced code block, or 0 if there is none.
// Splitting there keeps each committed chunk a self-contained markdown block.
func lastBlockBoundary(md string) int {
	inFence := false
	best := 0
	offset := 0
	prevBlank := false
	for _, line := range strings.SplitAfter(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if prevBlank && !inFence && offset > 0 && trimmed != "" {
			best = offset
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		prevBlank = trimmed == "" && !inFence
		offset += len(line)
	}
	return best
}

// ---------------------------------------------------------------- live region

// spinnerFrames matches the "dots" spinner the Python version used.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const refreshInterval = 80 * time.Millisecond

// Stream accumulates streamed tokens and live-renders them as markdown,
// repainting the region in place as more text arrives.
//
// Call PauseForPrompt before anything else reads from stdin or writes to the
// terminal, or the repainted region will chew through it.
type Stream struct {
	mu        sync.Mutex
	buf       strings.Builder
	status    string
	frame     int
	live      bool
	lastRows  int // terminal rows the live region currently occupies
	committed int // bytes of display text already printed permanently
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// NewStream returns an idle renderer.
func NewStream() *Stream { return &Stream{} }

// Start opens the live region with a spinner showing status.
func (s *Stream) Start(status string) {
	if status == "" {
		status = "thinking…"
	}
	s.mu.Lock()
	if s.live {
		s.status = status
		s.mu.Unlock()
		return
	}
	s.buf.Reset()
	s.committed = 0
	s.lastRows = 0
	s.frame = 0
	s.status = status
	s.live = true
	if !IsTTY() {
		s.mu.Unlock()
		return // no cursor tricks when output isn't a terminal
	}
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	stop, done := s.stopCh, s.doneCh
	s.mu.Unlock()

	go func() {
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.mu.Lock()
				s.frame++
				s.redraw(false)
				s.mu.Unlock()
			}
		}
	}()
}

// Token appends streamed assistant text. It starts the region if the caller
// hasn't — matching the Python behavior where a token always has somewhere to go.
func (s *Stream) Token(text string) {
	s.mu.Lock()
	if !s.live {
		s.mu.Unlock()
		s.Start("")
		s.mu.Lock()
	}
	s.buf.WriteString(text)
	s.mu.Unlock()
}

// Stop closes the live region, leaving the final render on screen.
func (s *Stream) Stop() {
	s.mu.Lock()
	if !s.live {
		s.mu.Unlock()
		return
	}
	stop, done := s.stopCh, s.doneCh
	s.live = false
	s.stopCh, s.doneCh = nil, nil
	s.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done // wait for the painter so it can't repaint after the final frame
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !IsTTY() {
		if disp := toolcall.Strip(s.buf.String()); disp != "" {
			fmt.Fprintln(Out, disp)
		}
		s.reset()
		return
	}
	s.redraw(true)
	if toolcall.Strip(s.buf.String()) != "" {
		fmt.Fprintln(Out)
	}
	s.reset()
}

// PauseForPrompt closes the live region so an input prompt renders cleanly.
// What has been rendered stays on screen; the next Token starts a fresh region
// below it.
func (s *Stream) PauseForPrompt() {
	s.mu.Lock()
	if !s.live {
		s.mu.Unlock()
		return
	}
	stop, done := s.stopCh, s.doneCh
	s.live = false
	s.stopCh, s.doneCh = nil, nil
	s.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if IsTTY() {
		s.redraw(true)
		if toolcall.Strip(s.buf.String()) != "" {
			fmt.Fprintln(Out)
		}
	} else if disp := toolcall.Strip(s.buf.String()); disp != "" {
		fmt.Fprintln(Out, disp)
	}
	s.reset()
}

func (s *Stream) reset() {
	s.buf.Reset()
	s.committed = 0
	s.lastRows = 0
}

// redraw repaints the live region. Caller holds the lock.
//
// The region is erased by moving up over the rows it occupies and clearing to
// the end of the screen. When the render outgrows the terminal it can no longer
// be erased, so completed markdown blocks are printed permanently and drop out
// of the live region — the tail keeps updating, the scrollback stays correct.
func (s *Stream) redraw(final bool) {
	width, height := Width(), Height()
	// glamour pads every line out to the wrap width, so wrapping one column
	// short keeps a rendered line from ever filling the last cell — a line that
	// exactly fills the terminal costs an extra row the erase can't account for
	wrap := width - 1
	if wrap < 20 {
		wrap = 20
	}
	disp := toolcall.Strip(s.buf.String())
	if s.committed > len(disp) {
		s.committed = len(disp)
	}
	livePart := disp[s.committed:]

	var commitOut, liveOut string
	if strings.TrimSpace(livePart) == "" {
		if !final && s.committed == 0 {
			// nothing but a hidden tool blob has streamed: keep the spinner up
			liveOut = dim.Render(spinnerFrames[s.frame%len(spinnerFrames)] + " " + s.status)
		}
	} else {
		if !final {
			// Commit every finished markdown block the moment it is finished.
			// The live region then only ever holds the block still streaming,
			// which keeps it small enough to repaint reliably: a region that
			// grows to the height of the screen can no longer be erased, since
			// the rows that scrolled off the top are out of the cursor's reach.
			if cut := lastBlockBoundary(livePart); cut > 0 {
				commitOut = RenderMarkdown(livePart[:cut], wrap)
				s.committed += cut
				livePart = livePart[cut:]
			}
		}
		liveOut = RenderMarkdown(livePart, wrap)
		avail := height - 4
		if avail < 8 {
			avail = 8
		}
		if !final && displayRows(liveOut, width) > avail {
			// one block taller than the budget: print it and let it scroll
			// rather than repaint garbage over the top of it
			if commitOut != "" {
				commitOut += "\n"
			}
			commitOut += liveOut
			s.committed = len(disp)
			liveOut = ""
		}
	}

	var b strings.Builder
	if s.lastRows > 0 {
		fmt.Fprintf(&b, "\x1b[%dA\r\x1b[J", s.lastRows)
	} else {
		b.WriteString("\r\x1b[K")
	}
	if commitOut != "" {
		b.WriteString(commitOut)
		b.WriteString("\n")
	}
	if liveOut != "" {
		b.WriteString(liveOut)
		b.WriteString("\n")
		s.lastRows = displayRows(liveOut, width)
	} else {
		s.lastRows = 0
	}
	fmt.Fprint(Out, b.String())
}
