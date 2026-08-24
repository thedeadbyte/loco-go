// Package agent is the agentic loop: stream model output, execute tool calls,
// repeat.
//
// UI concerns are injected as callbacks so the loop stays headless and
// testable — nothing in here knows about terminals, colors, or rendering.
package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/thedeadbyte/loco-go/internal/ollama"
	"github.com/thedeadbyte/loco-go/internal/toolcall"
	"github.com/thedeadbyte/loco-go/internal/tools"
)

const (
	// MaxToolRounds bounds one Ask: a model that keeps calling tools without
	// ever answering has to be stopped somewhere.
	MaxToolRounds = 25
	// compactAt is the fraction of num_ctx at which history is summarized.
	compactAt = 0.80
	// keepTail is how many trailing messages survive compaction verbatim.
	keepTail = 6
)

// systemPromptTmpl is a heavily example-driven behavior spec, not boilerplate.
// Small local models over-trigger tools, so it teaches that "write a haiku"
// means text, not write_file. Treat it as tuned behavior.
const systemPromptTmpl = `You are loco, an agentic coding assistant running locally on the user's machine (%s, cwd: %s).

Every turn, first decide: does the user want you to ACT on their files/system, or just RESPOND? Default to responding in plain text. Only use a tool when the task truly needs reading or changing something on disk, fetching a page, or running a command.

Follow these examples exactly:
- User: "write a haiku about the ocean" -> reply with the haiku as text. No tool.
- User: "explain what a closure is" -> explain in text. No tool.
- User: "give me a regex for emails" -> give it in text. No tool.
- User: "what's in this folder?" -> call list_dir.
- User: "look at app.py" -> call read_file (never ask them to paste it).
- User: "fix the bug in app.py" -> read_file, then edit_file.
- User: "create a script that pings a host" -> write_file.
- User: "run the tests" -> call %s.
- User: "read the docs at https://..." -> call fetch_url (never curl/wget/Invoke-WebRequest).

Notice: "write a haiku", "write a function to show me", and similar content requests are answered in TEXT — the word "write" does not mean create a file. Only create or edit a file when the user clearly wants something saved or changed on disk.

write_file, edit_file, and %s ask the user for approval. If the user declines, do NOT try again — answer their request directly in text.

Invoke tools through the function-calling mechanism, never as JSON text in your reply. Keep responses concise; summarize any file change in a sentence.`

// Usage tracks what the last turn cost.
type Usage struct {
	PromptTokens int
	EvalTokens   int
	TokPerSec    float64
	CtxTokens    int // best estimate of tokens now in the context window
}

// BuildSystemPrompt renders the system prompt, appending LOCO.md when present.
func BuildSystemPrompt(projectMemory string) string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	base := fmt.Sprintf(systemPromptTmpl, osName(), cwd, tools.ShellTool, tools.ShellTool)
	if m := strings.TrimSpace(projectMemory); m != "" {
		base += "\n\n# Project notes (from LOCO.md)\n" +
			"The user maintains these project-specific instructions. Follow them:\n\n" + m
	}
	return base
}

// osName mirrors Python's platform.system() + release() closely enough for the
// model to know what kind of machine it is on.
func osName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

// Agent owns the message history and the tool-execution loop.
type Agent struct {
	Client        *ollama.Client
	Model         string
	NumCtx        int
	ProjectMemory string
	// ToolsEnabled is flipped by /chat for tool-free conversation.
	ToolsEnabled bool
	Usage        Usage
	// Messages is the full history; index 0 is always the system prompt.
	Messages []ollama.Message

	// Approve returns true to run a tool, false to refuse on the user's behalf.
	Approve func(name string, args map[string]any) bool
	// OnToken receives streamed assistant text.
	OnToken func(text string)
	// OnTool fires when a tool call is about to run (after approval).
	OnTool func(name string, args map[string]any)
	// OnToolResult fires when a tool finishes.
	OnToolResult func(name, result string)
}

// New builds an agent with no-op callbacks; the caller wires its own.
func New(client *ollama.Client, model string, numCtx int, projectMemory string) *Agent {
	a := &Agent{
		Client: client, Model: model, NumCtx: numCtx,
		ProjectMemory: projectMemory, ToolsEnabled: true,
		Approve:      func(string, map[string]any) bool { return true },
		OnToken:      func(string) {},
		OnTool:       func(string, map[string]any) {},
		OnToolResult: func(string, string) {},
	}
	a.Messages = []ollama.Message{a.system()}
	return a
}

func (a *Agent) system() ollama.Message {
	return ollama.Message{Role: "system", Content: BuildSystemPrompt(a.ProjectMemory)}
}

// Clear wipes the conversation, keeping only the system prompt.
func (a *Agent) Clear() {
	a.Messages = []ollama.Message{a.system()}
	a.Usage = Usage{}
}

// ReloadMemory re-renders the system prompt in place after LOCO.md changes.
func (a *Agent) ReloadMemory(memory string) {
	a.ProjectMemory = memory
	a.Messages[0] = a.system()
}

// ---------------------------------------------------------------- token budget

// EstimateTokens is the best guess at current context size: Ollama's real count
// from the last turn when available, else a chars/4 heuristic.
func (a *Agent) EstimateTokens() int {
	if a.Usage.CtxTokens > 0 {
		return a.Usage.CtxTokens
	}
	chars := 0
	for _, m := range a.Messages {
		chars += len(m.Content)
	}
	return chars / 4
}

// NeedsCompaction reports whether the context is close enough to full that
// older history should be summarized before the next turn.
func (a *Agent) NeedsCompaction() bool {
	return a.EstimateTokens() > int(float64(a.NumCtx)*compactAt)
}

// Compact summarizes older history into one note, keeping the recent tail.
// Returns a short status string for the UI.
func (a *Agent) Compact(ctx context.Context) (string, error) {
	if len(a.Messages) <= keepTail+1 {
		return "nothing to compact yet", nil
	}

	// keep a coherent tail: start at the last user message so we never orphan a
	// tool result from the assistant tool_calls that produced it
	tailStart := len(a.Messages) - keepTail
	for tailStart > 1 && a.Messages[tailStart].Role != "user" {
		tailStart--
	}
	older := a.Messages[1:tailStart]
	if len(older) == 0 {
		return "nothing to compact yet", nil
	}
	tail := append([]ollama.Message(nil), a.Messages[tailStart:]...)

	var convo []string
	for _, m := range older {
		text := m.Content
		if len(m.ToolCalls) > 0 {
			names := make([]string, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				n := c.Function.Name
				if n == "" {
					n = "?"
				}
				names = append(names, n)
			}
			text = strings.TrimSpace(text + " [called: " + strings.Join(names, ", ") + "]")
		}
		convo = append(convo, m.Role+": "+text)
	}

	req := []ollama.Message{
		{Role: "system", Content: "You compress conversations. Produce a terse " +
			"briefing that preserves decisions made, files created or edited, " +
			"commands run, and any open tasks. Use short bullet lines."},
		{Role: "user", Content: "Summarize this conversation:\n\n" + strings.Join(convo, "\n")},
	}
	var parts strings.Builder
	err := a.Client.Chat(ctx, a.Model, req, nil, a.NumCtx, func(chunk ollama.Chunk) error {
		parts.WriteString(chunk.Message.Content)
		return nil
	})
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(parts.String())
	if summary == "" {
		summary = "(summary unavailable)"
	}

	a.Messages = append([]ollama.Message{
		a.system(),
		{Role: "assistant", Content: "[Summary of earlier conversation]\n" + summary},
	}, tail...)
	a.Usage.CtxTokens = 0 // will be re-measured on the next turn
	return fmt.Sprintf("compacted %d messages into a summary", len(older)), nil
}

// ---------------------------------------------------------------- main loop

func (a *Agent) recordUsage(chunk ollama.Chunk, aggEval *int, aggDurNs *int64) {
	if chunk.EvalCount != nil {
		*aggEval += *chunk.EvalCount
	}
	if chunk.EvalDuration != nil {
		*aggDurNs += *chunk.EvalDuration
	}
	if chunk.PromptEvalCount != nil {
		a.Usage.PromptTokens = *chunk.PromptEvalCount
		ev := 0
		if chunk.EvalCount != nil {
			ev = *chunk.EvalCount
		}
		a.Usage.CtxTokens = *chunk.PromptEvalCount + ev
	}
}

// Ask runs one user turn to completion: stream, run any tools, repeat until the
// model answers in text or the round limit is hit.
func (a *Agent) Ask(ctx context.Context, userText string) error {
	a.Messages = append(a.Messages, ollama.Message{Role: "user", Content: userText})
	aggEval := 0
	var aggDurNs int64
	// when the user declines a tool, the next round runs with tools removed so
	// the model is forced to just answer in text instead of dead-ending
	suppressTools := false
	finished := false

	for round := 0; round < MaxToolRounds; round++ {
		var activeTools []map[string]any
		if !suppressTools && a.ToolsEnabled {
			activeTools = tools.Specs
		}
		suppressTools = false

		var content strings.Builder
		var toolCalls []ollama.ToolCall
		err := a.Client.Chat(ctx, a.Model, a.Messages, activeTools, a.NumCtx,
			func(chunk ollama.Chunk) error {
				if c := chunk.Message.Content; c != "" {
					content.WriteString(c)
					a.OnToken(c)
				}
				if len(chunk.Message.ToolCalls) > 0 && activeTools != nil {
					toolCalls = append(toolCalls, chunk.Message.ToolCalls...)
				}
				if chunk.Done {
					a.recordUsage(chunk, &aggEval, &aggDurNs)
				}
				return nil
			})
		if err != nil {
			a.finishUsage(aggEval, aggDurNs)
			return err
		}

		text := content.String()
		if activeTools != nil && len(toolCalls) == 0 && text != "" {
			toolCalls = toolcall.Extract(text)
		}

		assistant := ollama.Message{Role: "assistant", Content: text}
		if len(toolCalls) > 0 {
			assistant.ToolCalls = toolCalls
		}
		a.Messages = append(a.Messages, assistant)

		if len(toolCalls) == 0 {
			finished = true // model answered in text — done
			break
		}

		declined := false
		for _, call := range toolCalls {
			if err := ctx.Err(); err != nil {
				a.finishUsage(aggEval, aggDurNs)
				return err
			}
			name := call.Function.Name
			if name == "" {
				name = "?"
			}
			args := map[string]any(call.Function.Arguments)
			if args == nil {
				args = map[string]any{}
			}

			var result string
			if tools.NeedsApproval[name] && !a.Approve(name, args) {
				result = "The user declined this action. Do not attempt it again " +
					"— respond to their request directly in text."
				declined = true
			} else {
				a.OnTool(name, args)
				result = tools.Run(name, args)
			}
			a.OnToolResult(name, result)
			a.Messages = append(a.Messages, ollama.Message{
				Role: "tool", ToolName: name, Content: result})
		}

		// after a decline, force the next round to answer without tools so a
		// small model can't just re-issue the same rejected call
		if declined {
			suppressTools = true
		}
	}
	if !finished {
		a.OnToken("\n[loco: stopped after too many tool rounds]")
	}

	a.finishUsage(aggEval, aggDurNs)
	return nil
}

func (a *Agent) finishUsage(aggEval int, aggDurNs int64) {
	a.Usage.EvalTokens = aggEval
	if aggDurNs > 0 {
		a.Usage.TokPerSec = float64(aggEval) / (float64(aggDurNs) / 1e9)
	} else {
		a.Usage.TokPerSec = 0
	}
}
