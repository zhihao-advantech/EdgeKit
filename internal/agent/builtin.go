package agent

import "context"

// builtinBackend runs EdgeKit's in-process agent: the OpenAI-compatible
// function-calling loop when a model is configured, otherwise the deterministic
// built-in workflows (inspection, logs, disk, memory, ping, ...).
type builtinBackend struct {
	m *Manager
}

func (b *builtinBackend) Send(ctx context.Context, text string) error {
	cfg := b.m.Config()
	if cfg.APIKey != "" && cfg.Model != "" {
		b.m.runLLM(ctx)
	} else {
		b.m.runLocal(ctx, text)
	}
	return nil
}

// Cancel is a no-op: the Manager cancels the turn context, which the built-in
// workflows and the model call already observe.
func (b *builtinBackend) Cancel() {}

// Reset is a no-op: the Manager owns the built-in conversation history.
func (b *builtinBackend) Reset() {}

func (b *builtinBackend) Close() error { return nil }
