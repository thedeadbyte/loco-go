package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCO_CONFIG_DIR", dir)
	return dir
}

func TestDefaultsWithNoConfig(t *testing.T) {
	sandbox(t)
	p, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "default" || p.Model != DefaultModel || p.NumCtx != DefaultNumCtx {
		t.Errorf("built-in default = %+v", p)
	}
}

func TestSaveAndResolve(t *testing.T) {
	sandbox(t)
	model, ctx := "qwen2.5-coder:14b", 16384
	if _, err := SaveProfile("work", SaveOpts{Model: &model, NumCtx: &ctx}); err != nil {
		t.Fatal(err)
	}
	p, err := Resolve("work")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != model || p.NumCtx != ctx {
		t.Fatalf("saved profile = %+v", p)
	}

	// only provided fields change
	newCtx := 4096
	if _, err := SaveProfile("work", SaveOpts{NumCtx: &newCtx}); err != nil {
		t.Fatal(err)
	}
	p, _ = Resolve("work")
	if p.Model != model || p.NumCtx != newCtx {
		t.Fatalf("partial update clobbered a field: %+v", p)
	}

	var noProfile *ErrNoProfile
	if _, err := Resolve("ghost"); err == nil {
		t.Fatal("expected an error for an unknown profile")
	} else if !asErrNoProfile(err, &noProfile) {
		t.Fatalf("error type = %T", err)
	}
}

func asErrNoProfile(err error, target **ErrNoProfile) bool {
	e, ok := err.(*ErrNoProfile)
	if ok {
		*target = e
	}
	return ok
}

// Precedence: an explicit name beats a hostname binding, which beats
// default_profile, which beats the built-in defaults.
func TestResolvePrecedence(t *testing.T) {
	sandbox(t)
	host := Hostname()
	bound, fallback := "bound", "fallback"
	if _, err := SaveProfile(bound, SaveOpts{Model: strp("bound-model"), BindHost: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveProfile(fallback, SaveOpts{Model: strp("fallback-model")}); err != nil {
		t.Fatal(err)
	}
	if ok, err := SetDefaultProfile(fallback); err != nil || !ok {
		t.Fatal(err)
	}

	if p, _ := Resolve(""); p.Name != bound {
		t.Errorf("hostname %q should select %q, got %q", host, bound, p.Name)
	}
	if p, _ := Resolve(fallback); p.Name != fallback {
		t.Errorf("explicit name ignored: %q", p.Name)
	}

	// a hostname can only auto-map to one profile
	if _, err := SaveProfile("other", SaveOpts{BindHost: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Resolve(""); p.Name != "other" {
		t.Errorf("rebinding the host failed: %q", p.Name)
	}
	for _, p := range List(nil) {
		if p.Name == bound && len(p.Hosts) != 0 {
			t.Errorf("old profile still bound to %v", p.Hosts)
		}
	}

	// deleting the default profile falls back to auto
	if ok, err := DeleteProfile("other"); err != nil || !ok {
		t.Fatal(err)
	}
	if p, _ := Resolve(""); p.Name != fallback {
		t.Errorf("after delete, expected %q, got %q", fallback, p.Name)
	}
}

func strp(s string) *string { return &s }

// A hand-edited config may hold keys loco knows nothing about; saving a profile
// must not throw them away.
func TestUnknownTopLevelKeysSurvive(t *testing.T) {
	dir := sandbox(t)
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("mystery = \"keep me\"\n\n[extras]\nfoo = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveProfile("p", SaveOpts{Model: strp("m")}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(cfgPath)
	for _, want := range []string{"mystery", "keep me", "[extras]"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("lost %q from config:\n%s", want, body)
		}
	}
}

// A corrupt config must not stop loco from starting.
func TestCorruptConfigFallsBack(t *testing.T) {
	dir := sandbox(t)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != DefaultModel {
		t.Errorf("expected built-in defaults, got %+v", p)
	}
}

func TestThemeRoundTrip(t *testing.T) {
	sandbox(t)
	if Theme() != "" {
		t.Error("expected no theme by default")
	}
	if err := SetTheme("matrix"); err != nil {
		t.Fatal(err)
	}
	if Theme() != "matrix" {
		t.Errorf("theme = %q", Theme())
	}
}
