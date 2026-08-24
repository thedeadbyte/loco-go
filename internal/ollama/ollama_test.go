package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Ollama sends tool arguments as an object, but plenty of local models send a
// JSON string instead; both must land in the same map, and garbage must not
// fail the whole response.
func TestArgsUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{"object", `{"path": "a.txt"}`, map[string]any{"path": "a.txt"}},
		{"json string", `"{\"path\": \"a.txt\"}"`, map[string]any{"path": "a.txt"}},
		{"unparseable string", `"not json"`, map[string]any{}},
		{"wrong type entirely", `42`, map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a Args
			if err := json.Unmarshal([]byte(tc.raw), &a); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if len(a) != len(tc.want) {
				t.Fatalf("args = %v, want %v", a, tc.want)
			}
			for k, v := range tc.want {
				if a[k] != v {
					t.Errorf("args[%q] = %v, want %v", k, a[k], v)
				}
			}
		})
	}
}

func TestHasModelAssumesLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{
			{"name": "qwen2.5-coder:7b"}, {"name": "llama3.1:latest"},
		}})
	}))
	defer srv.Close()
	c := New(srv.URL)

	for name, want := range map[string]bool{
		"qwen2.5-coder:7b": true, "llama3.1": true, "llama3.1:latest": true,
		"qwen2.5-coder": false, "nope": false,
	} {
		got, err := c.HasModel(name)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("HasModel(%q) = %v, want %v", name, got, want)
		}
	}
}

// An error can arrive as an HTTP status or mid-stream; both must surface as an
// *Error rather than a truncated success.
func TestChatErrors(t *testing.T) {
	t.Run("http status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "model not found"})
		}))
		defer srv.Close()
		err := New(srv.URL).Chat(context.Background(), "m", nil, nil, 8192, func(Chunk) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "model not found") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("mid-stream", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enc := json.NewEncoder(w)
			_ = enc.Encode(map[string]any{"message": map[string]string{"content": "partial"}})
			_ = enc.Encode(map[string]any{"error": "out of memory"})
		}))
		defer srv.Close()
		var got strings.Builder
		err := New(srv.URL).Chat(context.Background(), "m", nil, nil, 8192, func(c Chunk) error {
			got.WriteString(c.Message.Content)
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "out of memory") {
			t.Fatalf("err = %v", err)
		}
		if got.String() != "partial" {
			t.Errorf("chunks before the error were dropped: %q", got.String())
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		err := New("http://127.0.0.1:1").Chat(context.Background(), "m", nil, nil, 8192,
			func(Chunk) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "cannot reach Ollama") {
			t.Fatalf("err = %v", err)
		}
	})
}

// Tools are only sent when there are some: an empty list must not appear in the
// payload, or a model may believe it has tools it does not.
func TestChatOmitsEmptyTools(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true})
	}))
	defer srv.Close()

	c := New(srv.URL)
	_ = c.Chat(context.Background(), "m", nil, nil, 4096, func(Chunk) error { return nil })
	if _, ok := payload["tools"]; ok {
		t.Error("nil tools were sent")
	}
	if opts, _ := payload["options"].(map[string]any); opts["num_ctx"] != float64(4096) {
		t.Errorf("num_ctx not passed through: %v", payload["options"])
	}

	_ = c.Chat(context.Background(), "m", nil, []map[string]any{{"type": "function"}}, 4096,
		func(Chunk) error { return nil })
	if _, ok := payload["tools"]; !ok {
		t.Error("tools were not sent")
	}
}
