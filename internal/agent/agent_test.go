package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thedeadbyte/loco-go/internal/ollama"
	"github.com/thedeadbyte/loco-go/internal/tools"
)

// reply is one scripted model turn.
type reply struct {
	text  string
	calls []ollama.ToolCall
}

// fakeOllama serves a fixed script of replies, one per request, and records the
// requests it saw so tests can assert on what the agent sent.
type fakeOllama struct {
	mu       sync.Mutex
	script   []reply
	n        int
	requests []map[string]any
}

func call(name string, args map[string]any) ollama.ToolCall {
	return ollama.ToolCall{Function: ollama.FunctionCall{Name: name, Arguments: args}}
}

func (f *fakeOllama) server(t *testing.T) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		f.mu.Lock()
		f.requests = append(f.requests, body)
		var rep reply
		if f.n < len(f.script) {
			rep = f.script[f.n]
		} else {
			rep = reply{text: "(script exhausted)"}
		}
		f.n++
		f.mu.Unlock()

		enc := json.NewEncoder(w)
		// stream the text in small pieces so the agent's accumulation is exercised
		for i := 0; i < len(rep.text); i += 4 {
			j := min(i+4, len(rep.text))
			_ = enc.Encode(map[string]any{"message": map[string]any{"content": rep.text[i:j]}})
		}
		final := map[string]any{
			"message": map[string]any{"content": "", "tool_calls": rep.calls},
			"done":    true, "prompt_eval_count": 100, "eval_count": 20,
			"eval_duration": 1_000_000_000,
		}
		_ = enc.Encode(final)
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL)
}

func (f *fakeOllama) lastRequestHadTools() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.requests[len(f.requests)-1]["tools"]
	return ok
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// recorder captures everything the agent pushes at the UI.
type recorder struct {
	tokens  strings.Builder
	tools   []string
	results []string
	approve func(string, map[string]any) bool
}

func newAgent(t *testing.T, f *fakeOllama, rec *recorder) *Agent {
	t.Helper()
	a := New(f.server(t), "test-model", 8192, "")
	a.OnToken = func(s string) { rec.tokens.WriteString(s) }
	a.OnTool = func(name string, _ map[string]any) { rec.tools = append(rec.tools, name) }
	a.OnToolResult = func(_, result string) { rec.results = append(rec.results, result) }
	if rec.approve != nil {
		a.Approve = rec.approve
	}
	return a
}

func TestAskPlainAnswer(t *testing.T) {
	f := &fakeOllama{script: []reply{{text: "Salt wind on the waves."}}}
	rec := &recorder{}
	a := newAgent(t, f, rec)

	if err := a.Ask(context.Background(), "write a haiku"); err != nil {
		t.Fatal(err)
	}
	if rec.tokens.String() != "Salt wind on the waves." {
		t.Errorf("tokens = %q", rec.tokens.String())
	}
	if len(rec.tools) != 0 {
		t.Errorf("ran tools for a text answer: %v", rec.tools)
	}
	// system, user, assistant
	if len(a.Messages) != 3 || a.Messages[2].Role != "assistant" {
		t.Fatalf("history = %+v", a.Messages)
	}
	if a.Usage.EvalTokens != 20 || a.Usage.TokPerSec != 20 {
		t.Errorf("usage = %+v", a.Usage)
	}
	if a.Usage.CtxTokens != 120 {
		t.Errorf("ctx tokens = %d, want 120", a.Usage.CtxTokens)
	}
}

func TestAskRunsToolThenAnswers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeOllama{script: []reply{
		{calls: []ollama.ToolCall{call("read_file", map[string]any{"path": p})}},
		{text: "The file says hello."},
	}}
	rec := &recorder{}
	a := newAgent(t, f, rec)

	if err := a.Ask(context.Background(), "look at a.txt"); err != nil {
		t.Fatal(err)
	}
	if len(rec.tools) != 1 || rec.tools[0] != "read_file" {
		t.Fatalf("tools = %v", rec.tools)
	}
	if !strings.Contains(rec.results[0], "hello") {
		t.Errorf("tool result = %q", rec.results[0])
	}
	// system, user, assistant(tool_calls), tool, assistant
	if len(a.Messages) != 5 {
		t.Fatalf("history length = %d: %+v", len(a.Messages), a.Messages)
	}
	if a.Messages[3].Role != "tool" || a.Messages[3].ToolName != "read_file" {
		t.Errorf("tool message = %+v", a.Messages[3])
	}
}

// A tool call emitted as JSON text instead of through the structured channel
// must still run.
func TestAskRunsTextFormToolCall(t *testing.T) {
	dir := t.TempDir()
	f := &fakeOllama{script: []reply{
		{text: `Let me look. {"name": "list_dir", "arguments": {"path": "` + dir + `"}}`},
		{text: "It is empty."},
	}}
	rec := &recorder{}
	a := newAgent(t, f, rec)

	if err := a.Ask(context.Background(), "what's in there"); err != nil {
		t.Fatal(err)
	}
	if len(rec.tools) != 1 || rec.tools[0] != "list_dir" {
		t.Fatalf("tools = %v", rec.tools)
	}
}

// After a decline the next round must go out with no tools at all, so a small
// model cannot simply re-issue the rejected call.
func TestDeclineSuppressesToolsNextRound(t *testing.T) {
	f := &fakeOllama{script: []reply{
		{calls: []ollama.ToolCall{call("write_file", map[string]any{"path": "x", "content": "y"})}},
		{text: "Understood, here it is in text instead."},
	}}
	rec := &recorder{approve: func(string, map[string]any) bool { return false }}
	a := newAgent(t, f, rec)

	if err := a.Ask(context.Background(), "write x"); err != nil {
		t.Fatal(err)
	}
	if len(rec.tools) != 0 {
		t.Errorf("a declined tool was run: %v", rec.tools)
	}
	if !strings.Contains(rec.results[0], "declined") {
		t.Errorf("decline result = %q", rec.results[0])
	}
	if f.lastRequestHadTools() {
		t.Error("tools were still offered on the round after a decline")
	}
	if _, err := os.Stat("x"); err == nil {
		os.Remove("x")
		t.Error("declined write_file created the file anyway")
	}
}

// A tool that needs no approval is never put to the user.
func TestReadOnlyToolSkipsApproval(t *testing.T) {
	dir := t.TempDir()
	asked := false
	f := &fakeOllama{script: []reply{
		{calls: []ollama.ToolCall{call("list_dir", map[string]any{"path": dir})}},
		{text: "done"},
	}}
	rec := &recorder{approve: func(string, map[string]any) bool { asked = true; return true }}
	a := newAgent(t, f, rec)
	if err := a.Ask(context.Background(), "list it"); err != nil {
		t.Fatal(err)
	}
	if asked {
		t.Error("list_dir asked for approval")
	}
}

func TestMaxToolRounds(t *testing.T) {
	dir := t.TempDir()
	script := make([]reply, MaxToolRounds+5)
	for i := range script {
		script[i] = reply{calls: []ollama.ToolCall{call("list_dir", map[string]any{"path": dir})}}
	}
	f := &fakeOllama{script: script}
	rec := &recorder{}
	a := newAgent(t, f, rec)

	if err := a.Ask(context.Background(), "loop forever"); err != nil {
		t.Fatal(err)
	}
	if len(rec.tools) != MaxToolRounds {
		t.Errorf("ran %d rounds, want %d", len(rec.tools), MaxToolRounds)
	}
	if !strings.Contains(rec.tokens.String(), "too many tool rounds") {
		t.Errorf("no stop notice: %q", rec.tokens.String())
	}
}

func TestChatModeSendsNoTools(t *testing.T) {
	f := &fakeOllama{script: []reply{{text: "just talking"}}}
	a := newAgent(t, f, &recorder{})
	a.ToolsEnabled = false
	if err := a.Ask(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if f.lastRequestHadTools() {
		t.Error("chat mode still offered tools")
	}
}

func TestCancelStopsTheTurn(t *testing.T) {
	f := &fakeOllama{script: []reply{{text: "never mind"}}}
	a := newAgent(t, f, &recorder{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Ask(ctx, "hi"); err == nil {
		t.Fatal("expected a cancellation error")
	}
}

func TestOllamaErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "model 'x' not found"})
	}))
	defer srv.Close()
	a := New(ollama.New(srv.URL), "x", 8192, "")
	err := a.Ask(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "model 'x' not found") {
		t.Fatalf("err = %v", err)
	}
}

// Compaction must rewind to a user message so a tool result is never orphaned
// from the assistant tool_calls that produced it.
func TestCompactKeepsCoherentTail(t *testing.T) {
	f := &fakeOllama{script: []reply{{text: "- did some things"}}}
	a := newAgent(t, f, &recorder{})
	for i := 0; i < 4; i++ {
		a.Messages = append(a.Messages,
			ollama.Message{Role: "user", Content: "q"},
			ollama.Message{Role: "assistant", Content: "", ToolCalls: []ollama.ToolCall{call("list_dir", nil)}},
			ollama.Message{Role: "tool", ToolName: "list_dir", Content: "files"},
			ollama.Message{Role: "assistant", Content: "a"},
		)
	}
	msg, err := a.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "compacted") {
		t.Errorf("status = %q", msg)
	}
	if a.Messages[0].Role != "system" {
		t.Fatal("system prompt lost")
	}
	if !strings.Contains(a.Messages[1].Content, "did some things") {
		t.Errorf("summary = %q", a.Messages[1].Content)
	}
	if a.Messages[2].Role != "user" {
		t.Errorf("tail starts at %q, not a user message", a.Messages[2].Role)
	}
	// every tool message must follow an assistant message carrying tool_calls
	for i, m := range a.Messages {
		if m.Role == "tool" && (i == 0 || len(a.Messages[i-1].ToolCalls) == 0) {
			t.Errorf("orphaned tool result at index %d", i)
		}
	}
	if a.Usage.CtxTokens != 0 {
		t.Error("context estimate should be re-measured after compaction")
	}
}

func TestCompactNoopWhenShort(t *testing.T) {
	f := &fakeOllama{}
	a := newAgent(t, f, &recorder{})
	a.Messages = append(a.Messages, ollama.Message{Role: "user", Content: "hi"})
	msg, err := a.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "nothing to compact") {
		t.Errorf("status = %q", msg)
	}
}

func TestNeedsCompaction(t *testing.T) {
	f := &fakeOllama{}
	a := newAgent(t, f, &recorder{})
	a.NumCtx = 1000
	a.Usage.CtxTokens = 799
	if a.NeedsCompaction() {
		t.Error("compacting below the threshold")
	}
	a.Usage.CtxTokens = 801
	if !a.NeedsCompaction() {
		t.Error("not compacting above the threshold")
	}
}

func TestSystemPromptMentionsPlatformShell(t *testing.T) {
	p := BuildSystemPrompt("")
	if !strings.Contains(p, tools.ShellTool) {
		t.Errorf("system prompt does not name the shell tool %q", tools.ShellTool)
	}
	withMemory := BuildSystemPrompt("Always use tabs.")
	if !strings.Contains(withMemory, "Always use tabs.") || !strings.Contains(withMemory, "LOCO.md") {
		t.Error("project memory not appended to the system prompt")
	}
}

func TestClearKeepsSystemPrompt(t *testing.T) {
	f := &fakeOllama{script: []reply{{text: "hi"}}}
	a := newAgent(t, f, &recorder{})
	_ = a.Ask(context.Background(), "hello")
	a.Clear()
	if len(a.Messages) != 1 || a.Messages[0].Role != "system" {
		t.Fatalf("after Clear: %+v", a.Messages)
	}
	if a.Usage.EvalTokens != 0 {
		t.Error("usage not reset")
	}
}
