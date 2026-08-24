package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// fakeTerm points the renderer at a buffer and a fixed terminal size.
func fakeTerm(t *testing.T, w, h int) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	oldOut, oldSize, oldTTY := Out, termSize, ttyCheck
	Out = buf
	termSize = func() (int, int) { return w, h }
	ttyCheck = func() bool { return true }
	t.Cleanup(func() { Out, termSize, ttyCheck = oldOut, oldSize, oldTTY })
	return buf
}

func TestExpandTabs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\tb", "a       b"},
		{"12345678\tx", "12345678        x"},
		{"\ta", "        a"},
		{"a\nb\tc", "a\nb       c"},                        // the column resets on a newline
		{"\x1b[31m\tx\x1b[0m", "\x1b[31m        x\x1b[0m"}, // escapes cost no columns
		{"no tabs here", "no tabs here"},
	}
	for _, tc := range cases {
		if got := expandTabs(tc.in); got != tc.want {
			t.Errorf("expandTabs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The live region's erase depends on this count being exactly right; a tab or a
// wide rune that measures differently than it renders leaves stale rows behind.
func TestDisplayRows(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  int
	}{
		{"empty", "", 80, 0},
		{"one short line", "hello", 80, 1},
		{"two lines", "a\nb", 80, 2},
		{"exactly the width", strings.Repeat("x", 80), 80, 1},
		{"one over the width", strings.Repeat("x", 81), 80, 2},
		{"blank line still takes a row", "a\n\nb", 80, 3},
		{"colors do not count", "\x1b[31mred\x1b[0m", 80, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayRows(tc.s, tc.width); got != tc.want {
				t.Errorf("displayRows = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRenderedMarkdownFitsTheWrapWidth(t *testing.T) {
	const wrap = 60
	md := "## Heading\n\nSome prose that is long enough to wrap around at least once.\n\n" +
		"```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n"
	out := RenderMarkdown(md, wrap)
	if strings.Contains(out, "\t") {
		t.Error("rendered output still contains a tab")
	}
	for _, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > wrap {
			t.Errorf("line is %d cols, wrap is %d: %q", w, wrap, ansi.Strip(line))
		}
	}
}

func TestLastBlockBoundary(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string // the text that would stay live, or "" for no split
	}{
		{"single block", "one paragraph still going", ""},
		{"two blocks", "first para\n\nsecond para", "second para"},
		{"three blocks splits at the last", "a\n\nb\n\nc", "c"},
		{"blank line inside a fence is not a boundary",
			"intro\n\n```go\nfunc a() {}\n\nfunc b() {}\n```", "```go\nfunc a() {}\n\nfunc b() {}\n```"},
		{"trailing blank line is not a boundary", "para\n\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cut := lastBlockBoundary(tc.md)
			if tc.want == "" {
				if cut != 0 {
					t.Fatalf("expected no split, cut at %d leaving %q", cut, tc.md[cut:])
				}
				return
			}
			if got := tc.md[cut:]; got != tc.want {
				t.Errorf("live part = %q, want %q", got, tc.want)
			}
		})
	}
}

// The live region must erase exactly what it drew: every repaint moves up by
// the row count of the previous frame and clears to the end of the screen.
func TestStreamRepaintsInPlace(t *testing.T) {
	buf := fakeTerm(t, 60, 20)
	s := NewStream()
	s.Start("thinking…")
	for _, tok := range []string{"first line", " continues", " and ends."} {
		s.Token(tok)
		s.mu.Lock()
		s.redraw(false)
		s.mu.Unlock()
	}
	s.Stop()

	out := ansi.Strip(buf.String())
	if !strings.Contains(out, "first line continues and ends.") {
		t.Errorf("final text missing from output:\n%q", ansi.Strip(out))
	}
	// the text should survive exactly once in the final frame
	frames := strings.Split(buf.String(), "\x1b[J")
	last := ansi.Strip(frames[len(frames)-1])
	if n := strings.Count(last, "first line"); n != 1 {
		t.Errorf("final frame repeats the text %d times: %q", n, last)
	}
}

// A finished markdown block leaves the live region and is never redrawn, so a
// long answer cannot outgrow the erasable area.
func TestStreamCommitsFinishedBlocks(t *testing.T) {
	fakeTerm(t, 60, 20)
	s := NewStream()
	s.Start("")
	s.Token("first paragraph.\n\nsecond paragraph still streaming")
	s.mu.Lock()
	s.redraw(false)
	committed := s.committed
	rows := s.lastRows
	s.mu.Unlock()
	s.Stop()

	if committed == 0 {
		t.Error("the finished first paragraph was not committed")
	}
	if rows > 4 {
		t.Errorf("live region is %d rows; it should only hold the streaming block", rows)
	}
}

func TestStreamHidesToolJSON(t *testing.T) {
	buf := fakeTerm(t, 60, 20)
	s := NewStream()
	s.Start("")
	s.Token(`Let me look. {"name": "list_dir", "arguments": {"path": "."}}`)
	s.Stop()
	if strings.Contains(ansi.Strip(buf.String()), "list_dir") {
		t.Errorf("tool JSON leaked into the transcript:\n%s", ansi.Strip(buf.String()))
	}
	if !strings.Contains(ansi.Strip(buf.String()), "Let me look.") {
		t.Error("prose around the tool call was dropped")
	}
}

func TestNonTTYPrintsPlainText(t *testing.T) {
	buf := &bytes.Buffer{}
	oldOut, oldTTY := Out, ttyCheck
	Out, ttyCheck = buf, func() bool { return false }
	t.Cleanup(func() { Out, ttyCheck = oldOut, oldTTY })

	s := NewStream()
	s.Start("")
	s.Token("# Heading\n\nbody")
	s.Stop()
	got := buf.String()
	if strings.Contains(got, "\x1b[") {
		t.Errorf("piped output contains escape sequences: %q", got)
	}
	if !strings.Contains(got, "# Heading") {
		t.Errorf("piped output = %q", got)
	}
}

func TestContextBar(t *testing.T) {
	fakeTerm(t, 80, 24)
	got := ansi.Strip(ContextBar(4096, 8192, 16))
	if !strings.Contains(got, "(50%)") || !strings.Contains(got, "4.1k/8.2k") {
		t.Errorf("ContextBar = %q", got)
	}
	if strings.Count(got, "█") != 8 || strings.Count(got, "░") != 8 {
		t.Errorf("bar not half full: %q", got)
	}
	if got := ansi.Strip(ContextBar(9000, 8192, 16)); !strings.Contains(got, "(100%)") {
		t.Errorf("overflow should clamp to 100%%: %q", got)
	}
	if got := ansi.Strip(ContextBar(0, 0, 16)); !strings.Contains(got, "(0%)") {
		t.Errorf("zero total = %q", got)
	}
}

func TestHumanizeTokens(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1.0k", 8192: "8.2k"} {
		if got := HumanizeTokens(in); got != want {
			t.Errorf("HumanizeTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShowToolResultTruncates(t *testing.T) {
	buf := fakeTerm(t, 200, 24)
	ShowToolResult("line one\nline two\nline three")
	got := ansi.Strip(buf.String())
	if !strings.Contains(got, "line one") || !strings.Contains(got, "[+2 lines]") {
		t.Errorf("result line = %q", got)
	}
}
