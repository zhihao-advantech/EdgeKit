package kits

import (
	"context"
	"strings"
	"testing"
	"time"

	"edgekit/internal/kit"
	"edgekit/internal/timeline"
)

// tool returns a registry tool from the built-in kits.
func tool(t *testing.T, reg *kit.Registry, name string) kit.Tool {
	t.Helper()
	tl, ok := reg.Tool(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

func TestWaitForOutputMatchesRecentRecord(t *testing.T) {
	tl := timeline.New(100)
	tl.Append(timeline.Record{Channel: timeline.ChannelSerial, Kind: "rx", Data: []byte("U-Boot 2023.10")})
	reg := kit.NewRegistry()
	reg.Register(timelineKit{tl})

	w := tool(t, reg, "wait_for_output")
	out, err := w.Call(context.Background(), map[string]any{"pattern": "U-Boot", "channel": "serial"})
	if err != nil {
		t.Fatalf("wait_for_output: %v", err)
	}
	if !strings.Contains(out, "U-Boot 2023.10") || !strings.Contains(out, "seq=") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestWaitForOutputWaitsForFutureRecord(t *testing.T) {
	tl := timeline.New(100)
	reg := kit.NewRegistry()
	reg.Register(timelineKit{tl})
	w := tool(t, reg, "wait_for_output")

	go func() {
		time.Sleep(30 * time.Millisecond)
		tl.Append(timeline.Record{Channel: timeline.ChannelSSH, Kind: "stdout", Data: []byte("login:")})
	}()
	out, err := w.Call(context.Background(), map[string]any{"pattern": "^login:", "timeout_ms": 2000})
	if err != nil {
		t.Fatalf("wait_for_output: %v", err)
	}
	if !strings.Contains(out, "login:") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestWaitForOutputTimeoutTailsRecords(t *testing.T) {
	tl := timeline.New(100)
	tl.Append(timeline.Record{Channel: timeline.ChannelSerial, Kind: "rx", Data: []byte("kernel panic - not syncing")})
	reg := kit.NewRegistry()
	reg.Register(timelineKit{tl})
	w := tool(t, reg, "wait_for_output")

	out, err := w.Call(context.Background(), map[string]any{"pattern": "no such line", "timeout_ms": 20})
	if err != nil {
		t.Fatalf("timeout should report, not fail: %v", err)
	}
	if !strings.Contains(out, "等待超时") || !strings.Contains(out, "kernel panic") {
		t.Fatalf("timeout report should tail recent records: %q", out)
	}
}

func TestWaitForOutputValidatesArguments(t *testing.T) {
	tl := timeline.New(10)
	reg := kit.NewRegistry()
	reg.Register(timelineKit{tl})
	w := tool(t, reg, "wait_for_output")
	ctx := context.Background()

	if _, err := w.Call(ctx, nil); err == nil {
		t.Fatal("missing pattern should fail")
	}
	if _, err := w.Call(ctx, map[string]any{"pattern": "("}); err == nil {
		t.Fatal("invalid regex should fail")
	}
	if _, err := w.Call(ctx, map[string]any{"pattern": "x", "channel": "usb"}); err == nil {
		t.Fatal("unknown channel should fail")
	}
}

func TestWaitForOutputWithoutDeviceSession(t *testing.T) {
	reg := kit.NewRegistry()
	reg.Register(timelineKit{nil}) // no focused device
	w := tool(t, reg, "wait_for_output")
	if _, err := w.Call(context.Background(), map[string]any{"pattern": "x"}); err == nil {
		t.Fatal("no device session should fail")
	}
}
