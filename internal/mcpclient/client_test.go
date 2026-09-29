package mcpclient

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"edgekit/internal/mcp"
)

// fakeBackend is an in-process MCP server used to drive the client.
type fakeBackend struct{}

func (fakeBackend) Tools(context.Context) ([]mcp.Tool, error) {
	return []mcp.Tool{
		{Name: "echo", Description: "重复输入", InputSchema: map[string]any{"type": "object"}},
		{Name: "boom", Description: "总是失败", InputSchema: map[string]any{"type": "object"}},
	}, nil
}

func (fakeBackend) Call(_ context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "echo":
		return fmt.Sprint(args["text"]), nil
	case "boom":
		return "", fmt.Errorf("nope")
	default:
		return "", fmt.Errorf("unknown tool %s", name)
	}
}

func startFakeServer(t *testing.T) (*Client, func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mcp.Serve(ctx, c2, c2, fakeBackend{}, "fake", "1") }()

	cli := New(Options{ID: "kb", Name: "KB"})
	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := cli.StartWith(startCtx, c1, c1); err != nil {
		cancel()
		t.Fatalf("start client: %v", err)
	}
	cleanup := func() {
		_ = cli.Close()
		cancel()
		_ = c2.Close()
	}
	return cli, cleanup
}

func TestClientHandshakeAndCall(t *testing.T) {
	cli, cleanup := startFakeServer(t)
	defer cleanup()

	tools := cli.Tools()
	if len(tools) != 2 || tools[0].Name != "boom" || tools[1].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	if !cli.Alive() {
		t.Fatal("client should be alive")
	}
	if got := cli.ServerProtocol(); got != mcp.ProtocolVersion {
		t.Fatalf("negotiated protocol = %q, want %q", got, mcp.ProtocolVersion)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := cli.Call(ctx, "echo", map[string]any{"text": "hello"})
	if err != nil || out != "hello" {
		t.Fatalf("echo = %q, %v", out, err)
	}
	if _, err := cli.Call(ctx, "boom", nil); err == nil {
		t.Fatal("isError result should surface as an error")
	}
}
