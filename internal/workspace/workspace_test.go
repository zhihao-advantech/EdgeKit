package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveStaysInRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../../etc/passwd", "/../secret", "a/../../b", ".."} {
		abs, err := Resolve(rel)
		if err != nil {
			continue // clamping is also acceptable
		}
		if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			t.Fatalf("path %q escaped the workspace: %s", rel, abs)
		}
	}
}

func TestWorkspaceRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := Mkdir("firmware/v1"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := NewFile("firmware/v1/note.txt"); err != nil {
		t.Fatalf("newfile: %v", err)
	}
	abs, err := Write("firmware/v1/config.json", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if filepath.Base(abs) != "config.json" {
		t.Fatalf("unexpected path: %s", abs)
	}
	data, err := Read("firmware/v1/config.json")
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("read: %v %q", err, data)
	}

	list, err := List("firmware/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %+v", list)
	}
	// directories sort first, then names
	if list[0].Name != "config.json" || list[1].Name != "note.txt" {
		t.Fatalf("unexpected order: %+v", list)
	}
}
