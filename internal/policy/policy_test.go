package policy

import (
	"context"
	"sync"
	"testing"
	"time"

	"edgekit/internal/kit"
)

func TestReadIsAllowedWithoutAsking(t *testing.T) {
	asked := false
	g := New(func(Request) { asked = true })
	if err := g.Check(context.Background(), "serial_read", kit.RiskRead, ""); err != nil {
		t.Fatalf("read tool should always run: %v", err)
	}
	if asked {
		t.Fatal("read tool must not prompt")
	}
}

func TestDangerousIsBlocked(t *testing.T) {
	asked := false
	g := New(func(Request) { asked = true })
	if err := g.Check(context.Background(), "flash", kit.RiskDangerous, ""); err != ErrBlocked {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if asked {
		t.Fatal("dangerous tool must not prompt")
	}
}

func TestAutoRunSkipsApproval(t *testing.T) {
	g := New(func(Request) { t.Fatal("auto-run must not prompt") })
	g.SetAutoRun(true)
	if err := g.Check(context.Background(), "serial_write", kit.RiskMutate, `{"data":"x"}`); err != nil {
		t.Fatalf("auto-run mutate should pass: %v", err)
	}
}

func TestMutateAsksAndHonoursApproval(t *testing.T) {
	var (
		mu     sync.Mutex
		gotID  string
		called bool
	)
	g := New(func(r Request) {
		mu.Lock()
		gotID, called = r.ID, true
		mu.Unlock()
	})

	done := make(chan error, 1)
	go func() {
		done <- g.Check(context.Background(), "ssh_exec", kit.RiskMutate, `{"command":"reboot"}`)
	}()

	// wait for the prompt, then allow it
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		id, ok := gotID, called
		mu.Unlock()
		if ok {
			g.Approve(id, true)
			break
		}
		select {
		case <-deadline:
			t.Fatal("no prompt was delivered")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("approved call should pass: %v", err)
	}
}

func TestMutateDenied(t *testing.T) {
	prompted := make(chan string, 1)
	g := New(func(r Request) { prompted <- r.ID })

	done := make(chan error, 1)
	go func() { done <- g.Check(context.Background(), "ssh_exec", kit.RiskMutate, "") }()

	select {
	case id := <-prompted:
		g.Approve(id, false)
	case <-time.After(2 * time.Second):
		t.Fatal("no prompt")
	}
	if err := <-done; err != ErrDenied {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}

func TestPromptTimeout(t *testing.T) {
	g := New(func(Request) {}) // never answered
	g.timeout = 60 * time.Millisecond

	done := make(chan error, 1)
	go func() { done <- g.Check(context.Background(), "ssh_exec", kit.RiskMutate, "") }()

	select {
	case err := <-done:
		if err != ErrTimeout {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Check did not time out")
	}
}

func TestContextCancelUnblocks(t *testing.T) {
	g := New(func(Request) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Check(ctx, "ssh_exec", kit.RiskMutate, "") }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not unblock Check")
	}
}
