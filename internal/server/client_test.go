package server

import (
	"sync"
	"testing"
)

// TestClientEnqueueAfterClose guards against the send-on-closed-channel panic:
// enqueue must be a safe no-op once the client is closed, even under races.
func TestClientEnqueueAfterClose(t *testing.T) {
	c := &client{send: make(chan []byte, 4), quit: make(chan struct{})}
	c.close()

	// After close, enqueue must not panic.
	c.enqueue([]byte("after-close"))

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.enqueue([]byte("x")) }()
		go func() { defer wg.Done(); c.close() }()
	}
	wg.Wait()

	select {
	case <-c.quit:
	default:
		t.Fatal("quit channel should be closed")
	}
}
