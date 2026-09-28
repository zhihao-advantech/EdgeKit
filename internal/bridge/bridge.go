// Package bridge forwards MCP requests to a running EdgeKit instance over its
// local WebSocket API, so an MCP client (OpenClaw, Claude Code, ...) drives the
// devices the user already has connected.
//
// It is separate from internal/mcp (which speaks the protocol) so the
// forwarding logic can be tested without the GUI build.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"edgekit/internal/mcp"

	"github.com/gorilla/websocket"
)

// wsMessage is the envelope of the local WebSocket protocol.
type wsMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type waiter struct {
	typ   string
	match func(json.RawMessage) bool
	ch    chan wsMessage
}

// Bridge forwards tool calls to one running EdgeKit instance.
type Bridge struct {
	conn *websocket.Conn

	// writeMu serializes writes: mcp.Serve handles requests concurrently, and
	// gorilla panics on concurrent writes to one connection.
	writeMu sync.Mutex

	mu      sync.Mutex
	waiters []*waiter
	seq     int
}

// New creates a bridge over an established connection.
func New(conn *websocket.Conn) *Bridge {
	return &Bridge{conn: conn}
}

// Run reads replies until the connection closes; start it in its own goroutine.
func (b *Bridge) Run() {
	for {
		_, data, err := b.conn.ReadMessage()
		if err != nil {
			return
		}
		var m wsMessage
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		b.mu.Lock()
		var matched *waiter
		for i, w := range b.waiters {
			if w.typ == m.Type && (w.match == nil || w.match(m.Payload)) {
				matched = w
				b.waiters = append(b.waiters[:i], b.waiters[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		if matched != nil {
			select {
			case matched.ch <- m:
			default:
			}
		}
	}
}

// write sends one message, serialized against every other writer.
func (b *Bridge) write(data []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.conn.WriteMessage(websocket.TextMessage, data)
}

// exchange sends a request and waits for the matching reply type(s).
func (b *Bridge) exchange(ctx context.Context, typ string, payload any, want []string, match func(json.RawMessage) bool, timeout time.Duration) (wsMessage, error) {
	ch := make(chan wsMessage, 1)
	b.mu.Lock()
	ws := make([]*waiter, 0, len(want))
	for _, t := range want {
		w := &waiter{typ: t, match: match, ch: ch}
		b.waiters = append(b.waiters, w)
		ws = append(ws, w)
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		kept := b.waiters[:0]
		for _, w := range b.waiters {
			drop := false
			for _, mine := range ws {
				if w == mine {
					drop = true
				}
			}
			if !drop {
				kept = append(kept, w)
			}
		}
		b.waiters = kept
		b.mu.Unlock()
	}()

	msg := map[string]any{"type": typ}
	if payload != nil {
		msg["payload"] = payload
	}
	data, _ := json.Marshal(msg)
	if err := b.write(data); err != nil {
		return wsMessage{}, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case m := <-ch:
		if m.Type == "error" {
			var p struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(m.Payload, &p)
			return wsMessage{}, errors.New(p.Message)
		}
		return m, nil
	case <-timer.C:
		return wsMessage{}, fmt.Errorf("等待 %s 超时", want[0])
	case <-ctx.Done():
		return wsMessage{}, ctx.Err()
	}
}

// Tools lists the tools the running EdgeKit currently exposes. The surface is
// dynamic (kits activate as devices connect), so it is asked for every time
// rather than cached.
func (b *Bridge) Tools(ctx context.Context) ([]mcp.Tool, error) {
	msg, err := b.exchange(ctx, "tools.list", nil, []string{"tools.defs"}, nil, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var p struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Schema      map[string]any `json:"schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return nil, fmt.Errorf("解析工具列表失败: %w", err)
	}
	out := make([]mcp.Tool, 0, len(p.Tools))
	for _, t := range p.Tools {
		out = append(out, mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	return out, nil
}

// Call runs one tool on the running EdgeKit.
func (b *Bridge) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	b.mu.Lock()
	b.seq++
	id := fmt.Sprintf("c%d", b.seq)
	b.mu.Unlock()

	match := func(p json.RawMessage) bool {
		var m struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(p, &m) == nil && m.ID == id
	}
	msg, err := b.exchange(ctx, "tool.call",
		map[string]any{"id": id, "name": name, "args": args},
		[]string{"tool.result", "error"}, match, 10*time.Minute)
	if err != nil {
		return "", err
	}
	var r struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(msg.Payload, &r); err != nil {
		return "", fmt.Errorf("解析工具结果失败: %w", err)
	}
	if !r.OK {
		return "", errors.New(r.Error)
	}
	return r.Output, nil
}
