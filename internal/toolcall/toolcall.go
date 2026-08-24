// Package toolcall handles tool calls that models emit as plain JSON text
// instead of using Ollama's structured tool-call channel.
//
// Many local models do this. loco executes those calls anyway (Extract) and
// hides the JSON from the transcript (Strip). Both live here on purpose: they
// must agree exactly on what counts as a tool-call object, and in the Python
// version they were two separate scanners that had to be kept in sync by hand.
package toolcall

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/thedeadbyte/loco-go/internal/ollama"
	"github.com/thedeadbyte/loco-go/internal/tools"
)

// nextValue decodes the JSON value starting at s[i], returning the offset just
// past it. This is the equivalent of Python's JSONDecoder.raw_decode: it
// tolerates trailing content after the value.
func nextValue(s string, i int) (val any, end int, ok bool) {
	dec := json.NewDecoder(strings.NewReader(s[i:]))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, 0, false
	}
	return v, i + int(dec.InputOffset()), true
}

// unwrap returns the inner function object of a decoded tool call. Models emit
// either {"function": {"name": ..., "arguments": ...}} or the bare inner form.
func unwrap(obj any) (map[string]any, bool) {
	m, ok := obj.(map[string]any)
	if !ok {
		return nil, false
	}
	if inner, ok := m["function"].(map[string]any); ok {
		return inner, true
	}
	return m, true
}

// callFrom extracts a runnable tool call from a decoded object. It only accepts
// names loco actually implements, so arbitrary JSON in a reply is never run.
func callFrom(obj any) (ollama.ToolCall, bool) {
	fn, ok := unwrap(obj)
	if !ok {
		return ollama.ToolCall{}, false
	}
	name, _ := fn["name"].(string)
	if _, known := tools.Handlers[name]; !known {
		return ollama.ToolCall{}, false
	}
	args, ok := fn["arguments"].(map[string]any)
	if !ok {
		switch raw := fn["arguments"].(type) {
		case nil:
			args = map[string]any{}
		case string:
			// some models double-encode the arguments object as a string
			args = map[string]any{}
			if json.Unmarshal([]byte(raw), &args) != nil {
				return ollama.ToolCall{}, false
			}
		default:
			return ollama.ToolCall{}, false
		}
	}
	return ollama.ToolCall{Function: ollama.FunctionCall{Name: name, Arguments: args}}, true
}

// isToolObject reports whether a decoded object should be hidden from the
// transcript. Wider than callFrom: an unknown name that is unmistakably
// tool-call-shaped (just name + arguments) is hidden too, so the display looks
// right regardless of platform or tool naming.
func isToolObject(obj any) bool {
	fn, ok := unwrap(obj)
	if !ok {
		return false
	}
	name, ok := fn["name"].(string)
	if !ok {
		return false
	}
	if _, known := tools.Handlers[name]; known {
		return true
	}
	if _, hasArgs := fn["arguments"]; !hasArgs {
		return false
	}
	for k := range fn {
		if k != "name" && k != "arguments" {
			return false
		}
	}
	return true
}

// Extract fishes tool calls out of a model's plain-text reply.
func Extract(text string) []ollama.ToolCall {
	var calls []ollama.ToolCall
	for i := 0; i < len(text); {
		j := strings.IndexByte(text[i:], '{')
		if j < 0 {
			break
		}
		j += i
		obj, end, ok := nextValue(text, j)
		if !ok {
			i = j + 1
			continue
		}
		i = end
		if call, ok := callFrom(obj); ok {
			calls = append(calls, call)
		}
	}
	return calls
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

// Strip removes text-form tool-call JSON from streamed output for display.
//
// Prose is preserved, a complete tool-call object is dropped, and an incomplete
// one still streaming is hidden until it resolves — otherwise the user watches
// half a JSON blob type itself out before it disappears.
func Strip(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '{' {
			out.WriteByte(text[i])
			i++
			continue
		}
		obj, end, ok := nextValue(text, i)
		if !ok {
			tail := text[i:]
			// a partial object that looks like a tool call: hide the rest
			if strings.Contains(tail, `"name"`) &&
				strings.Count(tail, "{") > strings.Count(tail, "}") {
				break
			}
			out.WriteByte(text[i])
			i++
			continue
		}
		if !isToolObject(obj) {
			out.WriteString(text[i:end])
		}
		i = end
	}
	return strings.TrimSpace(blankRuns.ReplaceAllString(out.String(), "\n\n"))
}
