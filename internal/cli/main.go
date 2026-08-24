package cli

import (
	"context"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/pflag"

	"github.com/thedeadbyte/loco-go/internal/agent"
	"github.com/thedeadbyte/loco-go/internal/config"
	"github.com/thedeadbyte/loco-go/internal/ollama"
	"github.com/thedeadbyte/loco-go/internal/themes"
	"github.com/thedeadbyte/loco-go/internal/ui"
	"github.com/thedeadbyte/loco-go/internal/util"
)

const usage = `LOcal COde — agentic coding CLI for local models

usage: loco [options] [prompt ...]
       loco doctor
       loco profile {list|save|delete|use} ...
`

// Run is the process entry point; it returns the exit code.
func Run(argv []string) int {
	// `profile` and `doctor` are dispatched by hand, before flag parsing: the
	// main parser's prompt is a free-form positional that would swallow them
	if len(argv) > 0 && argv[0] == "profile" {
		return cmdProfile(argv[1:])
	}
	runDoctor := len(argv) > 0 && argv[0] == "doctor"
	if runDoctor {
		argv = argv[1:]
	}

	fs := pflag.NewFlagSet("loco", pflag.ContinueOnError)
	fs.SortFlags = false
	model := fs.StringP("model", "m", "", "override model for this run")
	profileName := fs.StringP("profile", "p", "", "use a named profile")
	numCtx := fs.Int("ctx", 0, "override context window size")
	host := fs.String("host", "", "override Ollama host URL")
	theme := fs.String("theme", "", "color scheme (see /theme for the list)")
	yolo := fs.Bool("yolo", false, "skip approval prompts (use with care)")
	assumeYes := fs.BoolP("yes", "y", false, "assume yes for model download prompts")
	showVersion := fs.Bool("version", false, "print version and exit")
	help := fs.BoolP("help", "h", false, "show this help")
	fs.Usage = func() {
		ui.Println(usage)
		ui.Println(fs.FlagUsages())
	}
	if err := fs.Parse(argv); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		ui.Println(red.Render(err.Error()))
		return 2
	}
	if *help {
		fs.Usage()
		return 0
	}
	if *showVersion {
		ui.Println("loco " + Version)
		return 0
	}

	themeName := *theme
	if themeName == "" {
		themeName = config.Theme()
	}
	ui.SetTheme(themes.Get(themeName))

	prof, err := config.Resolve(*profileName)
	if err != nil {
		ui.Println(red.Render("loco: " + err.Error()))
		return 2
	}
	if *model != "" {
		prof.Model = *model
	}
	if *numCtx > 0 {
		prof.NumCtx = *numCtx
	}
	if *host != "" {
		prof.OllamaHost = *host
	}

	client := ollama.New(prof.OllamaHost)

	if runDoctor {
		return cmdDoctor(prof, client)
	}
	if !ensureModel(client, &prof, *assumeYes) {
		return 1
	}

	memory := ""
	found := util.FindMemory("")
	if found != nil {
		memory = found.Text
	}
	a := agent.New(client, prof.Model, prof.NumCtx, memory)

	if prompt := strings.Join(fs.Args(), " "); prompt != "" {
		return oneShot(a, prof, prompt, *yolo)
	}

	cwd, _ := os.Getwd()
	memPath := ""
	if found != nil {
		memPath = found.Path
	}
	ui.PrintBanner(Version, prof, cwd, util.GitBranch(cwd), memPath)
	repl(a, &prof, client, *yolo)
	return 0
}

// oneShot runs a single non-interactive prompt and exits.
func oneShot(a *agent.Agent, prof config.Profile, prompt string, yolo bool) int {
	renderer := ui.NewStream()
	// no session allow-list: "always" has no meaning when there is no session
	wireUI(a, renderer, yolo, nil)

	text, _, missing := util.ExpandMentions(prompt)
	if len(missing) > 0 {
		ui.Println(yellowText("  ⚠ no such file: " + joinAt(missing)))
	}

	renderer.Start("")
	err := withInterrupt(func(ctx context.Context) error { return a.Ask(ctx, text) })
	renderer.Stop()
	switch {
	case isCancel(err):
		ui.Println(dim.Render("interrupted"))
		return 130
	case err != nil:
		ui.Println(red.Render("ollama error: " + err.Error()))
		return 1
	}
	ui.ShowUsage(usageOf(a), a.NumCtx)
	return 0
}

func yellowText(s string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(s)
}

func greenText(s string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(s)
}

func accentText(s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(ui.Theme.Accent).Render(s)
}
