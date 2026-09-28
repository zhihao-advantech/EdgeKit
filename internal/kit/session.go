package kit

import "context"

// SessionInfo describes one device session the host has open, so a brain can
// address it explicitly instead of acting on whatever is focused.
type SessionInfo struct {
	ID        string
	Kind      string // serial | ssh | desktop
	Label     string
	Connected bool
}

// sessionKey carries the target device session of one tool call.
type sessionKey struct{}

// WithSession returns a context whose device capability calls act on the given
// session instead of the focused one. The host applies it from the shared
// `session` tool argument before executing a tool.
func WithSession(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, id)
}

// SessionFrom returns the session id carried by ctx ("" means the focused one).
func SessionFrom(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}
