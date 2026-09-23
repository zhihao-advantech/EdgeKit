// Package runtime publishes where the running EdgeKit instance listens, so
// helper processes (such as `edgekit mcp`) can find the app that owns the
// device sessions.
package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Endpoint is the published address of a running instance.
type Endpoint struct {
	WS      string    `json:"ws"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

// Path returns the endpoint file location (~/.config/edgekit/runtime.json).
func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "edgekit", "runtime.json")
}

// Write publishes ep.
func Write(ep Endpoint) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

// Read loads the published endpoint.
func Read() (Endpoint, error) {
	p := Path()
	data, err := os.ReadFile(p)
	if err != nil {
		return Endpoint{}, fmt.Errorf("EdgeKit 未运行（找不到 %s）", p)
	}
	var ep Endpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		return Endpoint{}, fmt.Errorf("解析 %s 失败: %w", p, err)
	}
	if ep.WS == "" {
		return Endpoint{}, fmt.Errorf("%s 内容无效", p)
	}
	return ep, nil
}

// Clear removes the published endpoint.
func Clear() {
	_ = os.Remove(Path())
}
