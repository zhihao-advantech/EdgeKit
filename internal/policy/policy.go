// Package policy is EdgeKit's approval gate: the single place that decides
// whether a tool call may run.
//
// read      → allow
// mutate    → ask the user, unless auto-run is enabled
// dangerous → always ask the user (never auto-run)
//
// Runtime-attached external kits (MCP connectors) never inherit the global
// auto-run switch, so attaching a third-party server cannot silently grant it
// unattended write access.
//
// The built-in agent and the MCP surface both go through it, so a mutating
// action is gated identically no matter which brain requested it.
package policy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"edgekit/internal/kit"
)

// Errors returned by Check.
var (
	ErrDenied  = errors.New("用户拒绝执行")
	ErrTimeout = errors.New("审批超时")
	ErrBlocked = errors.New("该操作被策略禁止")
)

// externalKitPrefix marks kits attached at runtime (MCP connectors), whose
// tools never inherit the global auto-run switch.
const externalKitPrefix = "edgekit.connector."

func externalKit(kitID string) bool { return strings.HasPrefix(kitID, externalKitPrefix) }

// Request is an approval prompt shown to the user.
type Request struct {
	ID   string
	Tool string
	Args string
}

// Notifier delivers approval prompts to the UI.
type Notifier func(Request)

// Gate decides whether a tool call may proceed.
type Gate struct {
	mu      sync.Mutex
	notify  Notifier
	autoRun bool
	pending map[string]chan bool
	seq     int
	timeout time.Duration
}

// New creates a gate that asks the user through notify.
func New(notify Notifier) *Gate {
	return &Gate{
		notify:  notify,
		pending: make(map[string]chan bool),
		timeout: 5 * time.Minute,
	}
}

// SetAutoRun toggles skip-approval mode.
func (g *Gate) SetAutoRun(v bool) {
	g.mu.Lock()
	g.autoRun = v
	g.mu.Unlock()
}

// AutoRun reports the current setting.
func (g *Gate) AutoRun() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.autoRun
}

// Approve answers a pending prompt.
func (g *Gate) Approve(id string, allow bool) {
	g.mu.Lock()
	ch := g.pending[id]
	delete(g.pending, id)
	g.mu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
	}
}

// Check returns nil when the call may proceed. kitID lets the gate treat
// runtime-attached external kits (connectors) specially: they never inherit the
// global auto-run switch, and "dangerous" tools always require an explicit,
// per-call approval even when auto-run is on.
func (g *Gate) Check(ctx context.Context, kitID, toolName string, risk kit.Risk, argsText string) error {
	if risk == kit.RiskRead {
		return nil
	}
	if g.AutoRun() && risk != kit.RiskDangerous && !externalKit(kitID) {
		return nil
	}

	id := g.nextID()
	ch := make(chan bool, 1)
	g.mu.Lock()
	g.pending[id] = ch
	notify := g.notify
	timeout := g.timeout
	g.mu.Unlock()

	if notify != nil {
		notify(Request{ID: id, Tool: toolName, Args: argsText})
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case allow := <-ch:
		if allow {
			return nil
		}
		return ErrDenied
	case <-timer.C:
		g.discard(id)
		return ErrTimeout
	case <-ctx.Done():
		g.discard(id)
		return ctx.Err()
	}
}

func (g *Gate) discard(id string) {
	g.mu.Lock()
	delete(g.pending, id)
	g.mu.Unlock()
}

func (g *Gate) nextID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq++
	return "a" + itoa(g.seq)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
