// Package workspace provides sandboxed access to the local EdgeKit working
// directory (~/EdgeKit/workspace). All paths are relative to that root and
// cannot escape it.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry describes one file or directory.
type Entry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
	IsDir   bool      `json:"isDir"`
}

// Root returns the workspace directory, creating it on first use.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户目录: %w", err)
	}
	root := filepath.Join(home, "EdgeKit", "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建工作区失败: %w", err)
	}
	return root, nil
}

// Resolve maps a workspace-relative path to an absolute path inside the root.
func Resolve(rel string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	clean := filepath.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	abs := filepath.Join(root, clean)
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越界")
	}
	return abs, nil
}

// Display returns the absolute path for the UI.
func Display(rel string) (string, error) {
	return Resolve(rel)
}

// List returns the entries of a workspace directory (directories first).
func List(rel string) ([]Entry, error) {
	abs, err := Resolve(rel)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:    info.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Mkdir creates a directory (and parents) inside the workspace.
func Mkdir(rel string) error {
	abs, err := Resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	return nil
}

// NewFile creates an empty file inside the workspace.
func NewFile(rel string) error {
	abs, err := Resolve(rel)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err == nil {
		return fmt.Errorf("文件已存在: %s", rel)
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	return f.Close()
}

// Write saves data to a file inside the workspace and returns its absolute path.
func Write(rel string, data []byte) (string, error) {
	abs, err := Resolve(rel)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}
	return abs, nil
}

// Delete removes a file or directory (recursively) inside the workspace.
func Delete(rel string) error {
	abs, err := Resolve(rel)
	if err != nil {
		return err
	}
	root, err := Root()
	if err != nil {
		return err
	}
	if abs == root {
		return fmt.Errorf("不能删除工作区根目录")
	}
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	return nil
}

// Read loads a file from the workspace.
func Read(rel string) ([]byte, error) {
	abs, err := Resolve(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}
	return data, nil
}
