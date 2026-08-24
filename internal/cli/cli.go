// Package cli is loco's command line entry point and interactive REPL.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/pflag"

	"github.com/thedeadbyte/loco-go/internal/config"
	"github.com/thedeadbyte/loco-go/internal/ollama"
	"github.com/thedeadbyte/loco-go/internal/ui"
)

// Version is loco's version, also reported by --version.
const Version = "0.1.0"

var (
	bold = lipgloss.NewStyle().Bold(true)
	dim  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	red  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// ---------------------------------------------------------------- profiles CLI

func cmdProfile(argv []string) int {
	sub := ""
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		sub, argv = argv[0], argv[1:]
	}

	switch sub {
	case "save":
		fs := pflag.NewFlagSet("loco profile save", pflag.ContinueOnError)
		model := fs.StringP("model", "m", "", "model to use")
		ctx := fs.Int("ctx", 0, "context window size")
		host := fs.String("host", "", "Ollama host URL")
		bind := fs.Bool("bind-host", false, "auto-select this profile on this machine")
		if err := fs.Parse(argv); err != nil {
			return 2
		}
		name := fs.Arg(0)
		if name == "" {
			ui.Println("usage: loco profile save NAME [-m MODEL] [--ctx N] [--host URL] [--bind-host]")
			return 2
		}
		opts := config.SaveOpts{BindHost: *bind}
		if fs.Changed("model") {
			opts.Model = model
		}
		if fs.Changed("ctx") {
			opts.NumCtx = ctx
		}
		if fs.Changed("host") {
			opts.OllamaHost = host
		}
		prof, err := config.SaveProfile(name, opts)
		if err != nil {
			ui.Println(red.Render("could not save config: " + err.Error()))
			return 1
		}
		bound := ""
		if *bind {
			bound = fmt.Sprintf(" (bound to host '%s')", config.Hostname())
		}
		ui.Printf("saved profile %s: model=%s ctx=%d%s\n", bold.Render(prof.Name),
			prof.Model, prof.NumCtx, bound)
		return 0

	case "delete":
		name := firstArg(argv)
		ok, err := config.DeleteProfile(name)
		if err != nil {
			ui.Println(red.Render("could not save config: " + err.Error()))
			return 1
		}
		if ok {
			ui.Printf("deleted profile %s\n", bold.Render(name))
			return 0
		}
		ui.Printf("no profile named '%s'\n", name)
		return 1

	case "use":
		name := firstArg(argv)
		ok, err := config.SetDefaultProfile(name)
		if err != nil {
			ui.Println(red.Render("could not save config: " + err.Error()))
			return 1
		}
		if ok {
			ui.Printf("default profile set to %s\n", bold.Render(name))
			return 0
		}
		ui.Printf("no profile named '%s'\n", name)
		return 1
	}

	// list (default)
	profs := config.List(nil)
	if len(profs) == 0 {
		ui.Println("no profiles yet — create one with:\n  " +
			bold.Render("loco profile save NAME -m MODEL --ctx N --bind-host"))
		return 0
	}
	active, _ := config.Resolve("")
	cfg := config.Load()
	ui.Printf("profiles  (host: %s, default: %s)\n", config.Hostname(),
		config.DefaultProfileName(cfg))
	rows := [][]string{{"", "name", "model", "ctx", "ollama host", "bound hosts"}}
	for _, p := range profs {
		mark := ""
		if p.Name == active.Name {
			mark = "→"
		}
		hosts := strings.Join(p.Hosts, ", ")
		if hosts == "" {
			hosts = "-"
		}
		rows = append(rows, []string{mark, p.Name, p.Model,
			fmt.Sprintf("%d", p.NumCtx), p.OllamaHost, hosts})
	}
	printTable(rows)
	return 0
}

// printTable renders left-aligned columns padded to their widest cell.
func printTable(rows [][]string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	for n, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(fmt.Sprintf("%-*s", widths[i], c))
			if i < len(r)-1 {
				b.WriteString("  ")
			}
		}
		line := strings.TrimRight(b.String(), " ")
		if n == 0 {
			line = dim.Render(line)
		}
		ui.Println("  " + line)
	}
}

func firstArg(argv []string) string {
	for _, a := range argv {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// ---------------------------------------------------------------- model checks

// ensureModel verifies Ollama is reachable and the profile's model is pulled,
// offering to pull it if not. Returns false when the session can't proceed.
func ensureModel(client *ollama.Client, prof *config.Profile, assumeYes bool) bool {
	if !client.IsUp() {
		// distinguish "not installed at all" from "installed but not running" —
		// the two need completely different fixes, and guessing wrong wastes
		// the user's time
		if _, err := exec.LookPath("ollama"); err != nil {
			ui.Println(red.Render("Ollama doesn't seem to be installed") +
				fmt.Sprintf(" (couldn't reach %s and no 'ollama' on PATH).\nInstall it: %s",
					prof.OllamaHost, bold.Render("https://ollama.com/download")))
		} else {
			ui.Println(red.Render(fmt.Sprintf("Ollama is installed but not responding at %s.",
				prof.OllamaHost)) + "\nStart it with " + bold.Render("ollama serve") +
				" (Linux) or launch the Ollama app (Windows/Mac). Run " +
				bold.Render("loco doctor") + " to recheck.")
		}
		return false
	}
	has, err := client.HasModel(prof.Model)
	if err != nil {
		ui.Println(red.Render("could not list models: " + err.Error()))
		return false
	}
	if has {
		return true
	}
	ui.Printf("model %s is not downloaded yet.\n", bold.Render(prof.Model))
	if !assumeYes && !ui.Confirm("pull it now?") {
		return false
	}
	err = ui.PullProgress(func(report func(total, completed int64)) error {
		return client.Pull(context.Background(), prof.Model, func(ev ollama.PullEvent) {
			report(ev.Total, ev.Completed)
		})
	})
	if err != nil {
		ui.Println(red.Render("pull failed: " + err.Error()))
		return false
	}
	return true
}

// cmdDoctor is the preflight check: verify the whole local stack, print exact fixes.
func cmdDoctor(prof config.Profile, client *ollama.Client) int {
	ui.Println(bold.Render("loco doctor") + " — checking your setup\n")
	ok := true
	pass := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✓")
	fail := red.Render("✗")
	note := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("•")

	ollamaBin, binErr := exec.LookPath("ollama")
	if binErr == nil {
		ui.Printf("  %s ollama CLI: %s\n", pass, ollamaBin)
	} else {
		ui.Printf("  %s ollama CLI: %s\n", note,
			"not on PATH (optional — loco talks to the HTTP API)")
	}

	up := client.IsUp()
	if up {
		ui.Printf("  %s Ollama server responding at %s\n", pass, client.Host)
	} else {
		ok = false
		if binErr == nil {
			ui.Printf("  %s Ollama not responding at %s — start it: %s / launch the app\n",
				fail, client.Host, bold.Render("ollama serve"))
		} else {
			ui.Printf("  %s Ollama not installed — %s\n", fail,
				bold.Render("https://ollama.com/download"))
		}
	}

	if up {
		models, err := client.Models()
		if err != nil {
			ok = false
			ui.Printf("  %s couldn't list models: %v\n", fail, err)
		} else if has, _ := client.HasModel(prof.Model); has {
			ui.Printf("  %s model %s is pulled\n", pass, bold.Render(prof.Model))
		} else {
			ok = false
			ui.Printf("  %s model %s not pulled — %s\n", fail, bold.Render(prof.Model),
				bold.Render("ollama pull "+prof.Model))
			if len(models) > 0 {
				ui.Printf("      have: %s\n", strings.Join(models, ", "))
			}
		}
	}

	cfgPath := config.Path()
	if _, err := os.Stat(cfgPath); err == nil {
		ui.Printf("  %s config: %s\n", pass, cfgPath)
	} else {
		ui.Printf("  %s config: %s\n", pass, "(none yet — using built-in defaults)")
	}
	ui.Printf("\n  active profile %s: model %s · ctx %d · host %s\n",
		bold.Render(prof.Name), prof.Model, prof.NumCtx, prof.OllamaHost)
	if ok {
		ui.Println("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("10")).
			Render("All good — loco is ready."))
		return 0
	}
	ui.Println("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("11")).
		Render("Some checks failed — fix the ✗ items above, then rerun loco doctor."))
	return 1
}

// ---------------------------------------------------------------- interrupts

// withInterrupt runs fn with a context cancelled by Ctrl-C, so a long
// generation can be abandoned without killing the session.
func withInterrupt(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			cancel()
		case <-done:
		}
	}()
	err := fn(ctx)
	close(done)
	return err
}

func isCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
