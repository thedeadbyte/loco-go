// Package ollama is a thin client for the local Ollama HTTP API.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Error wraps every failure this package surfaces, so callers can tell an
// Ollama problem from a bug in loco.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(format string, a ...any) *Error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// Args are a tool call's arguments. Ollama sends an object, but some models
// send a JSON string instead — both decode into the same map here.
type Args map[string]any

func (a *Args) UnmarshalJSON(b []byte) error {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err == nil {
		*a = m
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		m = map[string]any{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			*a = Args{} // unparseable arguments become empty, never an error
			return nil
		}
		*a = m
		return nil
	}
	*a = Args{}
	return nil
}

// FunctionCall and ToolCall mirror Ollama's tool-call JSON shape.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments Args   `json:"arguments"`
}

type ToolCall struct {
	Function FunctionCall `json:"function"`
}

// Message is one entry of the conversation history. ToolName is set only on
// role="tool" messages; Ollama uses it to match a result to its call.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

// Chunk is one streamed object from /api/chat.
type Chunk struct {
	Message struct {
		Content   string     `json:"content"`
		ToolCalls []ToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	Error           string `json:"error"`
	PromptEvalCount *int   `json:"prompt_eval_count"`
	EvalCount       *int   `json:"eval_count"`
	EvalDuration    *int64 `json:"eval_duration"`
}

// PullEvent is one progress object from /api/pull.
type PullEvent struct {
	Status    string `json:"status"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// Client talks to one Ollama server.
type Client struct {
	Host    string
	Timeout time.Duration
	http    *http.Client
}

// New returns a client for host (default http://localhost:11434).
func New(host string) *Client {
	if host == "" {
		host = "http://localhost:11434"
	}
	return &Client{
		Host:    strings.TrimRight(host, "/"),
		Timeout: 600 * time.Second,
		// no client-level timeout: streamed generations are long-lived and the
		// per-request context is what actually bounds them
		http: &http.Client{},
	}
}

// IsUp reports whether the server answers at all.
func (c *Client) IsUp() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Host, nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// Models lists the locally pulled model tags.
func (c *Client) Models() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.Host+"/api/tags", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errf("cannot reach Ollama at %s: %v", c.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errf("ollama returned %s from /api/tags", resp.Status)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, errf("bad response from /api/tags: %v", err)
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	return out, nil
}

// HasModel reports whether name is pulled. A bare name means the :latest tag.
func (c *Client) HasModel(name string) (bool, error) {
	want := name
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	models, err := c.Models()
	if err != nil {
		return false, err
	}
	for _, m := range models {
		if m == want {
			return true, nil
		}
	}
	return false, nil
}

// Pull downloads a model, calling onEvent for each progress update.
func (c *Client) Pull(ctx context.Context, name string, onEvent func(PullEvent)) error {
	body, _ := json.Marshal(map[string]any{"model": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Host+"/api/pull",
		bytes.NewReader(body))
	if err != nil {
		return errf("%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return errf("cannot reach Ollama at %s: %v", c.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errf("%s", readError(resp))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var ev PullEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return errf("%v", err)
		}
		if ev.Error != "" {
			return &Error{Msg: ev.Error}
		}
		onEvent(ev)
	}
}

// Chat streams a completion, invoking onChunk for every object Ollama sends.
// Returning an error from onChunk aborts the stream and surfaces that error.
func (c *Client) Chat(ctx context.Context, model string, messages []Message,
	tools []map[string]any, numCtx int, onChunk func(Chunk) error) error {

	payload := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
		"options":  map[string]any{"num_ctx": numCtx},
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errf("could not encode request: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Host+"/api/chat",
		bytes.NewReader(body))
	if err != nil {
		return errf("%v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// a cancelled context is the user pressing Ctrl-C, not an Ollama fault
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errf("cannot reach Ollama at %s: %v", c.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &Error{Msg: readError(resp)}
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var chunk Chunk
		if err := dec.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errf("stream broke: %v", err)
		}
		if chunk.Error != "" {
			return &Error{Msg: chunk.Error}
		}
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
}

// readError pulls the most useful message out of a non-200 response.
func readError(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		return body.Error
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return resp.Status
}
