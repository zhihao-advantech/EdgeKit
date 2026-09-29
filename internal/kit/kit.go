// Package kit defines EdgeKit's extension model.
//
// A Kit is a capability pack that contributes tools to the host. Built-in kits
// ship inside the binary; external kits will attach over MCP later. The host
// aggregates every activated kit into a Registry, which the built-in agent and
// (in a later phase) the MCP server both consume — so a capability is defined
// exactly once.
package kit

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"edgekit/internal/sftpx"
	"edgekit/internal/testrun"
	"edgekit/internal/timeline"
)

// Risk classifies what a tool does, so the host can gate it. The built-in agent
// asks for approval on anything that is not RiskRead.
type Risk string

const (
	RiskRead      Risk = "read"
	RiskMutate    Risk = "mutate"
	RiskDangerous Risk = "dangerous"
)

// Tool is a capability contributed by a kit.
type Tool struct {
	Name        string
	Description string
	Risk        Risk
	Schema      map[string]any // JSON Schema for the arguments
	Call        func(ctx context.Context, args map[string]any) (string, error)
}

// Mutating reports whether the tool changes device or host state.
func (t Tool) Mutating() bool { return t.Risk != RiskRead }

// SessionArg is the shared argument that routes a device tool call to a specific
// device session. It is not special on its own: a tool participates in routing
// only when its schema declares this property (see DeclaresSession), so
// non-device and external tools are unaffected by a stray "session" key.
const SessionArg = "session"

// DeclaresSession reports whether the tool's schema exposes the session
// argument, i.e. it is a device tool that can target a specific session.
func (t Tool) DeclaresSession() bool {
	if t.Schema == nil {
		return false
	}
	props, _ := t.Schema["properties"].(map[string]any)
	if props == nil {
		return false
	}
	_, ok := props[SessionArg]
	return ok
}

// DefaultCallTimeout bounds a single tool call at the host's call sites. Long
// but legitimate operations (builds, flashing) still fit; a hung tool cannot
// stall an agent turn or an MCP client forever.
const DefaultCallTimeout = 10 * time.Minute

// callResult is the outcome of one tool call goroutine.
type callResult struct {
	out string
	err error
}

// Invoke runs the tool under a per-call deadline. The caller's wait is bounded
// even when the tool ignores its context: the call runs in its own goroutine,
// and a deadline that fires mid-call is reported as an error immediately (the
// goroutine itself finishes naturally in the background). timeout <= 0 bounds
// the call by ctx alone.
func (t Tool) Invoke(ctx context.Context, args map[string]any, timeout time.Duration) (string, error) {
	// A shared `session` argument routes the call to a specific device session;
	// only tools that declare it are routed, so external tools are unaffected.
	if t.DeclaresSession() {
		if id, _ := args[SessionArg].(string); id != "" {
			ctx = WithSession(ctx, id)
		}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	done := make(chan callResult, 1) // buffered: the goroutine never blocks on send
	go func() {
		out, err := t.Call(ctx, args)
		done <- callResult{out, err}
	}()
	select {
	case r := <-done:
		return t.finished(r, ctx, timeout)
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			return "", ctx.Err()
		}
		// A tool that respects its context often surfaces its partial output
		// exactly at the deadline; give it a short grace before reporting a
		// bare timeout.
		grace := time.NewTimer(50 * time.Millisecond)
		defer grace.Stop()
		select {
		case r := <-done:
			return t.finished(r, ctx, timeout)
		case <-grace.C:
			return "", fmt.Errorf("工具 %s 执行超时（上限 %s）", t.Name, timeout)
		}
	}
}

// finished maps a completed call to its result, converting a deadline that
// fired mid-call into a timeout error (keeping any partial output).
func (t Tool) finished(r callResult, ctx context.Context, timeout time.Duration) (string, error) {
	if r.err == nil && ctx.Err() == context.DeadlineExceeded {
		if r.out != "" {
			return r.out, fmt.Errorf("工具 %s 执行超时（上限 %s），以上为已产生的输出", t.Name, timeout)
		}
		return "", fmt.Errorf("工具 %s 执行超时（上限 %s）", t.Name, timeout)
	}
	return r.out, r.err
}

// Manifest describes a kit. It mirrors an editor extension manifest so the same
// shape can later be loaded from a kit.json for external kits.
type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	License     string   `json:"license,omitempty"`
	Runtime     string   `json:"runtime"`              // "builtin" (or "mcp" later)
	Activation  []string `json:"activation,omitempty"` // onStartup, onDeviceKind:serial, ...
	Description string   `json:"description,omitempty"`
}

// Kit is a capability pack.
type Kit interface {
	Manifest() Manifest
	Tools() []Tool
}

// Activation events a manifest can declare. A kit is exposed while any one of
// its activation events is satisfied, and hidden (like a disabled kit) while
// none is.
const (
	// EventStartup is satisfied from process start.
	EventStartup = "onStartup"
	// eventDeviceKindPrefix builds "a session of this device kind exists".
	eventDeviceKindPrefix = "onDeviceKind:"
)

// DeviceKindEvent returns the activation event meaning "a session of this
// device kind exists" (e.g. "serial", "ssh"). The host fires it when the
// first session of the kind appears and when the last one goes away.
func DeviceKindEvent(kind string) string { return eventDeviceKindPrefix + kind }

// Namespace builds a prefixed tool name for an external provider (e.g. an MCP
// connector) so remote tool names cannot collide with built-in tools or across
// providers. Characters outside [A-Za-z0-9_-] become '_'.
func Namespace(provider, name string) string {
	return sanitizeIdent(provider) + "_" + sanitizeIdent(name)
}

// sanitizeIdent keeps only identifier-safe characters.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

/* ------------------------------------------------------------------ *
 * capabilities the built-in kits operate on
 *
 * They are implemented by the host's session proxies. Action methods take a
 * context so a call can target a specific device session (kit.WithSession);
 * status methods report the focused session. A tool call always resolves its
 * device through the context, defaulting to the currently selected one.
 * ------------------------------------------------------------------ */

// Serial is the serial capability.
type Serial interface {
	IsOpen() bool
	Port() string
	Write(ctx context.Context, p []byte) error
	Recent(ctx context.Context) []byte
	RunCapture(ctx context.Context, command string, quiet, timeout time.Duration) (string, error)
}

// SSH is the SSH capability.
type SSH interface {
	IsConnected() bool
	Target() string
	ExecCapture(ctx context.Context, command string, maxBytes int) (string, error)
}

// SFTP is the SFTP capability.
type SFTP interface {
	IsConnected() bool
	List(ctx context.Context, path string) ([]sftpx.Entry, error)
	Download(ctx context.Context, path string) ([]byte, error)
	Upload(ctx context.Context, path string, data []byte) error
}

// Timeline is the device-record capability: the append-only log of everything
// observed or done on a device session. It lets a kit wait for a device output
// instead of polling (e.g. a boot banner on the serial console), and lets the
// host audit what was done to the device.
type Timeline interface {
	Append(ctx context.Context, r timeline.Record) timeline.Record
	Wait(ctx context.Context, f timeline.Filter, timeout time.Duration) (timeline.Record, error)
	Since(ctx context.Context, after uint64, limit int) []timeline.Record
	LastSeq(ctx context.Context) uint64
}

// Deps bundles the capabilities handed to the built-in kits.
type Deps struct {
	Serial   Serial
	SSH      SSH
	SFTP     SFTP
	Timeline Timeline
	// Sessions returns the open device sessions, so a brain can list them and
	// address one explicitly. Nil when the host has no directory.
	Sessions func() []SessionInfo
	// Test drives the host's test runs (list cases, run one, read a report).
	Test Test
}

// Test is the test-run capability: list saved cases and recent runs, run a case
// against a device session (connect → run → generate → archive), and read a
// previous run's report.
type Test interface {
	Definitions() []testrun.DefinitionRef
	Runs() []testrun.RunSummary
	// Run executes the case identified by path (a workspace definition) or def
	// (an inline definition) on sessionID (empty = focused) to completion.
	Run(ctx context.Context, sessionID, path string, def *testrun.Definition) (testrun.Run, error)
	// Report returns the Markdown report of an archived run.
	Report(runID string) (string, error)
}

/* ------------------------------------------------------------------ *
 * registry
 * ------------------------------------------------------------------ */

// entry is a registered kit and whether it is activated.
type entry struct {
	kit     Kit
	enabled bool
}

type toolEntry struct {
	tool  Tool
	kitID string
}

// Registry aggregates the tools contributed by the activated kits.
//
// A kit can be enabled or disabled (like an editor extension): a disabled kit's
// tools are neither advertised to a brain nor executable, but its manifest is
// still listed so the UI can offer to turn it back on. On top of that, a kit's
// activation events decide whether it is currently exposed: the host fires the
// events (e.g. onDeviceKind:serial while a serial session exists), so a kit
// whose capabilities no session backs is hidden from the agent and MCP until
// one appears — keeping the advertised tool surface small and relevant.
type Registry struct {
	mu     sync.Mutex
	kits   []*entry
	tools  map[string]toolEntry
	order  []string
	events map[string]bool
}

// NewRegistry returns an empty registry. onStartup is satisfied from the
// beginning; every other event starts unsatisfied until the host fires it.
func NewRegistry() *Registry {
	return &Registry{
		tools:  make(map[string]toolEntry),
		events: map[string]bool{EventStartup: true},
	}
}

// Register adds a kit's contributions (enabled by default). A tool name already
// registered by an earlier kit is not overwritten.
func (r *Registry) Register(k Kit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kits = append(r.kits, &entry{kit: k, enabled: true})
	id := k.Manifest().ID
	for _, t := range k.Tools() {
		if prev, exists := r.tools[t.Name]; exists {
			log.Printf("kit %s: tool %q already provided by %s, ignoring", id, t.Name, prev.kitID)
			continue
		}
		r.tools[t.Name] = toolEntry{tool: t, kitID: id}
		r.order = append(r.order, t.Name)
	}
}

// Replace registers an external kit, replacing any prior tools for the same
// kit id and keeping its enabled state. It backs (re)connecting an MCP
// connector: the tool surface follows the remote server without duplicating the
// kit entry. A tool name still owned by a *different* kit is skipped.
func (r *Registry) Replace(k Kit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := k.Manifest().ID

	enabled := true
	found := false
	kept := r.kits[:0]
	for _, e := range r.kits {
		if e.kit.Manifest().ID == id {
			enabled = e.enabled
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if found {
		r.kits = append(kept, &entry{kit: k, enabled: enabled})
	} else {
		r.kits = append(r.kits, &entry{kit: k, enabled: enabled})
	}

	// Drop this kit's previous tools, then re-add the current set.
	drop := map[string]bool{}
	for name, te := range r.tools {
		if te.kitID == id {
			drop[name] = true
			delete(r.tools, name)
		}
	}
	if len(drop) > 0 {
		order := r.order[:0]
		for _, name := range r.order {
			if !drop[name] {
				order = append(order, name)
			}
		}
		r.order = order
	}
	for _, t := range k.Tools() {
		if prev, exists := r.tools[t.Name]; exists {
			log.Printf("kit %s: tool %q already provided by %s, ignoring", id, t.Name, prev.kitID)
			continue
		}
		r.tools[t.Name] = toolEntry{tool: t, kitID: id}
		r.order = append(r.order, t.Name)
	}
}

// SetEnabled activates or deactivates a kit. It reports whether the kit exists.
func (r *Registry) SetEnabled(id string, on bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.kits {
		if e.kit.Manifest().ID == id {
			e.enabled = on
			return true
		}
	}
	return false
}

// Remove unregisters a kit and its tools. It is used when an external
// connector's process dies, so its tools disappear from the surface instead of
// lingering as dead entries.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.kits[:0]
	for _, e := range r.kits {
		if e.kit.Manifest().ID != id {
			kept = append(kept, e)
		}
	}
	r.kits = kept

	drop := map[string]bool{}
	for name, te := range r.tools {
		if te.kitID == id {
			drop[name] = true
			delete(r.tools, name)
		}
	}
	if len(drop) > 0 {
		order := r.order[:0]
		for _, name := range r.order {
			if !drop[name] {
				order = append(order, name)
			}
		}
		r.order = order
	}
}

// IsEnabled reports whether a kit is activated by the user (regardless of its
// activation events).
func (r *Registry) IsEnabled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.kits {
		if e.kit.Manifest().ID == id {
			return e.enabled
		}
	}
	return false
}

// IsActive reports whether a kit currently exposes its tools: enabled by the
// user and with at least one activation event satisfied.
func (r *Registry) IsActive(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.availableLocked(id)
}

// ToolKit returns the id of the kit that contributed a tool.
func (r *Registry) ToolKit(name string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	te, ok := r.tools[name]
	return te.kitID, ok
}

// Tools returns the tools of the enabled kits, in registration order.
func (r *Registry) Tools() []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.toolsLocked()
}

// Tool looks up an enabled tool by name. A kit that is disabled or whose
// activation is currently unsatisfied is as good as absent.
func (r *Registry) Tool(name string) (Tool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	te, ok := r.tools[name]
	if !ok || !r.availableLocked(te.kitID) {
		return Tool{}, false
	}
	return te.tool, true
}

// KitTools returns every tool contributed by one kit (regardless of state).
func (r *Registry) KitTools(id string) []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Tool{}
	for _, name := range r.order {
		if te := r.tools[name]; te.kitID == id {
			out = append(out, te.tool)
		}
	}
	return out
}

// Manifests returns the manifests of the registered kits, sorted by id.
func (r *Registry) Manifests() []Manifest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Manifest, 0, len(r.kits))
	for _, e := range r.kits {
		out = append(out, e.kit.Manifest())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SetEvent marks an activation event as satisfied (true) or not (false). The
// host fires device-kind events as sessions appear and go away; startup is
// satisfied from creation.
func (r *Registry) SetEvent(event string, active bool) {
	r.mu.Lock()
	r.events[event] = active
	r.mu.Unlock()
}

// Events returns the currently satisfied activation events (e.g. for the
// About panel).
func (r *Registry) Events() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.events))
	for ev, active := range r.events {
		if active {
			out[ev] = true
		}
	}
	return out
}

func (r *Registry) toolsLocked() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		te := r.tools[name]
		if r.availableLocked(te.kitID) {
			out = append(out, te.tool)
		}
	}
	return out
}

// availableLocked reports whether a kit currently exposes its tools: enabled
// by the user and with at least one activation event satisfied.
func (r *Registry) availableLocked(id string) bool {
	for _, e := range r.kits {
		if e.kit.Manifest().ID == id {
			if !e.enabled {
				return false
			}
			m := e.kit.Manifest()
			if len(m.Activation) == 0 {
				return true
			}
			for _, ev := range m.Activation {
				if r.events[ev] {
					return true
				}
			}
			return false
		}
	}
	return false
}

/* ------------------------------------------------------------------ *
 * shared helpers
 * ------------------------------------------------------------------ */

// Truncate shortens s to at most n bytes, marking the cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(已截断)"
}
