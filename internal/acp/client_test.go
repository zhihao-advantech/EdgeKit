package acp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHelperProcess is not a real test: when re-executed with ACP_FAKE_AGENT=1
// it acts as a scripted ACP agent on stdio.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ACP_FAKE_AGENT") != "1" {
		return
	}
	fakeAgent()
	os.Exit(0)
}

func writeMsg(enc *json.Encoder, v any) {
	_ = enc.Encode(v)
}

func notify(enc *json.Encoder, method string, params any) {
	writeMsg(enc, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// fakeAgent answers initialize / session/new / session/prompt and, during a
// prompt, emits two message chunks and one permission request.
func fakeAgent() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	var permID int64

	for {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := dec.Decode(&m); err != nil {
			return
		}
		switch m.Method {
		case "initialize":
			var ip struct {
				ProtocolVersion int `json:"protocolVersion"`
				ClientInfo      struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"clientInfo"`
			}
			_ = json.Unmarshal(m.Params, &ip)
			if ip.ProtocolVersion == 0 || ip.ClientInfo.Name == "" || ip.ClientInfo.Version == "" {
				writeMsg(enc, map[string]any{
					"jsonrpc": "2.0", "id": m.ID,
					"error": map[string]any{"code": -32602, "message": "Invalid params"},
				})
				continue
			}
			writeMsg(enc, map[string]any{
				"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}},
			})
		case "session/new":
			writeMsg(enc, map[string]any{
				"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{
					"sessionId": "sess-1",
					"models": map[string]any{
						"availableModels": []map[string]any{
							{"modelId": "custom:m1", "name": "m1"},
							{"modelId": "custom:m2", "name": "m2"},
						},
						"currentModelId": "custom:m1",
					},
				},
			})
		case "session/set_model":
			writeMsg(enc, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case "session/list":
			writeMsg(enc, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"sessions": []map[string]any{
					{"sessionId": "sess-1", "cwd": "/tmp", "title": "t1"},
					{"sessionId": "sess-2", "cwd": "/tmp"},
				},
			}})
		case "session/load":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			writeMsg(enc, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"sessionId": p.SessionID,
				"models": map[string]any{
					"availableModels": []map[string]any{{"modelId": "custom:m1", "name": "m1"}},
					"currentModelId":  "custom:m1",
				},
			}})
		case "session/prompt":
			promptID := m.ID
			notify(enc, "session/update", map[string]any{
				"sessionId": "sess-1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "hello "},
				},
			})
			permID++
			writeMsg(enc, map[string]any{
				"jsonrpc": "2.0", "id": permID, "method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "sess-1",
					"toolCall":  map[string]any{"toolCallId": "t1", "title": "run shell"},
					"options": []map[string]any{
						{"optionId": "allow_once", "name": "Allow", "kind": "allow_once"},
						{"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
					},
				},
			})
			// Wait for the client's permission response and verify the ACP
			// nesting: {"outcome":{"outcome":"selected","optionId":...}}.
			var reply struct {
				Result struct {
					Outcome struct {
						Outcome  string `json:"outcome"`
						OptionID string `json:"optionId"`
					} `json:"outcome"`
				} `json:"result"`
			}
			if err := dec.Decode(&reply); err != nil {
				return
			}
			if reply.Result.Outcome.Outcome != "selected" || reply.Result.Outcome.OptionID != "allow_once" {
				notify(enc, "session/update", map[string]any{
					"sessionId": "sess-1",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "BAD_PERMISSION "},
					},
				})
			}
			notify(enc, "session/update", map[string]any{
				"sessionId": "sess-1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "world"},
				},
			})
			writeMsg(enc, map[string]any{
				"jsonrpc": "2.0", "id": promptID,
				"result": map[string]any{"stopReason": "end_turn"},
			})
		}
	}
}

type recorder struct {
	mu    sync.Mutex
	text  string
	tools []Update
	perm  bool
}

func (r *recorder) onUpdate(u Update) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text += u.Text()
	if u.SessionUpdate == UpdateToolCall {
		r.tools = append(r.tools, u)
	}
}

func (r *recorder) onPermission(_ context.Context, _ PermissionRequest) PermissionOutcome {
	r.mu.Lock()
	r.perm = true
	r.mu.Unlock()
	return Selected("allow_once")
}

func (r *recorder) snapshot() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text, r.perm
}

func newTestClient(r *recorder) *Client {
	return New(Options{
		Command:      os.Args[0],
		Args:         []string{"-test.run=TestHelperProcess", "--"},
		Env:          []string{"ACP_FAKE_AGENT=1"},
		OnUpdate:     r.onUpdate,
		OnPermission: r.onPermission,
	})
}

func TestClientHandshakePromptAndPermission(t *testing.T) {
	r := &recorder{}
	c := newTestClient(r)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()
	if !c.Alive() {
		t.Fatal("client should be alive")
	}

	sess, err := c.NewSession(ctx, "/tmp", nil)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.ID != "sess-1" {
		t.Fatalf("session id = %q", sess.ID)
	}
	if len(sess.Models) != 2 || sess.CurrentModelID != "custom:m1" {
		t.Fatalf("models not parsed: %+v", sess)
	}
	sid := sess.ID

	if err := c.SetModel(ctx, sid, "custom:m2"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	stop, err := c.Prompt(ctx, sid, "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stopReason = %q", stop)
	}

	text, perm := r.snapshot()
	if text != "hello world" {
		t.Fatalf("agent text = %q, want %q", text, "hello world")
	}
	if !perm {
		t.Fatal("permission request was not delivered")
	}
}

func TestClientCloseIsIdempotent(t *testing.T) {
	r := &recorder{}
	c := newTestClient(r)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.Alive() {
		t.Fatal("client should be stopped")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestUpdateTextParsing(t *testing.T) {
	u := Update{SessionUpdate: UpdateAgentMessage, Content: json.RawMessage(`{"type":"text","text":"abc"}`)}
	if got := u.Text(); got != "abc" {
		t.Fatalf("Text() = %q", got)
	}
	other := Update{SessionUpdate: UpdateToolCall}
	if got := other.Text(); got != "" {
		t.Fatalf("tool call Text() = %q, want empty", got)
	}
}

// TestMCPServerRequiredFields guards the ACP schema requirement that stdio MCP
// servers always carry args and env (as [] rather than omitted).
func TestClientListAndLoadSessions(t *testing.T) {
	r := &recorder{}
	c := newTestClient(r)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	list, err := c.ListSessions(ctx, "/tmp")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 || list[0].SessionID != "sess-1" || list[0].Title != "t1" {
		t.Fatalf("unexpected sessions: %+v", list)
	}

	sess, err := c.LoadSession(ctx, "/tmp", "sess-2", nil)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if sess.ID != "sess-2" {
		t.Fatalf("loaded session id = %q", sess.ID)
	}
	if len(sess.Models) != 1 || sess.CurrentModelID != "custom:m1" {
		t.Fatalf("loaded models not parsed: %+v", sess)
	}
}

func TestMCPServerRequiredFields(t *testing.T) {
	b, err := json.Marshal(MCPServer{Name: "edgekit", Command: "/bin/edgekit"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"args":[]`) || !strings.Contains(got, `"env":[]`) {
		t.Fatalf("mcp server JSON missing required empty arrays: %s", got)
	}
}
