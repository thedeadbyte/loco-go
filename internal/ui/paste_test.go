package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func newPrompt() promptModel {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.Focus()
	ti.Width = 60
	return promptModel{ti: ti}
}

// send feeds one key, pretending it arrived `gap` after the previous one. Both
// timestamps move, so the whole clock shifts rather than just the last key.
func send(m promptModel, msg tea.KeyMsg, gap time.Duration) (promptModel, tea.Cmd) {
	m.lastKeyAt = time.Now().Add(-gap)
	m.pasteUntil = m.pasteUntil.Add(-gap)
	next, cmd := m.Update(msg)
	return next.(promptModel), cmd
}

func typeRune(m promptModel, r rune, gap time.Duration) promptModel {
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}, gap)
	return m
}

// typeBurst replays text the way a Windows console delivers a paste: one key
// event per character, back to back, with a bare Enter for every newline.
// gaps, when given, are extra delays injected before those character indexes —
// a terminal hands a large paste over in chunks, not all at once.
func typeBurst(m promptModel, text string, gaps ...int) promptModel {
	slow := map[int]bool{}
	for _, g := range gaps {
		slow[g] = true
	}
	for i, r := range []rune(text) {
		gap := pasteBurstGap / 4
		switch {
		case i == 0:
			gap = time.Second // the first character is indistinguishable from typing
		case slow[i]:
			gap = 3 * pasteBurstGap // a chunk boundary
		}
		var msg tea.KeyMsg
		switch r {
		case '\n':
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case '\t':
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		}
		m, _ = send(m, msg, gap)
	}
	return m
}

const snippet = "def hello(name):\n\tgreeting = f\"hi {name}\"\n\treturn greeting"

// The bug: on Windows a pasted newline is a bare Enter, so each line submitted
// itself as its own message.
func TestWindowsStylePasteDoesNotSubmit(t *testing.T) {
	m := typeBurst(newPrompt(), snippet)
	if m.done {
		t.Fatal("a pasted newline submitted the prompt")
	}
	if got := m.value(); got != snippet {
		t.Errorf("value = %q, want %q", got, snippet)
	}
}

// Tabs and newlines must survive verbatim: textinput's sanitizer replaces both
// with spaces, which would flatten the indentation of any pasted code.
func TestPasteKeepsIndentation(t *testing.T) {
	m := typeBurst(newPrompt(), snippet)
	got := m.value()
	if !strings.Contains(got, "\n\tgreeting") {
		t.Errorf("tab indentation lost: %q", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("newlines lost: %q", got)
	}
}

// Bracketed paste (Unix) hands the whole block over at once.
func TestBracketedPaste(t *testing.T) {
	m := newPrompt()
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(snippet), Paste: true}, time.Second)
	if m.done {
		t.Fatal("a bracketed paste submitted the prompt")
	}
	if got := m.value(); got != snippet {
		t.Errorf("value = %q, want %q", got, snippet)
	}
}

// The prompt is a single-line widget, so pasted newlines and tabs live on it as
// marks rather than as control characters.
func TestPastedLineBreaksShowAsMarks(t *testing.T) {
	m := typeBurst(newPrompt(), snippet)
	shown := m.ti.Value()
	if strings.ContainsAny(shown, "\n\t") {
		t.Errorf("input line holds raw control characters: %q", shown)
	}
	if strings.Count(shown, string(newlineMark)) != 2 {
		t.Errorf("input line = %q, want two newline marks", shown)
	}
	if strings.Count(shown, string(tabMark)) != 2 {
		t.Errorf("input line = %q, want two tab marks", shown)
	}
}

// A paste arrives in chunks with real gaps between them; it must not fragment
// into separate pastes, and it must not reorder.
func TestChunkedPasteStaysWhole(t *testing.T) {
	m := typeBurst(newPrompt(), snippet, 8, 20, 33, 44)
	if m.done {
		t.Fatal("a chunked paste submitted the prompt")
	}
	if got := m.value(); got != snippet {
		t.Errorf("value = %q, want %q", got, snippet)
	}
}

// Typed text around a paste is kept, in the right order.
func TestPasteInsertsAtTheCursor(t *testing.T) {
	m := newPrompt()
	for _, r := range "fix " {
		m = typeRune(m, r, time.Second)
	}
	m = typeBurst(m, snippet)
	for _, r := range " please" {
		m = typeRune(m, r, time.Second)
	}
	want := "fix " + snippet + " please"
	if got := m.value(); got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

// Two pastes in a row must concatenate, not interleave.
func TestTwoPastes(t *testing.T) {
	m := typeBurst(newPrompt(), "one\ntwo")
	m = typeBurst(m, "three\nfour")
	if got, want := m.value(), "one\ntwothree\nfour"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

// Backspace over a mark removes that line break, like any other character.
func TestMarksAreEditable(t *testing.T) {
	m := typeBurst(newPrompt(), "one\ntwo")
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyBackspace}, time.Second) // deletes "o"
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyBackspace}, time.Second) // deletes "w"
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyBackspace}, time.Second) // deletes "t"
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyBackspace}, time.Second) // deletes the mark
	if got := m.value(); got != "one" {
		t.Errorf("value = %q, want %q", got, "one")
	}
}

// Typing at human speed must still submit on Enter — the whole fix hinges on
// telling the two apart.
func TestTypedEnterStillSubmits(t *testing.T) {
	m := newPrompt()
	for _, r := range "hello" {
		m = typeRune(m, r, 80*time.Millisecond)
	}
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter}, 80*time.Millisecond)
	if !m.done {
		t.Fatal("Enter did not submit after normal typing")
	}
	if got := m.value(); got != "hello" {
		t.Errorf("value = %q", got)
	}
}

// Enter pressed right after a paste settles must submit, not extend the paste.
func TestEnterAfterPasteSubmits(t *testing.T) {
	m := typeBurst(newPrompt(), snippet)
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter}, time.Second)
	if !m.done {
		t.Fatal("Enter after a paste did not submit")
	}
	if got := m.value(); got != snippet {
		t.Errorf("value = %q, want %q", got, snippet)
	}
}

// A single-line paste is just text: nothing to mark, nothing to translate.
func TestSingleLinePasteIsLiteral(t *testing.T) {
	m := typeBurst(newPrompt(), "just one line")
	if got := m.ti.Value(); got != "just one line" {
		t.Errorf("input line = %q", got)
	}
	if got := m.value(); got != "just one line" {
		t.Errorf("value = %q", got)
	}
}

// A short fast keystroke run — rolling two keys while typing — must not be
// mistaken for a paste, or the next Enter would vanish into the line.
func TestFastKeyPairIsNotAPaste(t *testing.T) {
	m := newPrompt()
	m = typeRune(m, 'h', time.Second)
	m = typeRune(m, 'i', pasteBurstGap/4) // rolled, arriving fast
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter}, 90*time.Millisecond)
	if !m.done {
		t.Fatal("Enter after a rolled keystroke pair did not submit")
	}
	if got := m.value(); got != "hi" {
		t.Errorf("value = %q", got)
	}
}

// An Enter arriving just after a paste is ambiguous: it is either the newline a
// chunk boundary landed on, or the user sending what they pasted. It is held
// until the next keystroke settles it.
func TestEnterRightAfterPasteIsHeldThenSubmits(t *testing.T) {
	m := typeBurst(newPrompt(), "one\ntwo")
	m, cmd := send(m, tea.KeyMsg{Type: tea.KeyEnter}, 3*pasteBurstGap)
	if m.done {
		t.Fatal("the ambiguous Enter submitted before it was settled")
	}
	if !m.pendingEnter || cmd == nil {
		t.Fatal("the ambiguous Enter was not held")
	}
	// nothing follows, so the tick settles it as a submit
	next, _ := m.Update(enterDecisionMsg{seq: m.pendingSeq})
	m = next.(promptModel)
	if !m.done {
		t.Fatal("the held Enter never submitted")
	}
	if got, want := m.value(), "one\ntwo"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

// If the paste continues past that Enter, it was a line break all along.
func TestHeldEnterBecomesNewlineWhenPasteContinues(t *testing.T) {
	m := typeBurst(newPrompt(), "one\ntwo")
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter}, 3*pasteBurstGap) // chunk boundary
	m = typeRune(m, 'x', pasteBurstGap/4)
	if m.done {
		t.Fatal("a chunk-boundary Enter submitted")
	}
	if got, want := m.value(), "one\ntwo\nx"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

// A stale decision tick must not submit a line the user is still editing.
func TestStaleEnterDecisionIgnored(t *testing.T) {
	m := typeBurst(newPrompt(), "one\ntwo")
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter}, 3*pasteBurstGap)
	seq := m.pendingSeq
	m = typeRune(m, 'x', pasteBurstGap/4) // resolves the held Enter
	next, _ := m.Update(enterDecisionMsg{seq: seq})
	if next.(promptModel).done {
		t.Fatal("a stale tick submitted the line")
	}
}
