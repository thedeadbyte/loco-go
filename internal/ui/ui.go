// Package ui is all of loco's rendering: banner, streaming markdown, approval
// prompts, and the input line. Nothing outside this package should write to
// the terminal directly.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/thedeadbyte/loco-go/internal/config"
	"github.com/thedeadbyte/loco-go/internal/themes"
	"github.com/thedeadbyte/loco-go/internal/tools"
)

// Out is where all output goes; swapped in tests.
var Out io.Writer = os.Stdout

// Theme is the active color scheme, set once at startup and swapped by /theme.
var Theme = themes.Get("")

// SetTheme swaps the active theme and drops any cached markdown renderer built
// against the old one.
func SetTheme(t themes.Theme) {
	Theme = t
	resetRenderer()
}

// Common styles that don't depend on the theme.
var (
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	bold   = lipgloss.NewStyle().Bold(true)
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

// Printf writes a formatted line to the terminal.
func Printf(format string, a ...any) { fmt.Fprintf(Out, format, a...) }

// Println writes one line to the terminal.
func Println(s string) { fmt.Fprintln(Out, s) }

// Width is the terminal width, with a sane fallback when stdout isn't a TTY.
func Width() int { w, _ := termSize(); return w }

// Height is the terminal height, with a sane fallback.
func Height() int { _, h := termSize(); return h }

// termSize and ttyCheck are indirected so tests can drive the renderer against
// a fixed-size fake terminal.
var (
	termSize = func() (int, int) {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
			return w, h
		}
		return 80, 24
	}
	ttyCheck = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
)

// IsTTY reports whether stdout is a terminal. When it isn't, loco drops the
// live-rendering region and just prints, so piped output stays clean.
func IsTTY() bool { return ttyCheck() }

const banner = `
  ██╗      ██████╗  ██████╗ ██████╗
  ██║     ██╔═══██╗██╔════╝██╔═══██╗
  ██║     ██║   ██║██║     ██║   ██║
  ███████╗╚██████╔╝╚██████╗╚██████╔╝
  ╚══════╝ ╚═════╝  ╚═════╝ ╚═════╝
`

// PrintBanner draws the startup header.
func PrintBanner(version string, prof config.Profile, cwd, branch, memory string) {
	Println(Theme.BannerStyle().Render(banner))
	accent := lipgloss.NewStyle().Bold(true).Foreground(Theme.Accent)
	line := fmt.Sprintf("  %s v%s — model %s · profile %s · ctx %d",
		bold.Render("LOcal COde"), version, accent.Render(prof.Model),
		bold.Render(prof.Name), prof.NumCtx)
	if branch != "" {
		line += " · " + lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(" "+branch)
	}
	Println(line)
	Println(dim.Render("  " + cwd))
	if memory != "" {
		Println(dim.Render("  loaded project memory from " + memory))
	}
	Println(dim.Render("  /help for commands · @file to add a file · " +
		"Ctrl-C to interrupt · Ctrl-D to quit"))
	Println("")
}

// ShowToolCall prints the ⏺ marker for a tool that is about to run.
func ShowToolCall(name, summary string) {
	marker := lipgloss.NewStyle().Bold(true).Foreground(Theme.Tool)
	Println("  " + marker.Render("⏺ "+name) + " " + dim.Render(summary))
}

// ShowToolResult prints the ⎿ one-line summary of what a tool returned.
func ShowToolResult(result string) {
	lines := strings.Split(result, "\n")
	first := ""
	if len(lines) > 0 {
		first = lines[0]
	}
	if len(first) > 120 {
		first = first[:120]
	}
	if more := len(lines) - 1; more > 0 {
		first += fmt.Sprintf(" [+%d lines]", more)
	}
	style := lipgloss.NewStyle().Foreground(Theme.Result)
	if strings.HasPrefix(result, "error") {
		style = red
	}
	Println("    " + style.Render("⎿ "+first))
}

// ---------------------------------------------------------------- approval

// colorizeDiff styles a unified diff the way a reviewer expects to see it.
func colorizeDiff(diff string) string {
	var b strings.Builder
	grey := lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	for i, ln := range strings.Split(diff, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
			b.WriteString(bold.Render(ln))
		case strings.HasPrefix(ln, "+"):
			b.WriteString(green.Render(ln))
		case strings.HasPrefix(ln, "-"):
			b.WriteString(red.Render(ln))
		case strings.HasPrefix(ln, "@@"):
			b.WriteString(cyan.Render(ln))
		default:
			b.WriteString(grey.Render(ln))
		}
	}
	return b.String()
}

// panel draws a titled box around body, sized to its content.
func panel(title, body string, border lipgloss.Style) string {
	lines := strings.Split(body, "\n")
	inner := ansi.StringWidth(title) + 2
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > inner {
			inner = w
		}
	}
	if max := Width() - 4; inner > max && max > 10 {
		inner = max
	}
	var b strings.Builder
	b.WriteString(border.Render("╭─ ") + bold.Render(title) + " " +
		border.Render(strings.Repeat("─", maxInt(1, inner-ansi.StringWidth(title)-1))+"╮") + "\n")
	for _, l := range lines {
		pad := inner - ansi.StringWidth(l)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(border.Render("│ ") + l + strings.Repeat(" ", pad) +
			border.Render(" │") + "\n")
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", inner+2) + "╯"))
	return b.String()
}

// Decision is the outcome of an approval prompt.
type Decision int

const (
	DeclineOnce Decision = iota
	AllowOnce
	AllowAlways
)

// AskApproval prompts to run a tool. preview, when set, is a unified diff shown
// colorized in the panel so the user approves the exact change, not a byte count.
func AskApproval(name, summary, preview string) Decision {
	body := bold.Render(summary)
	if preview != "" {
		body += "\n\n" + colorizeDiff(preview)
	}
	Println(panel("approve "+name+"?", body, yellow))
	fmt.Fprint(Out, yellow.Render("  run? [y]es / [a]lways this tool / [N]o")+" ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return DeclineOnce
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "a", "always":
		return AllowAlways
	case "y", "yes":
		return AllowOnce
	}
	return DeclineOnce
}

// Confirm asks a [Y/n] question, defaulting to yes.
func Confirm(question string) bool {
	fmt.Fprint(Out, question+" [Y/n] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "n", "no":
		return false
	}
	return true
}

// ---------------------------------------------------------------- usage

// HumanizeTokens renders a token count compactly (1234 -> "1.2k").
func HumanizeTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// ContextBar renders the context-window gauge shown by /context.
func ContextBar(used, total, width int) string {
	frac := 0.0
	if total > 0 {
		frac = float64(used) / float64(total)
		if frac > 1 {
			frac = 1
		}
	}
	filled := int(frac * float64(width))
	pct := int(frac * 100)
	color := "10"
	switch {
	case pct >= 90:
		color = "9"
	case pct >= 70:
		color = "11"
	}
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).
		Render(strings.Repeat("█", filled) + strings.Repeat("░", width-filled))
	return fmt.Sprintf("%s %s/%s (%d%%)", bar, HumanizeTokens(used),
		HumanizeTokens(total), pct)
}

// Usage is the subset of agent.Usage this package needs, kept as its own type
// so ui doesn't import agent.
type Usage struct {
	EvalTokens int
	TokPerSec  float64
	CtxTokens  int
}

// ShowUsage prints the ⚡ speed/token line after a reply.
func ShowUsage(u Usage, numCtx int) {
	if u.EvalTokens == 0 {
		return
	}
	rate := ""
	if u.TokPerSec > 0 {
		rate = fmt.Sprintf("%.1f tok/s · ", u.TokPerSec)
	}
	Println(dim.Render(fmt.Sprintf("  ⚡ %s%d tokens out · ctx %s/%s",
		rate, u.EvalTokens, HumanizeTokens(u.CtxTokens), HumanizeTokens(numCtx))))
}

// ShowThemes previews every theme's accent so the user can pick by eye.
func ShowThemes(current string) {
	Println("  " + bold.Render("themes") + " (use " + bold.Render("/theme NAME") + " to switch):")
	for _, name := range themes.Names() {
		t := themes.Get(name)
		mark := " "
		if name == current {
			mark = "→"
		}
		swatch := lipgloss.NewStyle().Foreground(t.Accent).Render("⏺ ██") + " " +
			lipgloss.NewStyle().Foreground(t.Tool).Render("tool") + " " +
			lipgloss.NewStyle().Foreground(t.Result).Render("⎿ result")
		label := lipgloss.NewStyle().Bold(true).Foreground(t.Accent).
			Render(fmt.Sprintf("%-8s", name))
		Printf("  %s %s %s\n", mark, label, swatch)
	}
}

// ShowTools lists the tools the model can call.
func ShowTools() {
	for _, s := range tools.Described() {
		Println("  " + bold.Render(s.Name) + " — " + s.Description)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
