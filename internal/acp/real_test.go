package acp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealAgent drives a real ACP agent when ACP_REAL_CMD is set, e.g.
//
//	ACP_REAL_CMD=hermes ACP_REAL_ARGS=acp go test ./internal/acp -run TestRealAgent -v
//
// It is skipped by default so the unit suite stays hermetic.
func TestRealAgent(t *testing.T) {
	cmd := os.Getenv("ACP_REAL_CMD")
	if cmd == "" {
		t.Skip("set ACP_REAL_CMD to run against a real agent")
	}
	c := New(Options{
		Command: cmd,
		Args:    strings.Fields(os.Getenv("ACP_REAL_ARGS")),
		Logf:    t.Logf,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	var servers []MCPServer
	if os.Getenv("ACP_REAL_MCP") != "" {
		// A harmless command: the session must still be created even though
		// the MCP handshake fails.
		servers = []MCPServer{{Name: "dummy", Command: "/bin/echo"}}
	}

	sess, err := c.NewSession(ctx, "/tmp", servers)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("empty session id")
	}
	t.Logf("session created: %s (models=%d current=%q)", sess.ID, len(sess.Models), sess.CurrentModelID)

	if list, err := c.ListSessions(ctx, ""); err != nil {
		t.Logf("ListSessions: %v", err)
	} else {
		t.Logf("ListSessions: %d session(s)", len(list))
		if len(list) > 0 {
			loaded, err := c.LoadSession(ctx, "/tmp", list[0].SessionID, nil)
			if err != nil {
				t.Logf("LoadSession(%s): %v", list[0].SessionID, err)
			} else {
				t.Logf("LoadSession ok: %s", loaded.ID)
			}
		}
	}
}
