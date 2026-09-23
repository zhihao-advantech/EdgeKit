package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"edgekit/internal/mcp"
	"edgekit/internal/runtime"

	"github.com/gorilla/websocket"
)

// runMCP serves EdgeKit's tools to an MCP client (OpenClaw, Claude Code, ...)
// over stdio, forwarding every call to the running EdgeKit instance so the
// agent sees exactly the devices the user already has connected.
//
// stdout carries the protocol; all logging goes to stderr.
func runMCP() error {
	ep, err := runtime.Read()
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.Dial(ep.WS, nil)
	if err != nil {
		return fmt.Errorf("连接 EdgeKit 失败 (%s): %w", ep.WS, err)
	}
	defer conn.Close()

	b := newBridge(conn)
	go b.readLoop()
	return mcp.Serve(context.Background(), os.Stdin, os.Stdout, b, "edgekit", appVersion)
}

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

// bridge forwards MCP requests to the EdgeKit app over its WebSocket API.
type bridge struct {
	conn *websocket.Conn

	mu      sync.Mutex
	waiters []*waiter
	seq     int
	tools   []mcp.Tool
}

func newBridge(conn *websocket.Conn) *bridge {
	return &bridge{conn: conn}
}

func (b *bridge) readLoop() {
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

// exchange sends a request and waits for the matching reply type(s).
func (b *bridge) exchange(ctx context.Context, typ string, payload any, want []string, match func(json.RawMessage) bool, timeout time.Duration) (wsMessage, error) {
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
	if err := b.conn.WriteMessage(websocket.TextMessage, data); err != nil {
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

// Tools lists the tools the running EdgeKit exposes.
func (b *bridge) Tools(ctx context.Context) ([]mcp.Tool, error) {
	b.mu.Lock()
	if b.tools != nil {
		tools := b.tools
		b.mu.Unlock()
		return tools, nil
	}
	b.mu.Unlock()

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
	b.mu.Lock()
	b.tools = out
	b.mu.Unlock()
	return out, nil
}

// Call runs one tool on the running EdgeKit.
func (b *bridge) Call(ctx context.Context, name string, args map[string]any) (string, error) {
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
