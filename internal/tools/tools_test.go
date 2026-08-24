package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "a.txt", "one\ntwo\nthree\nfour\n")

	got := Run("read_file", map[string]any{"path": p})
	for _, want := range []string{"    1\tone", "    4\tfour"} {
		if !strings.Contains(got, want) {
			t.Errorf("read_file output missing %q:\n%s", want, got)
		}
	}

	// a slice reports how much is left, so the model knows to ask for more
	got = Run("read_file", map[string]any{"path": p, "offset": 2, "limit": 2})
	if !strings.Contains(got, "    2\ttwo") || !strings.Contains(got, "    3\tthree") {
		t.Errorf("sliced read wrong:\n%s", got)
	}
	if strings.Contains(got, "four") {
		t.Errorf("sliced read leaked past the limit:\n%s", got)
	}
	if !strings.Contains(got, "1 more lines") {
		t.Errorf("sliced read did not report the remainder:\n%s", got)
	}

	if got := Run("read_file", map[string]any{"path": filepath.Join(dir, "nope")}); !strings.HasPrefix(got, "error:") {
		t.Errorf("missing file should error, got %q", got)
	}
	if got := Run("read_file", map[string]any{}); !strings.Contains(got, "missing required argument 'path'") {
		t.Errorf("missing arg message = %q", got)
	}
}

func TestEditFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "b.txt", "alpha\nbeta\ngamma\nbeta\n")

	// ambiguous edits are refused rather than guessed at
	got := Run("edit_file", map[string]any{"path": p, "old_string": "beta", "new_string": "BETA"})
	if !strings.Contains(got, "appears 2 times") {
		t.Fatalf("expected ambiguity error, got %q", got)
	}
	if body, _ := os.ReadFile(p); strings.Contains(string(body), "BETA") {
		t.Fatal("file was modified despite the error")
	}

	got = Run("edit_file", map[string]any{"path": p, "old_string": "beta",
		"new_string": "BETA", "replace_all": true})
	if !strings.Contains(got, "replaced 2") {
		t.Fatalf("replace_all result = %q", got)
	}
	body, _ := os.ReadFile(p)
	if strings.Count(string(body), "BETA") != 2 {
		t.Fatalf("file = %q", body)
	}

	if got := Run("edit_file", map[string]any{"path": p, "old_string": "zzz", "new_string": "x"}); !strings.Contains(got, "not found") {
		t.Errorf("missing old_string = %q", got)
	}
}

func TestWriteFileCreatesParents(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "deep", "c.txt")
	if got := Run("write_file", map[string]any{"path": p, "content": "hi"}); !strings.Contains(got, "wrote 2 chars") {
		t.Fatalf("write_file = %q", got)
	}
	if body, err := os.ReadFile(p); err != nil || string(body) != "hi" {
		t.Fatalf("file = %q, err = %v", body, err)
	}
}

func TestListDirSortsDirsFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "zeta.txt", "")
	write(t, dir, "Alpha.txt", "")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := Run("list_dir", map[string]any{"path": dir})
	want := "sub/\nAlpha.txt\nzeta.txt"
	if got != want {
		t.Errorf("list_dir = %q, want %q", got, want)
	}
}

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", "package main\nfunc hello() {}\n")
	write(t, dir, "b.txt", "hello there\n")
	write(t, dir, ".git/config", "hello inside a skipped dir\n")

	got := Run("grep", map[string]any{"pattern": "hello", "path": dir})
	if !strings.Contains(got, "a.go:2") || !strings.Contains(got, "b.txt:1") {
		t.Errorf("grep missed a hit:\n%s", got)
	}
	if strings.Contains(got, ".git") {
		t.Errorf("grep descended into a skipped dir:\n%s", got)
	}

	got = Run("grep", map[string]any{"pattern": "hello", "path": dir, "glob": "*.go"})
	if strings.Contains(got, "b.txt") {
		t.Errorf("glob filter ignored:\n%s", got)
	}
	if got := Run("grep", map[string]any{"pattern": "(", "path": dir}); !strings.Contains(got, "bad regex") {
		t.Errorf("bad regex = %q", got)
	}
}

func TestRunUnknownTool(t *testing.T) {
	if got := Run("nope", nil); got != "error: unknown tool nope" {
		t.Errorf("got %q", got)
	}
}

// Small models often send numbers where the schema says string; coercing beats
// failing the call and burning a round trip.
func TestArgCoercion(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "n.txt", "l1\nl2\nl3\n")
	got := Run("read_file", map[string]any{"path": p, "offset": "2", "limit": float64(1)})
	if !strings.Contains(got, "    2\tl2") || strings.Contains(got, "l3\n") {
		t.Errorf("coerced args gave:\n%s", got)
	}
}

func TestPreviewDiff(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "d.txt", "one\ntwo\nthree\n")

	d := PreviewDiff("edit_file", map[string]any{"path": p, "old_string": "two",
		"new_string": "TWO"}, 60)
	if !strings.Contains(d, "-two") || !strings.Contains(d, "+TWO") {
		t.Errorf("edit diff:\n%s", d)
	}
	if strings.HasSuffix(d, "\n") {
		t.Error("diff should not end with a newline")
	}

	// a write that changes nothing has nothing to approve
	if d := PreviewDiff("write_file", map[string]any{"path": p,
		"content": "one\ntwo\nthree\n"}, 60); d != "" {
		t.Errorf("no-op write produced a diff:\n%s", d)
	}
	if d := PreviewDiff("read_file", map[string]any{"path": p}, 60); d != "" {
		t.Errorf("read_file should have no diff, got %q", d)
	}

	long := strings.Repeat("x\n", 200)
	d = PreviewDiff("write_file", map[string]any{"path": p, "content": long}, 10)
	if lines := strings.Split(d, "\n"); len(lines) != 11 || !strings.Contains(lines[10], "more diff lines") {
		t.Errorf("diff not capped: %d lines\n%s", len(lines), d)
	}
}

func TestDescribeCall(t *testing.T) {
	if got := DescribeCall(ShellTool, map[string]any{"command": "ls -l"}); got != "ls -l" {
		t.Errorf("shell = %q", got)
	}
	if got := DescribeCall("write_file", map[string]any{"path": "a.txt", "content": "1234"}); got != "a.txt  (4 chars)" {
		t.Errorf("write_file = %q", got)
	}
	if got := DescribeCall("list_dir", map[string]any{"path": "."}); got != `{"path": "."}` {
		t.Errorf("fallback = %q", got)
	}
	// keys are sorted so the summary line does not reshuffle between calls
	got := DescribeCall("grep", map[string]any{"pattern": "x", "glob": "*.go", "path": "."})
	if got != `{"glob": "*.go", "path": ".", "pattern": "x"}` {
		t.Errorf("sorted args = %q", got)
	}
}

func TestShellTool(t *testing.T) {
	if IsWindows {
		t.Skip("posix shell only")
	}
	if got := Run(ShellTool, map[string]any{"command": "echo hi"}); got != "hi" {
		t.Errorf("stdout = %q", got)
	}
	got := Run(ShellTool, map[string]any{"command": "echo out; echo err >&2; exit 3"})
	for _, want := range []string{"out", "[stderr]", "err", "[exit code 3]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if got := Run(ShellTool, map[string]any{"command": "sleep 5", "timeout": 1}); !strings.Contains(got, "timed out") {
		t.Errorf("timeout = %q", got)
	}
	// a command that would block on stdin must abort, not hang the session
	if got := Run(ShellTool, map[string]any{"command": "cat", "timeout": 5}); strings.Contains(got, "timed out") {
		t.Errorf("stdin was not closed: %q", got)
	}
}

func TestSpecsCoverHandlers(t *testing.T) {
	specs := map[string]bool{}
	for _, s := range Described() {
		specs[s.Name] = true
	}
	for name := range Handlers {
		if !specs[name] {
			t.Errorf("tool %q has a handler but no schema", name)
		}
	}
	for name := range specs {
		if _, ok := Handlers[name]; !ok {
			t.Errorf("tool %q has a schema but no handler", name)
		}
	}
	for name := range NeedsApproval {
		if _, ok := Handlers[name]; !ok {
			t.Errorf("NeedsApproval names %q, which is not a tool", name)
		}
	}
}
