package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/thedeadbyte/loco-go/internal/agent"
	"github.com/thedeadbyte/loco-go/internal/config"
	"github.com/thedeadbyte/loco-go/internal/ollama"
	"github.com/thedeadbyte/loco-go/internal/themes"
	"github.com/thedeadbyte/loco-go/internal/tools"
	"github.com/thedeadbyte/loco-go/internal/ui"
	"github.com/thedeadbyte/loco-go/internal/util"
)

const slashHelp = `/help              show this help
/model NAME        switch model for this session
/profile [NAME]    show active profile, or switch to NAME
/theme [NAME]      list color schemes, or switch to NAME
/chat              toggle tool-free mode (just converse, no file/shell tools)
/context           show context-window usage
/compact           summarize old history to free up context
/init              scaffold a LOCO.md project-memory file
/memory            reload LOCO.md from disk
/clear             wipe conversation history
/save [FILE]       save transcript as JSON
/tools             list tools the model can use
/doctor            check Ollama, model, and config are all set up
/allow             show tools auto-approved this session
/quit              exit (also Ctrl-D)

Tip: prefix a path with @ (e.g. @src/app.py) to add its contents to your message.`

// wireUI attaches the streaming renderer and approval flow to an agent.
// sessionAllow, when non-nil, records tools the user approved for the session.
func wireUI(a *agent.Agent, r *ui.Stream, yolo bool, sessionAllow map[string]bool) {
	a.Approve = func(name string, args map[string]any) bool {
		if yolo || (sessionAllow != nil && sessionAllow[name]) {
			return true
		}
		r.PauseForPrompt()
		switch ui.AskApproval(name, tools.DescribeCall(name, args),
			tools.PreviewDiff(name, args, 60)) {
		case ui.AllowAlways:
			if sessionAllow != nil {
				sessionAllow[name] = true
			}
			return true
		case ui.AllowOnce:
			return true
		}
		return false
	}
	a.OnToken = r.Token
	a.OnTool = func(name string, args map[string]any) {
		r.PauseForPrompt()
		ui.ShowToolCall(name, tools.DescribeCall(name, args))
	}
	a.OnToolResult = func(name, result string) {
		ui.ShowToolResult(result)
		r.Start("") // spinner while the model digests the result
	}
}

func usageOf(a *agent.Agent) ui.Usage {
	return ui.Usage{EvalTokens: a.Usage.EvalTokens, TokPerSec: a.Usage.TokPerSec,
		CtxTokens: a.Usage.CtxTokens}
}

func repl(a *agent.Agent, prof *config.Profile, client *ollama.Client, yolo bool) {
	renderer := ui.NewStream()
	sessionAllow := map[string]bool{}
	wireUI(a, renderer, yolo, sessionAllow)

	input := ui.NewInput(config.HistoryPath())
	toolbar := func() string {
		used, total := a.EstimateTokens(), a.NumCtx
		pct := 0
		if total > 0 {
			pct = 100 * used / total
		}
		mode := ""
		if !a.ToolsEnabled {
			mode = " · chat (no tools)"
		}
		return fmt.Sprintf(" %s · %s · ctx %d/%d (%d%%)%s ", prof.Model, prof.Name,
			used, total, pct, mode)
	}

	for {
		line, err := input.Prompt("❯ ", toolbar)
		if err == ui.ErrInterrupted {
			continue
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			ui.Println(red.Render("input error: " + err.Error()))
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			if quit := runSlash(a, prof, client, renderer, sessionAllow, line); quit {
				break
			}
			continue
		}

		// @file mentions: pull referenced files into the message
		text, loaded, missing := util.ExpandMentions(line)
		if len(loaded) > 0 {
			ui.Println(dim.Render("  + added " + joinAt(loaded)))
		}
		if len(missing) > 0 {
			ui.Println(yellowText("  ⚠ no such file: "+joinAt(missing)) + " " +
				dim.Render("(not added — check the name)"))
		}

		if a.NeedsCompaction() {
			renderer.Start("context nearly full — compacting…")
			err := withInterrupt(func(ctx context.Context) error {
				_, err := a.Compact(ctx)
				return err
			})
			renderer.Stop()
			if err != nil && !isCancel(err) {
				ui.Println(red.Render("compaction failed: " + err.Error()))
			}
		}

		renderer.Start("")
		err = withInterrupt(func(ctx context.Context) error { return a.Ask(ctx, text) })
		renderer.Stop()
		switch {
		case isCancel(err):
			ui.Println(dim.Render("interrupted"))
		case err != nil:
			ui.Println(red.Render("ollama error: " + err.Error()))
		}
		ui.ShowUsage(usageOf(a), a.NumCtx)
	}
	ui.Println(dim.Render("bye"))
}

// runSlash handles one /command. Returns true when the session should end.
func runSlash(a *agent.Agent, prof *config.Profile, client *ollama.Client,
	renderer *ui.Stream, sessionAllow map[string]bool, line string) bool {

	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "/quit", "/exit":
		return true

	case "/help":
		ui.Println(slashHelp)

	case "/clear":
		a.Clear()
		ui.Println(dim.Render("history cleared"))

	case "/model":
		if rest != "" {
			a.Model, prof.Model = rest, rest
			ensureModel(client, prof, false)
		}
		ui.Printf("model: %s\n", bold.Render(a.Model))

	case "/profile":
		if rest != "" {
			newp, err := config.Resolve(rest)
			if err != nil {
				ui.Println(dim.Render("  " + err.Error()))
			} else {
				prof.Name, prof.Model, prof.NumCtx = newp.Name, newp.Model, newp.NumCtx
				a.Model, a.NumCtx = newp.Model, newp.NumCtx
				ensureModel(client, prof, false)
			}
		}
		ui.Printf("profile: %s · model %s · ctx %d\n", bold.Render(prof.Name),
			prof.Model, prof.NumCtx)

	case "/theme":
		if rest == "" {
			ui.ShowThemes(ui.Theme.Name)
			break
		}
		if _, ok := themes.Themes[rest]; !ok {
			ui.Println(dim.Render("no theme '" + rest + "' — try: " +
				strings.Join(themes.Names(), ", ")))
			break
		}
		t := themes.Get(rest)
		ui.SetTheme(t)
		if err := config.SetTheme(rest); err != nil {
			ui.Println(dim.Render("  (theme applied but not saved: " + err.Error() + ")"))
		}
		ui.Printf("theme set to %s\n", accentText(rest))

	case "/chat":
		a.ToolsEnabled = !a.ToolsEnabled
		if a.ToolsEnabled {
			ui.Println(dim.Render("  tools on — loco can read/edit files and run commands again"))
		} else {
			ui.Println(greenText("  chat mode — tools off. loco will just talk; /chat again to re-enable."))
		}

	case "/context":
		ui.Println("  " + ui.ContextBar(a.EstimateTokens(), a.NumCtx, 16))

	case "/compact":
		renderer.Start("compacting…")
		var msg string
		err := withInterrupt(func(ctx context.Context) error {
			var err error
			msg, err = a.Compact(ctx)
			return err
		})
		renderer.Stop()
		switch {
		case isCancel(err):
			ui.Println(dim.Render("  interrupted"))
		case err != nil:
			ui.Println(red.Render("  compaction failed: " + err.Error()))
		default:
			ui.Println(dim.Render("  " + msg))
		}

	case "/init":
		p := util.MemoryNames[0]
		if _, err := os.Stat(p); err == nil {
			ui.Println(dim.Render("  " + p + " already exists — edit it directly"))
			break
		}
		if err := os.WriteFile(p, []byte(util.MemoryTemplate), 0o644); err != nil {
			ui.Println(red.Render("  could not create " + p + ": " + err.Error()))
			break
		}
		ui.Printf("  created %s — edit it, then /memory\n", bold.Render(p))

	case "/memory":
		if found := util.FindMemory(""); found != nil {
			a.ReloadMemory(found.Text)
			ui.Println(dim.Render("  loaded project memory from " + found.Path))
		} else {
			ui.Println(dim.Render("  no LOCO.md found — /init to create one"))
		}

	case "/allow":
		if len(sessionAllow) == 0 {
			ui.Println("  auto-approved this session: " + dim.Render("none"))
			break
		}
		names := make([]string, 0, len(sessionAllow))
		for n := range sessionAllow {
			names = append(names, n)
		}
		sortStrings(names)
		ui.Println("  auto-approved this session: " + strings.Join(names, ", "))

	case "/save":
		out := rest
		if out == "" {
			out = fmt.Sprintf("loco-session-%d.json", time.Now().Unix())
		}
		// SetEscapeHTML(false): the default would turn every < > & in the
		// transcript into \u003c-style escapes, which makes saved sessions
		// painful to read
		var blob bytes.Buffer
		enc := json.NewEncoder(&blob)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		err := enc.Encode(a.Messages)
		if err == nil {
			err = os.WriteFile(out, blob.Bytes(), 0o644)
		}
		if err != nil {
			ui.Println(red.Render("could not save transcript: " + err.Error()))
			break
		}
		ui.Printf("saved transcript to %s\n", bold.Render(out))

	case "/doctor":
		cmdDoctor(*prof, client)

	case "/tools":
		ui.ShowTools()

	default:
		ui.Println(dim.Render("unknown command " + cmd + " — /help"))
	}
	return false
}

func joinAt(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "@" + n
	}
	return strings.Join(out, ", ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
