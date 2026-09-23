package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

type fakeBackend struct{}

func (fakeBackend) Tools(ctx context.Context) ([]Tool, error) {
	return []Tool{{Name: "ping", Description: "does nothing", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (fakeBackend) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "ping":
		return "pong", nil
	case "boom":
		return "", errors.New("kaboom")
	}
	return "", errors.New("unknown tool " + name)
}

func serve(t *testing.T, in string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(in), &out, fakeBackend{}, "edgekit", "test"); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var msgs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}
	// Replies are written from concurrent goroutines; order them by request id.
	sort.Slice(msgs, func(i, j int) bool {
		a, _ := msgs[i]["id"].(float64)
		b, _ := msgs[j]["id"].(float64)
		return a < b
	})
	return msgs
}

func TestInitializeAndToolsList(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`
	msgs := serve(t, in)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 replies (notifications get none), got %d: %v", len(msgs), msgs)
	}
	init := msgs[0]["result"].(map[string]any)
	if init["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v", init["protocolVersion"])
	}
	if init["serverInfo"].(map[string]any)["name"] != "edgekit" {
		t.Fatalf("serverInfo = %v", init["serverInfo"])
	}
	tools := msgs[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "ping" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestToolsCallSuccessAndFailure(t *testing.T) {
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping","arguments":{}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"boom"}}
`
	msgs := serve(t, in)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(msgs))
	}
	ok := msgs[0]["result"].(map[string]any)
	if ok["isError"] != nil {
		t.Fatalf("unexpected error flag: %v", ok)
	}
	text := ok["content"].([]any)[0].(map[string]any)["text"]
	if text != "pong" {
		t.Fatalf("text = %v", text)
	}
	fail := msgs[1]["result"].(map[string]any)
	if fail["isError"] != true {
		t.Fatalf("failed call should set isError: %v", fail)
	}
}

func TestUnknownMethodReturnsRPCError(t *testing.T) {
	msgs := serve(t, `{"jsonrpc":"2.0","id":7,"method":"resources/list"}`)
	errObj, ok := msgs[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected rpc error, got %v", msgs[0])
	}
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("code = %v", errObj["code"])
	}
}
