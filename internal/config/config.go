// Package config handles loco's profiles and configuration file.
//
// Config lives in ~/.config/loco/config.toml and looks like:
//
//	default_profile = "auto"   # "auto" = pick by hostname, else a profile name
//
//	[profiles.laptop]
//	hosts = ["my-laptop"]          # hostnames this profile auto-applies to
//	model = "qwen2.5-coder:7b"
//	num_ctx = 8192
//	ollama_host = "http://localhost:11434"
//
//	[profiles.workstation]
//	hosts = ["my-workstation"]
//	model = "qwen2.5-coder:14b"
//	num_ctx = 16384
//
// Resolution order for the active profile:
//  1. --profile NAME on the command line
//  2. a profile whose `hosts` list contains this machine's hostname
//  3. the config's `default_profile` (if it names a real profile)
//  4. built-in defaults
//
// The file is read and written as a generic map, not a struct, so keys loco
// doesn't know about survive a `loco profile save`.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/BurntSushi/toml"
)

// Built-in defaults, used when no config or profile applies.
const (
	DefaultModel  = "qwen2.5-coder:7b"
	DefaultNumCtx = 8192
	DefaultHost   = "http://localhost:11434"
)

// Dir returns loco's config directory, honoring LOCO_CONFIG_DIR.
func Dir() string {
	if d := os.Getenv("LOCO_CONFIG_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("APPDATA")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "loco")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "loco")
}

// Path is the config file, HistoryPath the REPL input history.
func Path() string        { return filepath.Join(Dir(), "config.toml") }
func HistoryPath() string { return filepath.Join(Dir(), "history") }

// Profile is one named set of model settings.
type Profile struct {
	Name       string
	Model      string
	NumCtx     int
	OllamaHost string
	Hosts      []string
}

// NewProfile returns the built-in default profile.
func NewProfile() Profile {
	return Profile{Name: "default", Model: DefaultModel, NumCtx: DefaultNumCtx,
		OllamaHost: DefaultHost}
}

func (p Profile) toTable() map[string]any {
	hosts := make([]any, len(p.Hosts))
	for i, h := range p.Hosts {
		hosts[i] = h
	}
	return map[string]any{
		"model": p.Model, "num_ctx": int64(p.NumCtx),
		"ollama_host": p.OllamaHost, "hosts": hosts,
	}
}

// Hostname is this machine's name, used for per-machine profile binding.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "localhost"
	}
	return h
}

// Load reads config.toml. A corrupt or hand-edited file shouldn't crash loco —
// it warns on stderr and returns an empty config so the app still starts.
func Load() map[string]any {
	cfg := map[string]any{}
	p := Path()
	if _, err := os.Stat(p); err != nil {
		return cfg
	}
	if _, err := toml.DecodeFile(p, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "loco: warning — could not read %s (%v); using defaults\n", p, err)
		return map[string]any{}
	}
	return cfg
}

// Save writes config.toml, creating the directory if needed.
func Save(cfg map[string]any) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.Create(Path())
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

// --------------------------------------------------------------- table access

func table(cfg map[string]any, key string) map[string]any {
	if t, ok := cfg[key].(map[string]any); ok {
		return t
	}
	return nil
}

func str(t map[string]any, key, def string) string {
	if v, ok := t[key].(string); ok && v != "" {
		return v
	}
	return def
}

func num(t map[string]any, key string, def int) int {
	switch v := t[key].(type) {
	case int64:
		return int(v)
	case int:
		return v
	case float64:
		return int(v)
	}
	return def
}

func strs(t map[string]any, key string) []string {
	raw, ok := t[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func profileFromTable(name string, t map[string]any) Profile {
	return Profile{
		Name:       name,
		Model:      str(t, "model", DefaultModel),
		NumCtx:     num(t, "num_ctx", DefaultNumCtx),
		OllamaHost: str(t, "ollama_host", DefaultHost),
		Hosts:      strs(t, "hosts"),
	}
}

// List returns every configured profile, sorted by name so output is stable.
func List(cfg map[string]any) []Profile {
	if cfg == nil {
		cfg = Load()
	}
	tables := table(cfg, "profiles")
	names := make([]string, 0, len(tables))
	for n := range tables {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Profile, 0, len(names))
	for _, n := range names {
		t, _ := tables[n].(map[string]any)
		out = append(out, profileFromTable(n, t))
	}
	return out
}

// ErrNoProfile reports an explicitly requested profile that doesn't exist.
type ErrNoProfile struct {
	Name  string
	Known []string
}

func (e *ErrNoProfile) Error() string {
	have := "none"
	if len(e.Known) > 0 {
		have = ""
		for i, n := range e.Known {
			if i > 0 {
				have += ", "
			}
			have += n
		}
	}
	return fmt.Sprintf("no profile named '%s' (have: %s)", e.Name, have)
}

// Resolve picks the active profile. See the package doc for precedence.
// An explicit name that doesn't exist is an *ErrNoProfile.
func Resolve(explicit string) (Profile, error) {
	cfg := Load()
	profiles := List(cfg)
	byName := map[string]Profile{}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		byName[p.Name] = p
		names = append(names, p.Name)
	}

	if explicit != "" {
		if p, ok := byName[explicit]; ok {
			return p, nil
		}
		return Profile{}, &ErrNoProfile{Name: explicit, Known: names}
	}

	host := Hostname()
	for _, p := range profiles {
		for _, h := range p.Hosts {
			if h == host {
				return p, nil
			}
		}
	}

	def := str(cfg, "default_profile", "auto")
	if def != "auto" {
		if p, ok := byName[def]; ok {
			return p, nil
		}
	}
	return NewProfile(), nil
}

// SaveOpts are the fields SaveProfile may change; nil means "leave alone".
type SaveOpts struct {
	Model      *string
	NumCtx     *int
	OllamaHost *string
	BindHost   bool
}

// SaveProfile creates or updates a named profile; only provided fields change.
func SaveProfile(name string, opts SaveOpts) (Profile, error) {
	cfg := Load()
	tables := table(cfg, "profiles")
	if tables == nil {
		tables = map[string]any{}
		cfg["profiles"] = tables
	}
	existing, _ := tables[name].(map[string]any)
	prof := profileFromTable(name, existing)

	if opts.Model != nil {
		prof.Model = *opts.Model
	}
	if opts.NumCtx != nil {
		prof.NumCtx = *opts.NumCtx
	}
	if opts.OllamaHost != nil {
		prof.OllamaHost = *opts.OllamaHost
	}
	if opts.BindHost {
		h := Hostname()
		// a hostname can only auto-map to one profile — unbind it elsewhere
		for otherName, raw := range tables {
			other, ok := raw.(map[string]any)
			if !ok || otherName == name {
				continue
			}
			kept := []any{}
			for _, v := range strs(other, "hosts") {
				if v != h {
					kept = append(kept, v)
				}
			}
			other["hosts"] = kept
		}
		if !contains(prof.Hosts, h) {
			prof.Hosts = append(prof.Hosts, h)
		}
	}
	tables[name] = prof.toTable()
	return prof, Save(cfg)
}

// DeleteProfile removes a profile, reporting whether it existed.
func DeleteProfile(name string) (bool, error) {
	cfg := Load()
	tables := table(cfg, "profiles")
	if _, ok := tables[name]; !ok {
		return false, nil
	}
	delete(tables, name)
	if str(cfg, "default_profile", "") == name {
		cfg["default_profile"] = "auto"
	}
	return true, Save(cfg)
}

// SetDefaultProfile sets the fallback used when no hostname matches
// ("auto" to unset). Reports false for an unknown name.
func SetDefaultProfile(name string) (bool, error) {
	cfg := Load()
	if name != "auto" {
		if _, ok := table(cfg, "profiles")[name]; !ok {
			return false, nil
		}
	}
	cfg["default_profile"] = name
	return true, Save(cfg)
}

// DefaultProfileName is the configured fallback, or "auto".
func DefaultProfileName(cfg map[string]any) string {
	return str(cfg, "default_profile", "auto")
}

// Theme / SetTheme persist the user's color scheme choice.
func Theme() string { return str(Load(), "theme", "") }

func SetTheme(name string) error {
	cfg := Load()
	cfg["theme"] = name
	return Save(cfg)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
