package kit

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestInvokeBoundsHungTool(t *testing.T) {
	// A tool that ignores its context: Invoke must still unblock the caller
	// at the deadline instead of waiting for the tool's natural end.
	slow := Tool{
		Name: "slow",
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			time.Sleep(300 * time.Millisecond)
			return "done", nil
		},
	}
	start := time.Now()
	if _, err := slow.Invoke(context.Background(), nil, 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "执行超时") {
		t.Fatalf("hung tool should time out, got %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 250*time.Millisecond {
		t.Fatalf("caller waited for the tool's natural end (%s)", elapsed)
	}
}

func TestInvokeKeepsPartialOutput(t *testing.T) {
	partial := Tool{
		Name: "partial",
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			<-ctx.Done() // returns "output" only after the deadline fired
			return "partial output", nil
		},
	}
	out, err := partial.Invoke(context.Background(), nil, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "执行超时") {
		t.Fatalf("deadline should be reported, got %v", err)
	}
	if !strings.Contains(out, "partial output") {
		t.Fatalf("partial output should be kept, got %q", out)
	}
}

func TestInvokeWithinDeadline(t *testing.T) {
	fast := Tool{
		Name: "fast",
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			return "ok", nil
		},
	}
	out, err := fast.Invoke(context.Background(), nil, time.Minute)
	if err != nil || out != "ok" {
		t.Fatalf("fast tool should run, got %q, %v", out, err)
	}
	// timeout <= 0 leaves the call bounded by ctx alone.
	out, err = fast.Invoke(context.Background(), nil, 0)
	if err != nil || out != "ok" {
		t.Fatalf("zero timeout should not wrap, got %q, %v", out, err)
	}
}

func TestInvokePropagatesToolError(t *testing.T) {
	failing := Tool{
		Name: "failing",
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			return "", context.DeadlineExceeded
		},
	}
	if _, err := failing.Invoke(context.Background(), nil, time.Minute); err == nil {
		t.Fatal("tool error should propagate")
	}
}

func TestInvokeCarriesSessionArgument(t *testing.T) {
	var got string
	tool := Tool{
		Name:   "t",
		Schema: map[string]any{"type": "object", "properties": map[string]any{SessionArg: map[string]any{"type": "string"}}},
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			got = SessionFrom(ctx)
			return "ok", nil
		},
	}

	if _, err := tool.Invoke(context.Background(), map[string]any{"session": "serial-2"}, time.Minute); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "serial-2" {
		t.Fatalf("session = %q, want serial-2", got)
	}

	// Without the argument the capabilities resolve the focused session.
	got = "unset"
	if _, err := tool.Invoke(context.Background(), map[string]any{}, time.Minute); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "" {
		t.Fatalf("session = %q, want empty (focused)", got)
	}
}

// TestInvokeIgnoresSessionWhenNotDeclared guards the convergence rule: a tool
// that does not declare `session` in its schema is never routed by it, so
// external tools cannot be redirected by a stray argument.
func TestInvokeIgnoresSessionWhenNotDeclared(t *testing.T) {
	var got string
	tool := Tool{
		Name:   "ext",
		Schema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			got = SessionFrom(ctx)
			return "ok", nil
		},
	}
	if _, err := tool.Invoke(context.Background(), map[string]any{"session": "serial-2"}, time.Minute); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got != "" {
		t.Fatalf("undeclared session must not route, got %q", got)
	}
	if tool.DeclaresSession() {
		t.Fatal("tool without the session property must not declare it")
	}
}
