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

func (f Filter) match(r Record) bool {
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

// Timeline is a bounded, append-only record log.
type Timeline struct {
	mu      sync.Mutex
	records []Record
	max     int
	seq     uint64
	update  chan struct{}
}

// New creates a timeline keeping at most max records (default 5000).
func New(max int) *Timeline {
	if max <= 0 {
		max = 5000
	}
	return &Timeline{max: max, update: make(chan struct{})}
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
	t.records = append(t.records, r)
	if len(t.records) > t.max {
		drop := len(t.records) - t.max
		n := copy(t.records, t.records[drop:])
		t.records = t.records[:n]
	}
	// Wake any Wait callers: close the current channel and install a new one.
	close(t.update)
	t.update = make(chan struct{})
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
	return len(t.records)
}

// Since returns the records after the given sequence, oldest first. limit <= 0
// means "no limit"; when more than limit records qualify the newest are kept.
func (t *Timeline) Since(after uint64, limit int) []Record {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Record, 0, len(t.records))
	for _, r := range t.records {
		if r.Seq <= after {
			continue
		}
		out = append(out, clone(r))
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
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
		if r, ok := t.find(f); ok {
			return r, nil
		}
		t.mu.Lock()
		ch := t.update
		t.mu.Unlock()

		remain := time.Until(deadline)
		if remain <= 0 {
			return Record{}, ErrTimeout
		}
		timer := time.NewTimer(remain)
		select {
		case <-ch:
			timer.Stop()
		case <-timer.C:
			return Record{}, ErrTimeout
		case <-ctx.Done():
			timer.Stop()
			return Record{}, ctx.Err()
		}
	}
}

// find returns the first record after f.AfterSeq matching f.
func (t *Timeline) find(f Filter) (Record, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.records {
		if r.Seq <= f.AfterSeq {
			continue
		}
		if f.match(r) {
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
