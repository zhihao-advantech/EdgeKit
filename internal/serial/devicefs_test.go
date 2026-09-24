package serial

import (
	"bytes"
	"testing"
)

// TestCaptureFromSaturatedRing guards the regression where a full receive ring
// stopped growing len(recent), so a len-based slice returned nothing.
func TestCaptureFromSaturatedRing(t *testing.T) {
	m := &Manager{}
	m.recent = bytes.Repeat([]byte("a"), recentMax)
	m.rxTotal = 1000
	// Four new bytes arrived during a capture and sit at the tail of the ring.
	copy(m.recent[len(m.recent)-4:], []byte("XYZ\n"))
	m.rxTotal = 1004

	if got := m.captureFrom(1000); got != "XYZ\n" {
		t.Fatalf("captureFrom(1000) = %q, want %q", got, "XYZ\n")
	}
	if got := m.captureFrom(1004); got != "" {
		t.Fatalf("captureFrom(1004) = %q, want empty", got)
	}
	if got := m.captureFrom(0); len(got) != 1004 {
		t.Fatalf("captureFrom(0) len = %d, want 1004", len(got))
	}
}

func TestParseLS(t *testing.T) {
	out := "ls -la /\r\n" +
		"total 28\r\n" +
		"drwxr-xr-x    2 root     root          4096 Jan  1 00:00 bin\r\n" +
		"-rw-r--r--    1 root     root           123 Jan  1 00:00 config.txt\r\n" +
		"lrwxrwxrwx    1 root     root            11 Jan  1 00:00 lib -> usr/lib\r\n" +
		"drwxr-xr-x   18 root     root          4096 Sep 22 15:21 dev\r\n" +
		"user@board:/# "

	entries := ParseLS(out)
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}
	if !entries[0].IsDir || entries[0].Name != "bin" {
		t.Fatalf("bin entry wrong: %+v", entries[0])
	}
	if entries[1].IsDir || entries[1].Name != "config.txt" || entries[1].Size != 123 {
		t.Fatalf("config.txt entry wrong: %+v", entries[1])
	}
	if entries[2].Name != "lib" || entries[2].IsDir {
		t.Fatalf("symlink should be a file named lib: %+v", entries[2])
	}
	if !entries[3].IsDir || entries[3].Name != "dev" {
		t.Fatalf("dev entry wrong: %+v", entries[3])
	}
}

func TestParseLSIgnoresNoise(t *testing.T) {
	if got := ParseLS("prompt# \r\n\r\nWelcome to Linux\r\n"); len(got) != 0 {
		t.Fatalf("expected no entries, got %+v", got)
	}
}
