package toolcall

import (
	"reflect"
	"testing"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		want  []string // tool names, in order
		args0 map[string]any
	}{
		{"wrapped in function", `{"function": {"name": "list_dir", "arguments": {"path": "."}}}`,
			[]string{"list_dir"}, map[string]any{"path": "."}},
		{"bare form", `{"name": "read_file", "arguments": {"path": "a.txt"}}`,
			[]string{"read_file"}, map[string]any{"path": "a.txt"}},
		{"prose around it", "Let me look.\n{\"name\": \"list_dir\", \"arguments\": {}}\nDone.",
			[]string{"list_dir"}, map[string]any{}},
		{"arguments double-encoded as a string",
			`{"name": "read_file", "arguments": "{\"path\": \"b.txt\"}"}`,
			[]string{"read_file"}, map[string]any{"path": "b.txt"}},
		{"two calls", `{"name":"list_dir","arguments":{}} and {"name":"grep","arguments":{"pattern":"x"}}`,
			[]string{"list_dir", "grep"}, map[string]any{}},
		{"unknown tool is not run", `{"name": "rm_rf", "arguments": {"path": "/"}}`, nil, nil},
		{"plain json is not a call", `{"result": 42}`, nil, nil},
		{"incomplete json", `{"name": "read_file", "arguments": {"path":`, nil, nil},
		{"no json at all", "just prose about {braces}", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Extract(tc.text)
			var names []string
			for _, c := range got {
				names = append(names, c.Function.Name)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("names = %v, want %v", names, tc.want)
			}
			if tc.args0 != nil {
				if !reflect.DeepEqual(map[string]any(got[0].Function.Arguments), tc.args0) {
					t.Errorf("args = %v, want %v", got[0].Function.Arguments, tc.args0)
				}
			}
		})
	}
}

func TestStrip(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"prose is untouched", "hello world", "hello world"},
		{"known tool object dropped",
			"Let me look.\n{\"name\": \"list_dir\", \"arguments\": {\"path\": \".\"}}\nDone.",
			"Let me look.\n\nDone."},
		{"wrapped tool object dropped",
			`before {"function":{"name":"grep","arguments":{"pattern":"x"}}} after`,
			"before  after"},
		{"unknown but tool-shaped is dropped",
			`x {"name":"powershell","arguments":{"command":"ls"}} y`, "x  y"},
		{"ordinary json survives", `the result is {"count": 3} exactly`,
			`the result is {"count": 3} exactly`},
		{"partial tool call is hidden while streaming",
			`working on it {"name": "read_file", "argum`, "working on it"},
		{"partial non-tool json is kept", `total {"count`, `total {"count`},
		{"blank runs collapse", "a\n\n\n\n\nb", "a\n\nb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Strip(tc.in); got != tc.want {
				t.Errorf("Strip(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Extract and Strip must agree: anything Extract runs, Strip must hide.
func TestExtractAndStripAgree(t *testing.T) {
	texts := []string{
		`{"name":"list_dir","arguments":{}}`,
		`prose {"function":{"name":"read_file","arguments":{"path":"a"}}} more`,
		"multi {\"name\":\"grep\",\"arguments\":{\"pattern\":\"a\"}} and {\"name\":\"list_dir\",\"arguments\":{}}",
	}
	for _, text := range texts {
		if len(Extract(text)) == 0 {
			t.Fatalf("expected a call in %q", text)
		}
		if stripped := Strip(text); containsToolJSON(stripped) {
			t.Errorf("Strip left tool JSON behind: %q", stripped)
		}
	}
}

func containsToolJSON(s string) bool { return len(Extract(s)) > 0 }
