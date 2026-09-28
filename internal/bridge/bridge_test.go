package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeApp is a minimal stand-in for a running EdgeKit: it answers tools.list
// and every tool.call with a matching result. The advertised tool names can be
// changed between calls to model kits activating as devices connect.
func fakeApp(t *testing.T) (*httptest.Server, func([]string)) {
	t.Helper()
	var (
		mu    sync.Mutex
		names = []string{"echo"}
	)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			switch m.Type {
			case "tools.list":
				mu.Lock()
				list := append([]string(nil), names...)
				mu.Unlock()
				tools := make([]any, 0, len(list))
				for _, n := range list {
					tools = append(tools, map[string]any{
						"name": n, "description": "d",
						"schema": map[string]any{"type": "object", "properties": map[string]any{}},
					})
				}
				writeJSON(conn, map[string]any{"type": "tools.defs", "payload": map[string]any{"tools": tools}})
			case "tool.call":
				var p struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(m.Payload, &p)
				// Widen the race window for concurrent writers.
				time.Sleep(2 * time.Millisecond)
				writeJSON(conn, map[string]any{"type": "tool.result", "payload": map[string]any{
					"id": p.ID, "name": "echo", "ok": true, "output": "ok-" + p.ID,
				}})
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func(ns []string) {
		mu.Lock()
		names = ns
		mu.Unlock()
	}
}

func writeJSON(conn *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	_ = conn.WriteMessage(websocket.TextMessage, data)
}

func dialBridge(t *testing.T, srv *httptest.Server) *Bridge {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	b := New(conn)
	go b.Run()
	return b
}

// TestConcurrentCalls guards against the concurrent-write panic: mcp.Serve
// handles requests in parallel, so the bridge must serialize its writes.
func TestConcurrentCalls(t *testing.T) {
	srv, _ := fakeApp(t)
	b := dialBridge(t, srv)

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := b.Call(context.Background(), "echo", map[string]any{"i": 1})
			if err != nil {
				t.Errorf("Call: %v", err)
				return
			}
			if !strings.HasPrefix(out, "ok-c") {
				t.Errorf("unexpected output %q", out)
			}
		}()
	}
	wg.Wait()
}

// TestConcurrentToolsAndCalls mixes the tool listing with parallel calls.
func TestConcurrentToolsAndCalls(t *testing.T) {
	srv, _ := fakeApp(t)
	b := dialBridge(t, srv)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := b.Tools(context.Background()); err != nil {
				t.Errorf("Tools: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := b.Call(context.Background(), "echo", nil); err != nil {
				t.Errorf("Call: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestToolsFollowTheDynamicSurface checks the list is not cached: kits activate
// as devices connect, so each tools/list must reflect the current surface.
func TestToolsFollowTheDynamicSurface(t *testing.T) {
	srv, setTools := fakeApp(t)
	b := dialBridge(t, srv)

	tools, err := b.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("first list = %+v", tools)
	}

	setTools([]string{"echo", "serial_read"})
	tools, err = b.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	found := false
	for _, td := range tools {
		if td.Name == "serial_read" {
			found = true
		}
	}
	if len(tools) != 2 || !found {
		t.Fatalf("second list should reflect the new surface, got %+v", tools)
	}
}
