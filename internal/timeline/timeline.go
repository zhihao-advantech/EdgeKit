// Package timeline is EdgeKit's device record: an append-only, timestamped log
// of everything observed or done on a device session.
//
// It sits between the providers (which emit raw events) and the consumers (UI,
// built-in agent, future MCP server), so history, replay and cross-channel
// correlation all read the same source instead of each keeping its own buffer.
package timeline

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"
)

// Channel values.
const (
	ChannelSerial = "serial"
	ChannelSSH    = "ssh"
	ChannelAgent  = "agent" // tool calls / decisions (audit)
	ChannelHost   = "host"  // local machine
)

// ErrTimeout is returned by Wait when no matching record arrives in time.
var ErrTimeout = errors.New("等待超时")

// Record is one observation or action.
type Record struct {
	Seq     uint64    `json:"seq"`
	Time    time.Time `json:"time"`
	Channel string    `json:"channel"`
	Kind    string    `json:"kind"` // rx | tx | stdout | stderr | info | error | action
	Data    []byte    `json:"data"`
}

// Filter selects records. A zero Filter matches everything.
type Filter struct {
	Channel  string
	Kind     string
	AfterSeq uint64
	Pattern  *regexp.Regexp // matched against Data
}

// Match reports whether r passes the filter.
func (f Filter) Match(r Record) bool {
	if f.Channel != "" && r.Channel != f.Channel {
		return false
	}
	if f.Kind != "" && r.Kind != f.Kind {
		return false
	}
	if f.Pattern != nil && !f.Pattern.Match(r.Data) {
		return false
	}
	return true
}

// Timeline is a bounded, append-only record log. It keeps a fixed-size ring so
// appending never has to shift the whole buffer.
type Timeline struct {
	mu      sync.Mutex
	records []Record // fixed length == max; used as a ring
	head    int      // index of the oldest record
	count   int      // number of valid records (<= max)
	max     int
	seq     uint64
	update  chan struct{}
	waiters int
}

// New creates a timeline keeping at most max records (default 5000).
func New(max int) *Timeline {
	if max <= 0 {
		max = 5000
	}
	return &Timeline{max: max, records: make([]Record, max), update: make(chan struct{})}
}

// atLocked returns the i-th record (0 == oldest). Caller holds t.mu.
func (t *Timeline) atLocked(i int) Record {
	return t.records[(t.head+i)%t.max]
}

// Append stores a record and stamps it with the next sequence number. It
// returns the stored record (with Seq and Time filled in).
func (t *Timeline) Append(r Record) Record {
	t.mu.Lock()
	t.seq++
	r.Seq = t.seq
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	if r.Data != nil {
		data := make([]byte, len(r.Data))
		copy(data, r.Data)
		r.Data = data
	}
	if t.count < t.max {
		t.records[(t.head+t.count)%t.max] = r
		t.count++
	} else {
		t.records[t.head] = r
		t.head = (t.head + 1) % t.max
	}
	// Wake Wait callers, but only when someone is actually waiting (an append
	// with no waiter must not allocate a channel).
	if t.waiters > 0 {
		close(t.update)
		t.update = make(chan struct{})
	}
	t.mu.Unlock()
	return r
}

// LastSeq returns the sequence number of the newest record.
func (t *Timeline) LastSeq() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seq
}

// Len returns the number of buffered records.
func (t *Timeline) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// Since returns the records after the given sequence, oldest first. limit <= 0
// means "no limit"; when more than limit records qualify the newest are kept.
func (t *Timeline) Since(after uint64, limit int) []Record {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Walk from the newest backwards, stopping as soon as we have enough or hit
	// the mark, so only the records we return are cloned.
	capHint := t.count
	if limit > 0 && limit < capHint {
		capHint = limit
	}
	out := make([]Record, 0, capHint)
	for i := t.count - 1; i >= 0; i-- {
		r := t.atLocked(i)
		if r.Seq <= after {
			break
		}
		out = append(out, clone(r))
		if limit > 0 && len(out) == limit {
			break
		}
	}
	// Reverse into oldest-first order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Wait blocks until a record matching f arrives (only records newer than the
// sequence at call time are considered), the timeout elapses, or ctx is done.
func (t *Timeline) Wait(ctx context.Context, f Filter, timeout time.Duration) (Record, error) {
	if f.AfterSeq == 0 {
		f.AfterSeq = t.LastSeq()
	}
	deadline := time.Now().Add(timeout)
	for {
		t.mu.Lock()
		if r, ok := t.findLocked(f); ok {
			t.mu.Unlock()
			return r, nil
		}
		ch := t.update
		t.waiters++
		t.mu.Unlock()

		remain := time.Until(deadline)
		if remain <= 0 {
			t.removeWaiter()
			return Record{}, ErrTimeout
		}
		timer := time.NewTimer(remain)
		select {
		case <-ch:
			timer.Stop()
		case <-timer.C:
			t.removeWaiter()
			return Record{}, ErrTimeout
		case <-ctx.Done():
			timer.Stop()
			t.removeWaiter()
			return Record{}, ctx.Err()
		}
		t.removeWaiter()
	}
}

func (t *Timeline) removeWaiter() {
	t.mu.Lock()
	if t.waiters > 0 {
		t.waiters--
	}
	t.mu.Unlock()
}

// find returns the first record after f.AfterSeq matching f.
func (t *Timeline) find(f Filter) (Record, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.findLocked(f)
}

func (t *Timeline) findLocked(f Filter) (Record, bool) {
	for i := 0; i < t.count; i++ {
		r := t.atLocked(i)
		if r.Seq <= f.AfterSeq {
			continue
		}
		if f.Match(r) {
			return clone(r), true
		}
	}
	return Record{}, false
}

func clone(r Record) Record {
	if r.Data != nil {
		data := make([]byte, len(r.Data))
		copy(data, r.Data)
		r.Data = data
	}
	return r
}
