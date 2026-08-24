package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Pasting into the prompt has to be detected, not merely received.
//
// On Unix the terminal brackets a paste (ESC[200~ … ESC[201~) and bubbletea
// hands it over as one KeyMsg with Paste set. On Windows it does not: bubbletea
// reads the console API rather than VT input, so ENABLE_VIRTUAL_TERMINAL_INPUT
// is never set and no bracket markers exist. Every pasted character arrives as
// its own KeyMsg and every newline arrives as VK_RETURN — indistinguishable
// from the user pressing Enter, which is why a pasted snippet used to submit
// itself one line at a time and lose everything after the first.
//
// Timing is the only signal left, so pastes are recognised by how fast the keys
// arrive. Two things keep that honest:
//
//   - A run of at least minBurstKeys keys must arrive back to back before
//     anything counts as a paste, so an ordinary fast keystroke pair can't make
//     the next Enter disappear.
//   - Once in a paste, it stays one until input has been quiet for pasteIdleGap.
//     A terminal delivers a large paste in chunks with real gaps between them,
//     and without this the paste would fragment at every chunk boundary.
const (
	// 20ms between keystrokes is ~50 characters a second sustained, well past
	// human typing speed.
	pasteBurstGap = 20 * time.Millisecond
	pasteIdleGap  = 120 * time.Millisecond
	minBurstKeys  = 3
	// How long to wait before believing an Enter that arrived during a paste
	// but not inside a chunk of one. Such an Enter is genuinely ambiguous — it
	// is either the newline a chunk boundary landed on, or the user submitting
	// the paste they just made — and the difference only shows up in what comes
	// next. Deferring the decision this long costs nothing noticeable, and only
	// ever applies just after a paste; ordinary typing submits immediately.
	enterDecideGap = 40 * time.Millisecond
)

// enterDecisionMsg resolves a deferred Enter. seq guards against a tick from an
// earlier Enter deciding a later one.
type enterDecisionMsg struct{ seq int }

// Pasted newlines and tabs are held on the input line as marks rather than as
// control characters. textinput's sanitizer replaces a real newline or tab with
// a space, which would silently flatten the indentation of pasted code, and a
// single-line widget cannot render one anyway. The marks are ordinary runes: they
// insert, move and delete like any other character, keeping the line in order no
// matter how the paste was chopped up, and value() turns them back on the way out.
const (
	newlineMark = '⏎'
	tabMark     = '⇥'
)

var (
	marksToText = strings.NewReplacer(string(newlineMark), "\n", string(tabMark), "\t")
	textToMarks = strings.NewReplacer("\r\n", string(newlineMark), "\r", string(newlineMark),
		"\n", string(newlineMark), "\t", string(tabMark))
)

// noteKey records a keystroke's arrival. fast means it came too quickly to have
// been typed; pasting means a paste is in flight, across chunk gaps included.
func (m promptModel) noteKey() (_ promptModel, fast, pasting bool) {
	now := time.Now()
	fast = now.Sub(m.lastKeyAt) < pasteBurstGap
	if fast {
		m.fastRun++
	} else {
		m.fastRun = 1
	}
	m.lastKeyAt = now

	pasting = m.fastRun >= minBurstKeys || now.Before(m.pasteUntil)
	if pasting {
		m.pasteUntil = now.Add(pasteIdleGap)
	}
	return m, fast, pasting
}

// deferEnter holds an ambiguous Enter until the next keystroke settles it.
func (m promptModel) deferEnter() (promptModel, tea.Cmd) {
	m.pendingEnter = true
	m.pendingSeq++
	seq := m.pendingSeq
	return m, tea.Tick(enterDecideGap, func(time.Time) tea.Msg {
		return enterDecisionMsg{seq: seq}
	})
}

// resolvePendingEnter turns a held Enter into a line break, which is what it
// must have been if more input followed it.
func (m promptModel) resolvePendingEnter() promptModel {
	if !m.pendingEnter {
		return m
	}
	m.pendingEnter = false
	return m.insertRunes(string(newlineMark))
}

// insertRunes puts text into the input through the widget, so the cursor and
// any surrounding text behave exactly as they do for typing.
func (m promptModel) insertRunes(s string) promptModel {
	if s == "" {
		return m
	}
	ti, _ := m.ti.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	m.ti = ti
	return m
}

// insertPaste inserts a whole pasted block, marks and all.
func (m promptModel) insertPaste(text string) promptModel {
	return m.insertRunes(textToMarks.Replace(text))
}

// value is the line to submit: what is on screen, with the marks turned back
// into the newlines and tabs they stand for.
func (m promptModel) value() string {
	return marksToText.Replace(m.ti.Value())
}
