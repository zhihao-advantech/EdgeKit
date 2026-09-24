package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestACPHelperProcess is a scripted ACP agent used by TestACPAgentBackend. It
// is only active when re-executed with AGENT_FAKE_ACP=1.
func TestACPHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_FAKE_ACP") != "1" {
		return
	}
	fakeACPAgent()
	os.Exit(0)
}

func fakeACPAgent() {
	if path := os.Getenv("AGENT_FAKE_ACP_COUNT_FILE"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.WriteString("start\n")
			_ = f.Close()
		}
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	emit := func(m map[string]any) { _ = enc.Encode(m) }

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
			if ip.ProtocolVersion == 0 || ip.ClientInfo.Version == "" {
				emit(map[string]any{"jsonrpc": "2.0", "id": m.ID,
					"error": map[string]any{"code": -32602, "message": "Invalid params"}})
				continue
			}
			emit(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}})
		case "session/new":
			emit(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{
					"sessionId": "sess-1",
					"models": map[string]any{
						"availableModels": []map[string]any{
							{"modelId": "custom:m1", "name": "m1"},
						},
						"currentModelId": "custom:m1",
					},
				}})
		case "session/set_model":
			emit(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case "session/list":
			emit(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"sessions": []map[string]any{
					{"sessionId": "sess-1", "cwd": "/tmp", "title": "first"},
					{"sessionId": "sess-old", "cwd": "/tmp"},
				},
			}})
		case "session/load":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			emit(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"sessionId": p.SessionID,
				"models": map[string]any{
					"availableModels": []map[string]any{{"modelId": "custom:m1", "name": "m1"}},
					"currentModelId":  "custom:m1",
				},
			}})
		case "session/prompt":
			emit(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "sess-1",
				"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    "t1", "title": "serial_read", "kind": "read",
					"rawInput": map[string]any{"port": "/dev/ttyUSB0"},
				},
			}})
			emit(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "sess-1",
				"update": map[string]any{
					"sessionUpdate": "tool_call_update",
					"toolCallId":    "t1", "status": "completed", "rawOutput": "output!",
				},
			}})
			// Ask for approval; report which option the client chose.
			emit(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "sess-1",
					"toolCall":  map[string]any{"toolCallId": "t2", "title": "serial_write"},
					"options": []map[string]any{
						{"optionId": "allow_once", "name": "Allow", "kind": "allow_once"},
						{"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
					},
				}})
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
			chosen := reply.Result.Outcome.OptionID
			if reply.Result.Outcome.Outcome != "selected" {
				chosen = "BAD"
			}
			emit(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "sess-1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "chose:" + chosen},
				},
			}})
			emit(map[string]any{"jsonrpc": "2.0", "id": m.ID,
				"result": map[string]any{"stopReason": "end_turn"}})
		}
	}
}

func TestACPAgentBackend(t *testing.T) {
	t.Setenv("AGENT_FAKE_ACP", "1")

	c := newCollector()
	ag := newTestManager(Deps{}, testGate(true), c.on)
	ag.SetConfig(Config{
		Backend:    BackendACP,
		ACPCommand: os.Args[0],
		ACPArgs:    []string{"-test.run=TestACPHelperProcess", "--"},
	})
	ag.Send("检查串口")
	c.wait(t)
	ag.Close()

	var running, ok bool
	for _, e := range c.find(KindTool) {
		if e.Tool != "serial_read" {
			continue
		}
		if e.State == "running" {
			running = true
		}
		if e.State == "ok" && strings.Contains(e.Result, "output!") {
			ok = true
		}
	}
	if !running || !ok {
		t.Fatalf("tool events incomplete: running=%v ok=%v events=%+v", running, ok, c.find(KindTool))
	}

	var sawChose bool
	for _, e := range c.find(KindAssistant) {
		if strings.Contains(e.Text, "chose:allow_once") {
			sawChose = true
		}
	}
	if !sawChose {
		t.Fatalf("permission was not auto-allowed: %+v", c.find(KindAssistant))
	}
}

func TestACPAgentModelOverride(t *testing.T) {
	t.Setenv("AGENT_FAKE_ACP", "1")

	c := newCollector()
	ag := newTestManager(Deps{}, testGate(true), c.on)
	ag.SetConfig(Config{
		Backend:     BackendACP,
		ACPCommand:  os.Args[0],
		ACPArgs:     []string{"-test.run=TestACPHelperProcess", "--"},
		ACPOverride: true,
		BaseURL:     "http://example.invalid/v1",
		APIKey:      "k",
		Model:       "m2",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ag.Prepare(ctx); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer ag.Close()

	models := ag.Models()
	if len(models) == 0 || models[0].ID != "m2" {
		t.Fatalf("override model should head the list: %+v", models)
	}
	if got := ag.CurrentModel(); got != "m2" {
		t.Fatalf("current model = %q, want m2", got)
	}

	if err := ag.SetModel(ctx, "custom:m1"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if got := ag.CurrentModel(); got != "custom:m1" {
		t.Fatalf("current model after switch = %q", got)
	}
}

func TestACPAgentSessionReuse(t *testing.T) {
	t.Setenv("AGENT_FAKE_ACP", "1")
	countFile := filepath.Join(t.TempDir(), "starts.txt")
	t.Setenv("AGENT_FAKE_ACP_COUNT_FILE", countFile)

	c := newCollector()
	ag := newTestManager(Deps{}, testGate(true), c.on)
	ag.SetConfig(Config{
		Backend:    BackendACP,
		ACPCommand: os.Args[0],
		ACPArgs:    []string{"-test.run=TestACPHelperProcess", "--"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer ag.Close()

	if err := ag.Prepare(ctx); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if ag.CurrentSession() == "" {
		t.Fatal("no session after Prepare")
	}

	if err := ag.NewSession(ctx); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessions, err := ag.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[1].ID != "sess-old" {
		t.Fatalf("unexpected sessions: %+v", sessions)
	}
	if err := ag.LoadSession(ctx, "sess-old"); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got := ag.CurrentSession(); got != "sess-old" {
		t.Fatalf("current session = %q, want sess-old", got)
	}

	// Process residency: new/list/load must all reuse one agent process.
	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read count file: %v", err)
	}
	if starts := strings.Count(string(data), "start"); starts != 1 {
		t.Fatalf("agent process started %d times, want 1", starts)
	}
}

func TestEncodeModel(t *testing.T) {
	cases := []struct{ cmd, in, want string }{
		{"/usr/local/bin/hermes", "Qwen3", "custom:Qwen3"},
		{"hermes", "custom:Qwen3", "custom:Qwen3"},
		{"openclaw", "Qwen3", "Qwen3"},
		{"", "Qwen3", "Qwen3"},
	}
	for _, c := range cases {
		if got := encodeModel(c.cmd, c.in); got != c.want {
			t.Fatalf("encodeModel(%q,%q) = %q, want %q", c.cmd, c.in, got, c.want)
		}
	}
}
