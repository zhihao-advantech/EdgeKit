package server

import (
	"sync"
	"testing"
	"time"
)

// TestBatcherCoalesces verifies that a burst of tiny chunks becomes one emit.
func TestBatcherCoalesces(t *testing.T) {
	var mu sync.Mutex
	var got []flushItem
	b := newStreamBatcher(func(ch, id, k string, d []byte, ts time.Time) {
		mu.Lock()
		got = append(got, flushItem{channel: ch, id: id, kind: k, data: d, ts: ts})
		mu.Unlock()
	})

	const n = 4096
	for i := 0; i < n; i++ {
		b.add("serial", "s1", "rx", []byte{byte(i)}, time.Now(), true)
	}
	b.flushAll()

	if len(got) != 1 {
		t.Fatalf("expected 1 coalesced emit, got %d", len(got))
	}
	if len(got[0].data) != n {
		t.Fatalf("expected %d bytes, got %d", n, len(got[0].data))
	}
	for i, v := range got[0].data {
		if v != byte(i) {
			t.Fatalf("byte %d out of order: %d", i, v)
		}
	}
}

// TestBatcherOrdering verifies that a non-data event flushes pending data first.
func TestBatcherOrdering(t *testing.T) {
	var mu sync.Mutex
	var kinds []string
	b := newStreamBatcher(func(ch, id, k string, d []byte, ts time.Time) {
		mu.Lock()
		kinds = append(kinds, ch+":"+id+":"+k)
		mu.Unlock()
	})

	b.add("serial", "s1", "rx", []byte("abc"), time.Now(), true)
	b.add("serial", "s1", "info", []byte("log"), time.Now(), false)
	b.add("serial", "s1", "rx", []byte("def"), time.Now(), true)
	b.flushAll()

	want := []string{"serial:s1:rx", "serial:s1:info", "serial:s1:rx"}
	if len(kinds) != len(want) {
		t.Fatalf("expected %v, got %v", want, kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, kinds)
		}
	}
}

// BenchmarkBatcherThroughput reports how many WS emits a 1-byte-per-read
// serial stream produces with and without batching.
func BenchmarkBatcherThroughput(b *testing.B) {
	const events = 11520 // ~1s of 115200 baud delivered byte by byte

	b.Run("unbatched", func(b *testing.B) {
		count := 0
		em := func(channel, id, kind string, data []byte, ts time.Time) { count++ }
		for i := 0; i < b.N; i++ {
			count = 0
			for j := 0; j < events; j++ {
				em("serial", "s1", "rx", []byte{'x'}, time.Now())
			}
		}
		b.ReportMetric(float64(count), "emits/op")
	})

	b.Run("batched", func(b *testing.B) {
		count := 0
		bat := newStreamBatcher(func(channel, id, kind string, data []byte, ts time.Time) { count++ })
		for i := 0; i < b.N; i++ {
			count = 0
			bat.flushAll()
			for j := 0; j < events; j++ {
				bat.add("serial", "s1", "rx", []byte{'x'}, time.Now(), true)
			}
			bat.flushAll()
		}
		b.ReportMetric(float64(count), "emits/op")
	})
}
