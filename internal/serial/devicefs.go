package serial

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DeviceEntry is a file entry scraped from a serial console.
type DeviceEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
	Mode  string `json:"mode"`
	Time  string `json:"time"`
}

// RunCapture writes command to the port and collects the device response until
// the line goes quiet (no new bytes for `quiet`) or `timeout` elapses. It is a
// best-effort way to read command output from a serial console. The injected
// command and its echo are shown in the terminal stream.
func (m *Manager) RunCapture(command string, quiet, timeout time.Duration) (string, error) {
	return m.runCapture(command, quiet, timeout, false)
}

// RunCaptureSilent is like RunCapture, but the injected command, its echo and
// the response are kept out of the terminal stream. Used by the workspace panel
// so browsing the device directory does not pollute the receive buffer.
func (m *Manager) RunCaptureSilent(command string, quiet, timeout time.Duration) (string, error) {
	return m.runCapture(command, quiet, timeout, true)
}

func (m *Manager) runCapture(command string, quiet, timeout time.Duration, silent bool) (string, error) {
	if !m.IsOpen() {
		return "", fmt.Errorf("串口未打开")
	}
	if silent {
		m.setCapturing(true)
		defer m.setCapturing(false)
	}

	m.mu.Lock()
	start := len(m.recent)
	m.mu.Unlock()

	if silent {
		if err := m.writeRaw([]byte(command + "\r")); err != nil {
			return "", err
		}
	} else if err := m.Write([]byte(command + "\r")); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	lastLen := start
	lastChange := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		m.mu.Lock()
		n := len(m.recent)
		m.mu.Unlock()
		if n != lastLen {
			lastLen = n
			lastChange = time.Now()
		} else if time.Since(lastChange) >= quiet {
			break
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if start > len(m.recent) {
		start = 0 // ring buffer wrapped past the mark
	}
	out := make([]byte, len(m.recent)-start)
	copy(out, m.recent[start:])
	return strings.ToValidUTF8(string(out), "\uFFFD"), nil
}

// lsLineRe matches a long-format listing line:
//
//	drwxr-xr-x  2 root root 4096 Jan  1 00:00 name
var lsLineRe = regexp.MustCompile(
	`^([dlbcps-][rwxStTs-]{9}[.+@]?)\s+(\d+)\s+(\S+)\s+(\S+)\s+(\d+)\s+([A-Za-z]{3}\s+\d{1,2}\s+(?:\d{2}:\d{2}|\d{4}))\s+(.+)$`)

// ParseLS parses `ls -la` output captured from a device console, ignoring the
// echoed command, prompts and any other noise.
func ParseLS(out string) []DeviceEntry {
	var entries []DeviceEntry
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		mm := lsLineRe.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		name := mm[7]
		if i := strings.Index(name, " -> "); i >= 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "." || name == ".." {
			continue
		}
		size, _ := strconv.ParseInt(mm[5], 10, 64)
		entries = append(entries, DeviceEntry{
			Name:  name,
			Size:  size,
			IsDir: mm[1][0] == 'd',
			Mode:  mm[1],
			Time:  mm[6],
		})
	}
	return entries
}
