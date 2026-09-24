package agent

import (
	"context"
	"strings"
)

// Backend names.
const (
	BackendBuiltin = "builtin" // in-process OpenAI-compatible / Normal agent
	BackendACP     = "acp"     // external agent driven over the Agent Client Protocol
)

// Backend is the swappable "brain" behind a Manager. The built-in backend runs
// the in-process model loop; the ACP backend drives an external agent (Hermes,
// OpenClaw, ...) as a child process. Both report through the same Event stream,
// so the host, the UI protocol and the approval gate stay unchanged.
type Backend interface {
	// Send runs one turn to completion (or until Cancel).
	Send(ctx context.Context, text string) error
	// Cancel aborts the running turn.
	Cancel()
	// Reset clears the conversation.
	Reset()
	// Close releases the backend and any child process.
	Close() error
}

// Model is one selectable model exposed to the UI.
type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Preparer is implemented by backends that must start (or connect) before their
// model list is known.
type Preparer interface {
	Prepare(ctx context.Context) error
}

// ModelSelector is implemented by backends that can list and switch models.
type ModelSelector interface {
	Models() []Model
	CurrentModel() string
	SetModel(ctx context.Context, modelID string) error
}

// SessionInfo describes one persisted agent session exposed to the UI.
type SessionInfo struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// SessionController is implemented by backends that can create, list and
// resume conversations without restarting the agent process.
type SessionController interface {
	NewSession(ctx context.Context) error
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	LoadSession(ctx context.Context, sessionID string) error
	CurrentSession() string
}

// backendName normalizes the configured backend name.
func (c Config) backendName() string {
	if c.Backend == BackendACP {
		return BackendACP
	}
	return BackendBuiltin
}

// backendKey identifies a backend instance; a change means the Manager must
// tear down the old one before the next turn.
func (c Config) backendKey() string {
	if c.Backend == BackendACP {
		key := BackendACP + "|" + c.ACPCommand + "|" + strings.Join(c.ACPArgs, " ")
		if c.ACPOverride {
			key += "|override|" + c.BaseURL + "|" + c.APIKey
		}
		return key
	}
	return BackendBuiltin
}

// newBackend builds the backend selected by cfg.
func newBackend(cfg Config, m *Manager) Backend {
	if cfg.backendName() == BackendACP {
		return newACPBackend(cfg, m)
	}
	return &builtinBackend{m: m}
}

// backendLocked returns the active backend, rebuilding it when the selection
// changed. Callers must hold m.mu.
func (m *Manager) backendLocked(cfg Config) Backend {
	key := cfg.backendKey()
	if m.backend != nil && m.backendKey == key {
		return m.backend
	}
	if m.backend != nil {
		_ = m.backend.Close()
	}
	m.backend = newBackend(cfg, m)
	m.backendKey = key
	return m.backend
}
