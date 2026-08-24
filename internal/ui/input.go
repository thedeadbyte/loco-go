package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// ErrInterrupted is returned by Prompt when the user presses Ctrl-C. The REPL
// treats it as "abandon this line", not "quit" — Ctrl-D (io.EOF) quits.
var ErrInterrupted = errors.New("interrupted")

// Input reads user lines with history and a status toolbar. It falls back to
// plain line reading when stdin isn't a terminal, so `echo hi | loco` works.
type Input struct {
	history []string
	path    string
	mu      sync.Mutex
	tty     bool
	reader  *bufio.Reader
}

// NewInput loads history from path (missing or unreadable history is not an
// error — it just starts empty).
func NewInput(path string) *Input {
	in := &Input{
		path:   path,
		tty:    term.IsTerminal(int(os.Stdin.Fd())) && IsTTY(),
		reader: bufio.NewReader(os.Stdin),
	}
	if raw, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(raw), "\n") {
			if l = strings.TrimRight(l, "\r"); l != "" {
				in.history = append(in.history, l)
			}
		}
	}
	return in
}

// Prompt reads one line. toolbar, when non-nil, renders the status bar shown
// under the input while the user types.
func (in *Input) Prompt(prompt string, toolbar func() string) (string, error) {
	if !in.tty {
		line, err := in.reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if err != nil && line == "" {
			return "", io.EOF
		}
		return line, nil
	}

	ti := textinput.New()
	ti.Prompt = prompt
	ti.PromptStyle = lipgloss.NewStyle().Bold(true).Foreground(Theme.Prompt)
	ti.Focus()
	ti.Width = maxInt(10, Width()-ansi.StringWidth(prompt)-1)

	m := promptModel{ti: ti, toolbar: toolbar, history: in.snapshot()}
	m.idx = len(m.history)

	final, err := tea.NewProgram(m, tea.WithOutput(os.Stdout)).Run()
	if err != nil {
		return "", err
	}
	res, _ := final.(promptModel)
	switch {
	case res.interrupted:
		return "", ErrInterrupted
	case res.eof:
		return "", io.EOF
	}
	line := res.ti.Value()
	// bubbletea wipes its own frames on exit, so echo the entered line here —
	// otherwise the transcript loses the question next to its answer
	fmt.Fprintln(Out, ti.PromptStyle.Render(prompt)+line)
	in.Append(line)
	return line, nil
}

func (in *Input) snapshot() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]string, len(in.history))
	copy(out, in.history)
	return out
}

// Append records a line in history, in memory and on disk. Blank lines and
// immediate repeats are skipped — they only make the up-arrow less useful.
func (in *Input) Append(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if n := len(in.history); n > 0 && in.history[n-1] == line {
		return
	}
	in.history = append(in.history, line)
	if in.path == "" {
		return
	}
	if err := os.MkdirAll(dirOf(in.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(in.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return // history is a convenience; failing to persist it is not fatal
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

// ---------------------------------------------------------------- bubbletea

type promptModel struct {
	ti          textinput.Model
	toolbar     func() string
	history     []string
	idx         int    // cursor into history; len(history) means "the live line"
	draft       string // the typed line, stashed while browsing history
	interrupted bool
	eof         bool
	done        bool
}

func (m promptModel) Init() tea.Cmd { return textinput.Blink }

func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ti.Width = maxInt(10, msg.Width-ansi.StringWidth(m.ti.Prompt)-1)
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC:
			m.interrupted, m.done = true, true
			return m, tea.Quit
		case tea.KeyCtrlD:
			if m.ti.Value() == "" {
				m.eof, m.done = true, true
				return m, tea.Quit
			}
		case tea.KeyUp:
			if m.idx > 0 {
				if m.idx == len(m.history) {
					m.draft = m.ti.Value()
				}
				m.idx--
				m.ti.SetValue(m.history[m.idx])
				m.ti.CursorEnd()
			}
			return m, nil
		case tea.KeyDown:
			if m.idx < len(m.history) {
				m.idx++
				if m.idx == len(m.history) {
					m.ti.SetValue(m.draft)
				} else {
					m.ti.SetValue(m.history[m.idx])
				}
				m.ti.CursorEnd()
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	return m, cmd
}

var toolbarStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("236")).Foreground(lipgloss.Color("250"))

func (m promptModel) View() string {
	if m.done {
		return "" // the caller echoes the entered line; see Prompt
	}
	view := m.ti.View()
	if m.toolbar == nil {
		return view
	}
	bar := m.toolbar()
	if pad := Width() - ansi.StringWidth(bar); pad > 0 {
		bar += strings.Repeat(" ", pad)
	}
	return view + "\n" + toolbarStyle.Render(bar)
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i > 0 {
		return path[:i]
	}
	return "."
}
