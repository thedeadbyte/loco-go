// Package themes holds the color schemes for loco's terminal UI.
//
// A theme is just a set of colors. Add one by dropping another entry in
// Themes — the fields must match what ui.go uses.
package themes

import "github.com/charmbracelet/lipgloss"

// Theme is immutable once constructed; the UI swaps whole themes, never fields.
type Theme struct {
	Name       string
	Accent     lipgloss.Color // banner, tool markers, model name
	Tool       lipgloss.Color // the ⏺ tool-call marker
	Result     lipgloss.Color // the ⎿ tool-result line
	Prompt     lipgloss.Color // the ❯ input line
	BannerBold bool           // extra weight on the banner
}

// The numbers are the xterm-256 equivalents of the rich color names the Python
// version used, so both builds render identically in the same terminal.
var Themes = map[string]Theme{
	"orchid": {Name: "orchid", Accent: "134", Tool: "134", Result: "244", Prompt: "5", BannerBold: true},
	"matrix": {Name: "matrix", Accent: "40", Tool: "46", Result: "28", Prompt: "2", BannerBold: true},
	"amber":  {Name: "amber", Accent: "214", Tool: "220", Result: "166", Prompt: "3", BannerBold: true},
	"ocean":  {Name: "ocean", Accent: "39", Tool: "51", Result: "67", Prompt: "6", BannerBold: true},
	"mono":   {Name: "mono", Accent: "252", Tool: "15", Result: "244", Prompt: "7", BannerBold: true},
	"sunset": {Name: "sunset", Accent: "197", Tool: "205", Result: "96", Prompt: "5", BannerBold: true},
}

// Order is the display order for /theme, kept stable because Go map iteration
// is randomized and a shuffling list looks broken.
var Order = []string{"orchid", "matrix", "amber", "ocean", "mono", "sunset"}

const Default = "orchid"

// Get returns the named theme, falling back to the default for "" or unknown.
func Get(name string) Theme {
	if t, ok := Themes[name]; ok {
		return t
	}
	return Themes[Default]
}

// Names lists theme names in display order.
func Names() []string {
	out := make([]string, len(Order))
	copy(out, Order)
	return out
}

// BannerStyle is the style for the ASCII banner.
func (t Theme) BannerStyle() lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(t.Accent)
	if t.BannerBold {
		s = s.Bold(true)
	}
	return s
}
