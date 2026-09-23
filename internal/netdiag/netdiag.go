// Package netdiag provides small synchronous network checks used by the
// EdgeKit agent (ping, TCP port probe, DNS resolution, local host info).
package netdiag

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// SysInfo returns a short description of the local host.
func SysInfo() string {
	host, _ := os.Hostname()
	var b strings.Builder
	fmt.Fprintf(&b, "主机名: %s\n", host)
	fmt.Fprintf(&b, "系统: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "CPU: %d 核\n", runtime.NumCPU())
	fmt.Fprintf(&b, "Go: %s\n", runtime.Version())
	fmt.Fprintf(&b, "时间: %s", time.Now().Format(time.RFC3339))
	return b.String()
}

// Ping runs the system ping command and returns its output.
func Ping(ctx context.Context, host string, count int) (string, error) {
	if host == "" {
		return "", fmt.Errorf("目标主机为空")
	}
	if count <= 0 {
		count = 4
	}
	cmd := exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(count), host)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil && text == "" {
		return "", fmt.Errorf("ping 失败: %w", err)
	}
	return text, nil
}

// CheckPort probes a TCP port and returns whether it is open plus the latency.
func CheckPort(host string, port int) (bool, time.Duration, error) {
	if host == "" {
		return false, 0, fmt.Errorf("目标主机为空")
	}
	if port <= 0 {
		port = 22
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return false, time.Since(start), nil
	}
	_ = conn.Close()
	return true, time.Since(start), nil
}

// Resolve performs a DNS lookup.
func Resolve(host string) ([]string, error) {
	if host == "" {
		return nil, fmt.Errorf("目标主机为空")
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return nil, fmt.Errorf("解析失败: %w", err)
	}
	return ips, nil
}
