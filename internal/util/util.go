// Package util holds small filesystem/git helpers used across loco.
package util

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MemoryNames are the project-memory filenames loco looks for, in priority order.
var MemoryNames = []string{"LOCO.md", "loco.md"}

// Only match @ at the start of a word (start-of-string or after whitespace) so
// an email like me@x.com is never treated as a file mention. Go's regexp has no
// lookbehind, so the preceding space is captured and re-emitted by the caller;
// here we only need the match positions, so a capture group is enough.
var mention = regexp.MustCompile(`(^|\s)@(\S+)`)

const (
	mentionTrail = "?!.,;:)]}'\""
	mentionMax   = 40_000
)

// ExpandMentions replaces @path tokens by appending the referenced files'
// contents to the message. Returns the augmented text plus the paths that were
// loaded and the ones that could not be — silence on a bad path looks like a
// broken feature, so the caller warns about missing ones.
func ExpandMentions(text string) (out string, loaded, missing []string) {
	var blocks []string
	for _, m := range mention.FindAllStringSubmatch(text, -1) {
		raw := strings.TrimRight(m[2], mentionTrail)
		if raw == "" {
			continue
		}
		p := ExpandUser(raw)
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			missing = append(missing, raw)
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			missing = append(missing, raw)
			continue
		}
		s := string(body)
		if len(s) > mentionMax {
			s = s[:mentionMax]
		}
		loaded = append(loaded, raw)
		blocks = append(blocks, "--- @"+raw+" ---\n"+s)
	}
	out = text
	if len(blocks) > 0 {
		out = text + "\n\n" + strings.Join(blocks, "\n\n")
	}
	return out, loaded, missing
}

// MemoryTemplate is the LOCO.md scaffold written by /init.
const MemoryTemplate = `# LOCO.md

Project instructions loco reads on startup. Keep it short and specific — it is
prepended to the model's system prompt every session.

## What this project is
<one or two sentences>

## Conventions
- <language / formatting / naming rules the model should follow>

## Commands
- test: <how to run tests>
- run: <how to run the app>

## Don't
- <things the model should avoid touching>
`

// Memory is a located LOCO.md and its contents.
type Memory struct {
	Path string
	Text string
}

// FindMemory looks for LOCO.md in start (default: cwd) and its ancestors,
// stopping at a git root. Returns nil when there is none.
func FindMemory(start string) *Memory {
	if start == "" {
		var err error
		if start, err = os.Getwd(); err != nil {
			return nil
		}
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil
	}
	for {
		for _, name := range MemoryNames {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				b, err := os.ReadFile(p)
				if err != nil {
					return nil
				}
				return &Memory{Path: p, Text: string(b)}
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return nil // don't climb past the repo root
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// GitBranch returns the current branch, or "" when cwd isn't a repo.
func GitBranch(cwd string) string {
	// --show-current works even on an unborn branch (fresh repo, no commits)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ExpandUser resolves a leading ~ the way Python's Path.expanduser does.
func ExpandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
