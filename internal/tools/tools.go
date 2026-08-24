// Package tools implements the tools the model can call, Claude Code style:
// files, search, shell.
//
// Adding a tool means touching Specs, the handler, and Handlers — plus
// NeedsApproval if it mutates anything. Tools never return an error: every
// failure comes back as an "error: ..." string that goes to the model as a
// tool result, so a bad call is something the model can recover from rather
// than something that crashes loco.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	"golang.org/x/net/html"

	"github.com/thedeadbyte/loco-go/internal/util"
)

// IsWindows drives both the shell tool's name and how commands are run.
var IsWindows = runtime.GOOS == "windows"

// ShellTool is the shell tool's name. It is named after what it actually runs
// so local models generate the right syntax for the platform — anything
// referring to the shell tool must go through this, never a literal "bash".
var ShellTool = shellToolName()

func shellToolName() string {
	if IsWindows {
		return "powershell"
	}
	return "bash"
}

const (
	maxReadBytes   = 200_000
	maxResultChars = 20_000
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true,
	"venv": true, "dist": true, "build": true, ".mypy_cache": true,
	".ruff_cache": true, ".pytest_cache": true,
}

// NeedsApproval lists the tools that ask the user before running.
var NeedsApproval = map[string]bool{
	ShellTool: true, "write_file": true, "edit_file": true,
}

// Specs is the tool schema sent to Ollama on every call.
var Specs = buildSpecs()

func fn(name, description string, props map[string]any, required ...string) map[string]any {
	params := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		params["required"] = required
	}
	return map[string]any{"type": "function", "function": map[string]any{
		"name": name, "description": description, "parameters": params,
	}}
}

func p(typ, description string) map[string]any {
	m := map[string]any{"type": typ}
	if description != "" {
		m["description"] = description
	}
	return m
}

func buildSpecs() []map[string]any {
	shellDesc := "Run a shell command and return stdout/stderr. Requires user approval."
	if IsWindows {
		shellDesc = "Run a PowerShell command and return output. Requires user approval."
	}
	return []map[string]any{
		fn("read_file",
			"Read a text file. Returns numbered lines. Use offset/limit for large files.",
			map[string]any{
				"path":   p("string", "File path (absolute or relative to cwd)"),
				"offset": p("integer", "1-based line to start from"),
				"limit":  p("integer", "Max lines to return (default 400)"),
			}, "path"),
		fn("write_file", "Create or overwrite a file with the given content.",
			map[string]any{
				"path":    p("string", ""),
				"content": p("string", ""),
			}, "path", "content"),
		fn("edit_file",
			"Replace an exact string in a file with a new string. old_string must appear exactly once unless replace_all is true.",
			map[string]any{
				"path":        p("string", ""),
				"old_string":  p("string", ""),
				"new_string":  p("string", ""),
				"replace_all": p("boolean", ""),
			}, "path", "old_string", "new_string"),
		fn("list_dir", "List files and directories at a path.",
			map[string]any{"path": p("string", "Directory path (default: cwd)")}),
		fn("grep", "Search file contents recursively with a regular expression.",
			map[string]any{
				"pattern": p("string", "Regular expression (RE2 syntax)"),
				"path":    p("string", "Directory to search (default: cwd)"),
				"glob":    p("string", "Filename filter like *.py"),
			}, "pattern"),
		fn("fetch_url",
			"Fetch a web page (e.g. API docs) and return its text content. "+
				"Use this for reading online documentation instead of shell commands.",
			map[string]any{"url": p("string", "http(s) URL to fetch")}, "url"),
		fn(ShellTool, shellDesc, map[string]any{
			"command": p("string", ""),
			"timeout": p("integer", "Seconds (default 120)"),
		}, "command"),
	}
}

// Spec is a lightweight view of a tool schema, for /tools.
type Spec struct{ Name, Description string }

// Described lists the tools in schema order for display.
func Described() []Spec {
	out := make([]Spec, 0, len(Specs))
	for _, s := range Specs {
		f, _ := s["function"].(map[string]any)
		name, _ := f["name"].(string)
		desc, _ := f["description"].(string)
		out = append(out, Spec{Name: name, Description: desc})
	}
	return out
}

func clip(text string, limit int) string {
	if len(text) > limit {
		return text[:limit] + fmt.Sprintf("\n... [truncated, %d more chars]", len(text)-limit)
	}
	return text
}

// ---------------------------------------------------------------- file tools

func readFile(path string, offset, limit int) string {
	p := util.ExpandUser(path)
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("error: %s is not a file", path)
	}
	if info.Size() > maxReadBytes*5 {
		return fmt.Sprintf("error: file too large (%d bytes); read a slice with offset/limit", info.Size())
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "error: " + err.Error()
	}
	lines := splitLines(string(raw))
	if offset < 1 {
		offset = 1
	}
	start := offset - 1
	if start > len(lines) {
		start = len(lines)
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	chunk := lines[start:end]
	var b strings.Builder
	for i, l := range chunk {
		fmt.Fprintf(&b, "%5d\t%s", offset+i, l)
		if i < len(chunk)-1 {
			b.WriteByte('\n')
		}
	}
	body := b.String()
	note := ""
	if start+limit < len(lines) {
		note = fmt.Sprintf("\n... [%d more lines]", len(lines)-(start+len(chunk)))
	}
	if body == "" {
		return "(empty file)"
	}
	return clip(body+note, maxResultChars)
}

func writeFile(path, content string) string {
	p := util.ExpandUser(path)
	if dir := filepath.Dir(p); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "error: " + err.Error()
		}
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("wrote %d chars to %s", len(content), p)
}

func editFile(path, oldString, newString string, replaceAll bool) string {
	p := util.ExpandUser(path)
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("error: %s is not a file", path)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "error: " + err.Error()
	}
	text := string(raw)
	n := strings.Count(text, oldString)
	if n == 0 {
		return "error: old_string not found in file"
	}
	if n > 1 && !replaceAll {
		return fmt.Sprintf("error: old_string appears %d times; make it unique or set replace_all", n)
	}
	count := 1
	if replaceAll {
		count = -1
	}
	if err := os.WriteFile(p, []byte(strings.Replace(text, oldString, newString, count)), info.Mode().Perm()); err != nil {
		return "error: " + err.Error()
	}
	if replaceAll {
		return fmt.Sprintf("replaced %d occurrence(s) in %s", n, p)
	}
	return fmt.Sprintf("replaced 1 occurrence(s) in %s", p)
}

func listDir(path string) string {
	p := util.ExpandUser(path)
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("error: %s is not a directory", path)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "error: " + err.Error()
	}
	// directories first, then files, each case-insensitively by name
	sort.SliceStable(entries, func(i, j int) bool {
		fi, fj := !entries[i].IsDir(), !entries[j].IsDir()
		if fi != fj {
			return !fi
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name()+"/")
		} else {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		return "(empty directory)"
	}
	return clip(strings.Join(out, "\n"), maxResultChars)
}

func grep(pattern, path, glob string) string {
	rx, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Sprintf("error: bad regex: %v", err)
	}
	root := util.ExpandUser(path)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return fmt.Sprintf("error: %s is not a directory", path)
	}
	var hits []string
	limitHit := false
	_ = filepath.WalkDir(root, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if skipDirs[d.Name()] && fp != root {
				return fs.SkipDir
			}
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, d.Name()); !ok {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxReadBytes {
			return nil
		}
		raw, err := os.ReadFile(fp)
		if err != nil {
			return nil
		}
		for i, line := range splitLines(string(raw)) {
			if !rx.MatchString(line) {
				continue
			}
			hits = append(hits, fmt.Sprintf("%s:%d: %s", fp, i+1, truncate(strings.TrimSpace(line), 200)))
			if len(hits) >= 200 {
				limitHit = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if limitHit {
		return clip(strings.Join(hits, "\n")+"\n... [match limit reached]", maxResultChars)
	}
	if len(hits) == 0 {
		return "no matches"
	}
	return clip(strings.Join(hits, "\n"), maxResultChars)
}

// ---------------------------------------------------------------- fetch_url

// htmlSkip are elements whose text is markup or invisible, never content.
var htmlSkip = map[string]bool{"script": true, "style": true, "noscript": true, "head": true}

// htmlToText is a crude HTML-to-text pass: drop script/style, keep visible text.
func htmlToText(r io.Reader) string {
	z := html.NewTokenizer(r)
	var parts []string
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(parts, "\n")
		case html.StartTagToken:
			if name, _ := z.TagName(); htmlSkip[string(name)] {
				skip++
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); htmlSkip[string(name)] && skip > 0 {
				skip--
			}
		case html.TextToken:
			if skip == 0 {
				if s := strings.TrimSpace(string(z.Text())); s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
}

func fetchURL(url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "error: url must start with http:// or https://"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "error: " + err.Error()
	}
	req.Header.Set("User-Agent", "loco/0.4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "error: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("error: %s for url: %s", resp.Status, url)
	}
	// cap the read: the result is clipped anyway, and a huge page would only
	// waste time and memory before being thrown away
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "error: " + err.Error()
	}
	if strings.Contains(resp.Header.Get("content-type"), "html") {
		return clip(htmlToText(strings.NewReader(string(body))), maxResultChars)
	}
	return clip(string(body), maxResultChars)
}

// ---------------------------------------------------------------- shell

func shell(command string, timeout int) string {
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 600 {
		timeout = 600
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if IsWindows {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
			"-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	// stdin from the null device so a command that tries to prompt interactively
	// (e.g. a PowerShell security confirmation) gets EOF and aborts instead of
	// hanging loco forever waiting on input it can never receive.
	devnull, err := os.Open(os.DevNull)
	if err == nil {
		cmd.Stdin = devnull
		defer devnull.Close()
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("error: command timed out after %ds", timeout)
	}
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			return "error: " + runErr.Error()
		}
	}

	out := stdout.String()
	if s := stderr.String(); s != "" {
		if out != "" {
			out += "\n"
		}
		out += "[stderr]\n" + s
	}
	out = strings.TrimSpace(out)
	if out == "" {
		out = "(no output)"
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		out += fmt.Sprintf("\n[exit code %d]", code)
	}
	return clip(out, maxResultChars)
}

// ---------------------------------------------------------------- dispatch

// Handlers maps a tool name to its implementation.
var Handlers = map[string]func(map[string]any) string{
	"read_file": func(a map[string]any) string {
		path, err := reqStr(a, "path")
		if err != nil {
			return badArgs("read_file", err)
		}
		return readFile(path, optInt(a, "offset", 1), optInt(a, "limit", 400))
	},
	"write_file": func(a map[string]any) string {
		path, err := reqStr(a, "path")
		if err != nil {
			return badArgs("write_file", err)
		}
		content, err := reqStr(a, "content")
		if err != nil {
			return badArgs("write_file", err)
		}
		return writeFile(path, content)
	},
	"edit_file": func(a map[string]any) string {
		path, err := reqStr(a, "path")
		if err != nil {
			return badArgs("edit_file", err)
		}
		oldS, err := reqStr(a, "old_string")
		if err != nil {
			return badArgs("edit_file", err)
		}
		newS, err := reqStr(a, "new_string")
		if err != nil {
			return badArgs("edit_file", err)
		}
		return editFile(path, oldS, newS, optBool(a, "replace_all", false))
	},
	"list_dir": func(a map[string]any) string {
		return listDir(optStr(a, "path", "."))
	},
	"grep": func(a map[string]any) string {
		pattern, err := reqStr(a, "pattern")
		if err != nil {
			return badArgs("grep", err)
		}
		return grep(pattern, optStr(a, "path", "."), optStr(a, "glob", ""))
	},
	"fetch_url": func(a map[string]any) string {
		url, err := reqStr(a, "url")
		if err != nil {
			return badArgs("fetch_url", err)
		}
		return fetchURL(url)
	},
	ShellTool: func(a map[string]any) string {
		command, err := reqStr(a, "command")
		if err != nil {
			return badArgs(ShellTool, err)
		}
		return shell(command, optInt(a, "timeout", 120))
	},
}

// Run executes a tool. It never panics out: a panic in a handler becomes an
// error string, because a crashed tool must not take the session with it.
func Run(name string, args map[string]any) (result string) {
	handler, ok := Handlers[name]
	if !ok {
		return "error: unknown tool " + name
	}
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("error: %v", r)
		}
	}()
	return handler(args)
}

// ---------------------------------------------------------------- approval UI

// PreviewDiff returns a unified diff of what write_file/edit_file would change,
// so the user approves with the exact change in front of them. Empty when the
// call isn't a file mutation or wouldn't change anything.
func PreviewDiff(name string, args map[string]any, maxLines int) string {
	var oldText, newText, label string
	switch name {
	case "write_file":
		p := util.ExpandUser(optStr(args, "path", ""))
		if raw, err := os.ReadFile(p); err == nil {
			oldText = string(raw)
		}
		newText = optStr(args, "content", "")
		label = p
	case "edit_file":
		p := util.ExpandUser(optStr(args, "path", ""))
		raw, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		oldText = string(raw)
		oldS, newS := optStr(args, "old_string", ""), optStr(args, "new_string", "")
		if !strings.Contains(oldText, oldS) {
			return ""
		}
		count := 1
		if optBool(args, "replace_all", false) {
			count = -1
		}
		newText = strings.Replace(oldText, oldS, newS, count)
		label = p
	default:
		return ""
	}
	if oldText == newText {
		return ""
	}
	out, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: withNewlines(splitLines(oldText)), FromFile: label,
		B: withNewlines(splitLines(newText)), ToFile: label,
		Eol: "\n", Context: 3,
	})
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if maxLines > 0 && len(lines) > maxLines {
		extra := len(lines) - maxLines
		lines = append(lines[:maxLines], fmt.Sprintf("... (%d more diff lines)", extra))
	}
	return strings.Join(lines, "\n")
}

// DescribeCall is a one-line human-readable summary of a tool call, used in the
// approval prompt and the ⏺ transcript marker.
func DescribeCall(name string, args map[string]any) string {
	switch name {
	case ShellTool:
		return optStr(args, "command", "")
	case "fetch_url":
		return optStr(args, "url", "")
	case "write_file":
		return fmt.Sprintf("%s  (%d chars)", optStr(args, "path", ""),
			len(optStr(args, "content", "")))
	case "edit_file":
		return optStr(args, "path", "")
	}
	return truncate(jsonArgs(args), 200)
}

// jsonArgs renders a call's arguments for display. Keys are sorted (Go
// randomizes map order, and a summary line that reshuffles itself looks broken)
// and spaced like Python's json.dumps, which the transcript format grew up on.
func jsonArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		key, _ := json.Marshal(k)
		val, err := json.Marshal(args[k])
		if err != nil {
			val = []byte(`"<unencodable>"`)
		}
		b.Write(key)
		b.WriteString(": ")
		b.Write(val)
	}
	b.WriteByte('}')
	return b.String()
}

// ---------------------------------------------------------------- arg helpers

func badArgs(name string, err error) string {
	return fmt.Sprintf("error: bad arguments for %s: %v", name, err)
}

// reqStr pulls a required string. Numbers and bools are accepted and
// stringified: small models routinely send 5 where "5" was asked for, and
// failing the call over that just wastes a round trip.
func reqStr(a map[string]any, key string) (string, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return "", fmt.Errorf("missing required argument '%s'", key)
	}
	s, ok := asString(v)
	if !ok {
		return "", fmt.Errorf("'%s' must be a string", key)
	}
	return s, nil
}

func optStr(a map[string]any, key, def string) string {
	if v, ok := a[key]; ok && v != nil {
		if s, ok := asString(v); ok && s != "" {
			return s
		}
	}
	return def
}

func optInt(a map[string]any, key string, def int) int {
	switch v := a[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func optBool(a map[string]any, key string, def bool) bool {
	switch v := a[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(v) {
		case "true", "yes", "1":
			return true
		case "false", "no", "0":
			return false
		}
	}
	return def
}

func asString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case float64:
		if s == float64(int64(s)) {
			return fmt.Sprintf("%d", int64(s)), true
		}
		return fmt.Sprintf("%v", s), true
	case bool:
		return fmt.Sprintf("%t", s), true
	}
	return "", false
}

// ---------------------------------------------------------------- text helpers

// splitLines splits on \n the way Python's str.splitlines does for the cases
// loco cares about: no trailing empty element for a file ending in a newline,
// and \r\n handled so Windows files don't show stray carriage returns.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func withNewlines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l + "\n"
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
