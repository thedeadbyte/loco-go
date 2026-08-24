package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// PullProgress renders an Ollama model download. pull is handed a callback to
// report bytes as they arrive; it should block until the pull finishes.
func PullProgress(pull func(report func(total, completed int64)) error) error {
	var last time.Time
	started := time.Now()
	var lastLine string
	tty := IsTTY()

	report := func(total, completed int64) {
		if total <= 0 || !tty {
			return
		}
		// repaint at most ~12x/sec: the server sends progress far faster than
		// anyone can read it, and every repaint costs a terminal round trip
		if time.Since(last) < 80*time.Millisecond && completed < total {
			return
		}
		last = time.Now()
		lastLine = pullBar(total, completed, time.Since(started))
		fmt.Fprint(Out, "\r\x1b[K"+lastLine)
	}

	err := pull(report)
	if tty && lastLine != "" {
		fmt.Fprint(Out, "\r\x1b[K")
	}
	if err != nil {
		return err
	}
	Println("  " + green.Render("model ready"))
	return nil
}

func pullBar(total, completed int64, elapsed time.Duration) string {
	const width = 24
	frac := float64(completed) / float64(total)
	if frac > 1 {
		frac = 1
	}
	filled := int(frac * width)
	bar := lipgloss.NewStyle().Foreground(Theme.Accent).
		Render(strings.Repeat("█", filled) + strings.Repeat("░", width-filled))
	speed := ""
	if s := elapsed.Seconds(); s > 0.5 {
		speed = "  " + humanBytes(int64(float64(completed)/s)) + "/s"
	}
	return fmt.Sprintf("  %s %s/%s%s", bar, humanBytes(completed),
		humanBytes(total), dim.Render(speed))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
