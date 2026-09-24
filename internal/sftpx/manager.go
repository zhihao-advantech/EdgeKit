// Package sftpx provides SFTP file browsing and transfer. It attaches to an
// existing SSH connection so a single SSH session serves both the terminal and
// the workspace file panel.
package sftpx

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// MaxTransfer bounds a single upload/download to keep the payload sane.
const MaxTransfer = 16 << 20 // 16 MiB

// Entry describes one remote directory entry.
type Entry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
	IsDir   bool      `json:"isDir"`
}

// Manager owns the SFTP subsystem of one SSH connection.
type Manager struct {
	mu      sync.Mutex
	sftp    *sftp.Client
	label   string
	onEvent func(kind, msg string)

	// opMu serializes whole operations (and Attach/Detach) so the client handle
	// is never closed while an operation is using it.
	opMu sync.Mutex
}

// New creates an SFTP manager.
func New(onEvent func(kind, msg string)) *Manager {
	return &Manager{onEvent: onEvent}
}

func (m *Manager) emit(kind, msg string) {
	if m.onEvent != nil {
		m.onEvent(kind, msg)
	}
}

// Attach starts an SFTP subsystem on an existing SSH client. The client is
// owned by the caller (sshclient) and is not closed by Detach.
func (m *Manager) Attach(client *ssh.Client, label string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.sftp != nil {
		m.mu.Unlock()
		return fmt.Errorf("SFTP 已就绪")
	}
	m.mu.Unlock()

	sc, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("启动 SFTP 子系统失败: %w", err)
	}
	m.mu.Lock()
	m.sftp = sc
	m.label = label
	m.mu.Unlock()
	m.emit("info", "SFTP 已就绪: "+label)
	return nil
}

// Detach closes the SFTP subsystem (leaving the SSH connection intact).
func (m *Manager) Detach() {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	sc := m.sftp
	was := sc != nil
	m.sftp = nil
	m.label = ""
	m.mu.Unlock()
	if sc != nil {
		_ = sc.Close()
	}
	if was {
		m.emit("info", "SFTP 已关闭")
	}
}

// IsConnected reports whether the SFTP subsystem is available.
func (m *Manager) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sftp != nil
}

// Label returns the human-readable target of the SFTP session.
func (m *Manager) Label() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.label
}

// List returns the directory entries at path (directories first).
func (m *Manager) List(path string) ([]Entry, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	sc, err := m.session()
	if err != nil {
		return nil, err
	}
	infos, err := sc.ReadDir(normalize(path))
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}
	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		entries = append(entries, Entry{
			Name:    info.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// Mkdir creates a remote directory (and parents).
func (m *Manager) Mkdir(path string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	sc, err := m.session()
	if err != nil {
		return err
	}
	if err := sc.MkdirAll(normalize(path)); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	return nil
}

// NewFile creates an empty remote file.
func (m *Manager) NewFile(path string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	sc, err := m.session()
	if err != nil {
		return err
	}
	f, err := sc.OpenFile(normalize(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	return f.Close()
}

// Delete removes a remote file, or a directory recursively.
func (m *Manager) Delete(path string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	sc, err := m.session()
	if err != nil {
		return err
	}
	p := normalize(path)
	if p == "/" {
		return fmt.Errorf("不能删除根目录")
	}
	info, err := sc.Stat(p)
	if err != nil {
		return fmt.Errorf("读取失败: %w", err)
	}
	if !info.IsDir() {
		if err := sc.Remove(p); err != nil {
			return fmt.Errorf("删除失败: %w", err)
		}
		return nil
	}
	if err := removeAll(sc, p); err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	return nil
}

func removeAll(sc *sftp.Client, dir string) error {
	entries, err := sc.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := strings.TrimRight(dir, "/") + "/" + e.Name()
		if e.IsDir() {
			if err := removeAll(sc, full); err != nil {
				return err
			}
			continue
		}
		if err := sc.Remove(full); err != nil {
			return err
		}
	}
	return sc.RemoveDirectory(dir)
}

// Download reads a remote file (bounded by MaxTransfer).
func (m *Manager) Download(path string) ([]byte, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	sc, err := m.session()
	if err != nil {
		return nil, err
	}
	f, err := sc.Open(normalize(path))
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxTransfer+1))
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	if len(data) > MaxTransfer {
		return nil, fmt.Errorf("文件超过 %d MiB 上限", MaxTransfer>>20)
	}
	return data, nil
}

// Upload writes data to a remote file, creating or truncating it.
func (m *Manager) Upload(path string, data []byte) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	sc, err := m.session()
	if err != nil {
		return err
	}
	f, err := sc.Create(normalize(path))
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	return nil
}

func (m *Manager) session() (*sftp.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sftp == nil {
		return nil, fmt.Errorf("SFTP 未就绪（请先连接 SSH）")
	}
	return m.sftp, nil
}

func normalize(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}
