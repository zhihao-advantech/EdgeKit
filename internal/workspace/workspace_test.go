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

func TestSimpleDiff(t *testing.T) {
	got := SimpleDiff("line1\nline2\nline3", "line1\nchanged\nline3")
	want := " line1\n-line2\n+changed\n line3"
	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Fatalf("SimpleDiff = %q, want %q", got, want)
	}
	if got := SimpleDiff("same", "same"); strings.TrimSpace(got) != "" {
		t.Fatalf("identical text should produce empty diff, got %q", got)
	}
}

func TestApplyPatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Write("mod.txt", []byte("old content")); err != nil {
		t.Fatal(err)
	}
	diff, err := ApplyPatch("mod.txt", "new content")
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if !strings.Contains(diff, "-old content") || !strings.Contains(diff, "+new content") {
		t.Fatalf("diff should show the change, got %q", diff)
	}
	data, err := Read("mod.txt")
	if err != nil || string(data) != "new content" {
		t.Fatalf("file should be updated, got %q", data)
	}
}

func TestApplyPatchSavesBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Write("src/mod.txt", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPatch("src/mod.txt", "modified"); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	backup, err := Read(".backup/src/mod.txt")
	if err != nil || string(backup) != "original" {
		t.Fatalf("backup = %q, err %v", backup, err)
	}
}

func TestRevertRestoresBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Write("revert.txt", []byte("before")); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPatch("revert.txt", "after"); err != nil {
		t.Fatal(err)
	}
	msg, err := Revert("revert.txt")
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if !strings.Contains(msg, "已回退") {
		t.Fatalf("unexpected revert message: %q", msg)
	}
	data, _ := Read("revert.txt")
	if string(data) != "before" {
		t.Fatalf("after revert = %q, want before", data)
	}
}

func TestRevertNoBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Revert("never-backed-up.txt"); err == nil {
		t.Fatal("revert without backup should fail")
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := Resolve("link/secret.txt"); err == nil {
		t.Fatal("a symlink escaping the workspace should be rejected")
	}
}

func TestWriteCreatesParentDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := Write("a/b/c/file.txt", []byte("x")); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	if data, err := Read("a/b/c/file.txt"); err != nil || string(data) != "x" {
		t.Fatalf("read nested: %v %q", err, data)
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
