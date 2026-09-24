package serial

import (
	"bytes"
	"testing"
)

// TestCaptureFromSaturatedRing guards the regression where a full receive ring
// stopped growing, so a len-based slice returned nothing.
func TestCaptureFromSaturatedRing(t *testing.T) {
	m := &Manager{recent: make([]byte, recentMax), recentN: recentMax, recentW: 4, rxTotal: 1004}
	// The newest four bytes sit at indices 0..3 (the write cursor wrapped to 4).
	copy(m.recent[0:4], []byte("XYZ\n"))

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

// TestRingAppendWraps checks the ring keeps the newest recentMax bytes in order
// once it has wrapped several times.
func TestRingAppendWraps(t *testing.T) {
	m := &Manager{recent: make([]byte, recentMax)}
	total := recentMax + 3
	for i := 0; i < total; i++ {
		m.appendRecent([]byte{byte(i & 0xff)})
	}
	got := m.Recent()
	if len(got) != recentMax {
		t.Fatalf("Recent len = %d, want %d", len(got), recentMax)
	}
	if got[0] != byte((total-recentMax)&0xff) {
		t.Fatalf("oldest byte = %d, want %d", got[0], byte((total-recentMax)&0xff))
	}
	if got[len(got)-1] != byte((total-1)&0xff) {
		t.Fatalf("newest byte = %d, want %d", got[len(got)-1], byte((total-1)&0xff))
	}
	if m.rxTotal != uint64(total) {
		t.Fatalf("rxTotal = %d, want %d", m.rxTotal, total)
	}
}

// TestRingAppendChunkWraps checks a multi-byte chunk that crosses the end of the
// ring is stored contiguously on read.
func TestRingAppendChunkWraps(t *testing.T) {
	m := &Manager{recent: make([]byte, recentMax)}
	m.appendRecent(bytes.Repeat([]byte{'a'}, recentMax-2))
	m.appendRecent([]byte("HELLO"))
	got := m.Recent()
	if len(got) != recentMax {
		t.Fatalf("Recent len = %d, want %d", len(got), recentMax)
	}
	if string(got[len(got)-5:]) != "HELLO" {
		t.Fatalf("tail = %q, want HELLO", got[len(got)-5:])
	}
}

// BenchmarkAppendRecentSaturated measures the per-chunk cost once the receive
// ring is full (the previous implementation memmoved 64 KiB every chunk).
func BenchmarkAppendRecentSaturated(b *testing.B) {
	m := &Manager{recent: make([]byte, recentMax), recentN: recentMax}
	one := []byte{'x'}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.appendRecent(one)
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
