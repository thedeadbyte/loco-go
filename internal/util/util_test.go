package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandMentions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.py")
	if err := os.WriteFile(p, []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, loaded, missing := ExpandMentions("explain @" + p)
	if len(loaded) != 1 || loaded[0] != p {
		t.Fatalf("loaded = %v", loaded)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	if !strings.Contains(out, "print('hi')") || !strings.Contains(out, "--- @"+p+" ---") {
		t.Errorf("expanded text:\n%s", out)
	}

	// an email address is not a file mention
	if _, loaded, missing := ExpandMentions("mail me@example.com"); len(loaded)+len(missing) != 0 {
		t.Errorf("email treated as a mention: loaded=%v missing=%v", loaded, missing)
	}

	// trailing punctuation belongs to the sentence, not the path
	if _, loaded, _ := ExpandMentions("look at @" + p + ", please"); len(loaded) != 1 {
		t.Errorf("trailing comma broke the mention: %v", loaded)
	}

	// a missing path is reported so the user sees why nothing was added
	_, loaded, missing = ExpandMentions("read @/no/such/file")
	if len(loaded) != 0 || len(missing) != 1 {
		t.Errorf("loaded=%v missing=%v", loaded, missing)
	}

	// a directory is not a file
	if _, _, missing := ExpandMentions("look at @" + dir); len(missing) != 1 {
		t.Errorf("directory should be reported missing: %v", missing)
	}
}

func TestFindMemoryStopsAtRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "LOCO.md"), []byte("outer"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	deep := filepath.Join(repo, "src", "pkg")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	// no LOCO.md inside the repo: the search must not climb past the repo root
	if m := FindMemory(deep); m != nil {
		t.Fatalf("climbed past the repo root and found %s", m.Path)
	}

	inner := filepath.Join(repo, "LOCO.md")
	if err := os.WriteFile(inner, []byte("inner"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := FindMemory(deep)
	if m == nil || m.Text != "inner" {
		t.Fatalf("memory = %+v", m)
	}
}

func TestExpandUser(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := ExpandUser("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("ExpandUser(~/x) = %q", got)
	}
	if got := ExpandUser("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path rewritten: %q", got)
	}
}
