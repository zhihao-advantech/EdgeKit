// Package mcp implements the part of the Model Context Protocol that EdgeKit
// needs to act as a tool server: JSON-RPC 2.0 over stdio, with initialize,
// tools/list and tools/call.
//
// It is deliberately small and dependency-free so it builds on the Go version
// EdgeKit targets; a future Go upgrade can swap it for the official SDK without
// touching the backend interface.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ProtocolVersion is the MCP revision this server answers with.
const ProtocolVersion = "2025-06-18"

// Tool is a tool advertised to the client.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// Backend supplies the tools and executes calls. It is implemented by the
// bridge that talks to the running EdgeKit instance.
type Backend interface {
	Tools(ctx context.Context) ([]Tool, error)
	Call(ctx context.Context, name string, args map[string]any) (string, error)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve speaks MCP on in/out until the stream ends.
func Serve(ctx context.Context, in io.Reader, out io.Writer, backend Backend, name, version string) error {
	dec := json.NewDecoder(in)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		enc = json.NewEncoder(out)
	)
	write := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(v)
	}
	reply := func(id json.RawMessage, result any) {
		write(response{JSONRPC: "2.0", ID: id, Result: result})
	}
	replyErr := func(id json.RawMessage, code int, msg string) {
		write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	}

	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				wg.Wait()
				return nil
			}
			return fmt.Errorf("decode: %w", err)
		}
		if len(req.ID) == 0 {
			// Notification (notifications/initialized, cancelled, ...). Nothing to do.
			continue
		}
		r := req
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch r.Method {
			case "initialize":
				reply(r.ID, map[string]any{
					"protocolVersion": ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": name, "version": version},
				})
			case "ping":
				reply(r.ID, map[string]any{})
			case "tools/list":
				tools, err := backend.Tools(ctx)
				if err != nil {
					replyErr(r.ID, -32603, err.Error())
					return
				}
				reply(r.ID, map[string]any{"tools": tools})
			case "tools/call":
				var p struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}
				if err := json.Unmarshal(r.Params, &p); err != nil {
					replyErr(r.ID, -32602, "invalid params: "+err.Error())
					return
				}
				text, err := backend.Call(ctx, p.Name, p.Arguments)
				if err != nil {
					reply(r.ID, map[string]any{
						"content": []any{map[string]any{"type": "text", "text": err.Error()}},
						"isError": true,
					})
					return
				}
				reply(r.ID, map[string]any{
					"content": []any{map[string]any{"type": "text", "text": text}},
				})
			default:
				replyErr(r.ID, -32601, "method not found: "+r.Method)
			}
		}()
	}
}
