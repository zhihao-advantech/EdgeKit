package timeline

import (
	"context"
	"regexp"
	"testing"
	"time"
)

func TestAppendAssignsSeqAndClones(t *testing.T) {
	tl := New(4)
	src := []byte("hello")
	r := tl.Append(Record{Channel: ChannelSerial, Kind: "rx", Data: src})
	if r.Seq != 1 {
		t.Fatalf("seq = %d, want 1", r.Seq)
	}
	src[0] = 'X' // the stored record must not alias the caller's slice
	got := tl.Since(0, 0)
	if len(got) != 1 || string(got[0].Data) != "hello" {
		t.Fatalf("record aliased caller data: %q", got[0].Data)
	}
}

func TestBoundedRetention(t *testing.T) {
	tl := New(3)
	for i := 0; i < 5; i++ {
		tl.Append(Record{Channel: ChannelSerial, Kind: "rx", Data: []byte{byte('0' + i)}})
	}
	got := tl.Since(0, 0)
	if len(got) != 3 {
		t.Fatalf("kept %d records, want 3", len(got))
	}
	if got[0].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("wrong window: %d..%d", got[0].Seq, got[2].Seq)
	}
}

func TestSinceAndLimit(t *testing.T) {
	tl := New(100)
	for i := 0; i < 10; i++ {
		tl.Append(Record{Channel: ChannelSSH, Kind: "stdout", Data: []byte("x")})
	}
	if got := tl.Since(7, 0); len(got) != 3 {
		t.Fatalf("since=7 -> %d records, want 3", len(got))
	}
	got := tl.Since(0, 4)
	if len(got) != 4 || got[0].Seq != 7 {
		t.Fatalf("limit should keep the newest 4, got %d starting at %d", len(got), got[0].Seq)
	}
}

func TestWaitFindsMatchingRecord(t *testing.T) {
	tl := New(100)
	go func() {
		time.Sleep(30 * time.Millisecond)
		tl.Append(Record{Channel: ChannelSerial, Kind: "rx", Data: []byte("booting...")})
		time.Sleep(20 * time.Millisecond)
		tl.Append(Record{Channel: ChannelSerial, Kind: "rx", Data: []byte("login:")})
	}()

	re := regexp.MustCompile(`login:`)
	rec, err := tl.Wait(context.Background(), Filter{Pattern: re}, 2*time.Second)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if string(rec.Data) != "login:" {
		t.Fatalf("matched %q", rec.Data)
	}
}

func TestWaitTimesOut(t *testing.T) {
	tl := New(10)
	start := time.Now()
	if _, err := tl.Wait(context.Background(), Filter{Pattern: regexp.MustCompile("nope")}, 80*time.Millisecond); err != ErrTimeout {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("returned too early: %v", elapsed)
	}
}

func TestWaitIsCancellable(t *testing.T) {
	tl := New(10)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := tl.Wait(ctx, Filter{}, 2*time.Second); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestWaitIgnoresHistoryBeforeCall(t *testing.T) {
	tl := New(10)
	tl.Append(Record{Channel: ChannelSerial, Kind: "rx", Data: []byte("old login:")})
	if _, err := tl.Wait(context.Background(), Filter{Pattern: regexp.MustCompile("login:")}, 80*time.Millisecond); err != ErrTimeout {
		t.Fatalf("history should not satisfy Wait, got %v", err)
	}
}
