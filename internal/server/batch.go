package server

import (
	"sync"
	"time"
)

// Batching parameters. A short window keeps latency imperceptible while
// collapsing a burst of tiny reads (a serial port often delivers 1 byte at a
// time) into a single WebSocket message.
const (
	batchInterval = 12 * time.Millisecond
	batchMaxBytes = 32 * 1024
)

type flushItem struct {
	channel string
	id      string
	kind    string
	data    []byte
	ts      time.Time
}

type batchPart struct {
	channel string
	id      string
	kind    string
	buf     []byte
	ts      time.Time
	timer   *time.Timer
}

// streamBatcher coalesces high-rate data chunks per (channel, session, kind)
// and emits them as one message. Non-data events flush pending data on the same
// channel+session first so ordering is preserved.
type streamBatcher struct {
	mu    sync.Mutex
	parts map[string]*batchPart
	emit  func(channel, id, kind string, data []byte, ts time.Time)
}

func newStreamBatcher(emit func(channel, id, kind string, data []byte, ts time.Time)) *streamBatcher {
	return &streamBatcher{parts: make(map[string]*batchPart), emit: emit}
}

// add queues a chunk. batchable=false means "emit now" (after flushing the
// channel's pending data so the relative order is kept).
func (b *streamBatcher) add(channel, id, kind string, data []byte, ts time.Time, batchable bool) {
	if !batchable || len(data) == 0 {
		b.mu.Lock()
		var pending []*flushItem
		for key, p := range b.parts {
			if p.channel == channel && p.id == id {
				if it := b.takeLocked(key, p); it != nil {
					pending = append(pending, it)
				}
			}
		}
		b.mu.Unlock()
		for _, it := range pending {
			b.emit(it.channel, it.id, it.kind, it.data, it.ts)
		}
		if len(data) > 0 {
			b.emit(channel, id, kind, data, ts)
		}
		return
	}

	key := channel + "\x00" + id + "\x00" + kind
	b.mu.Lock()
	p := b.parts[key]
	if p == nil {
		p = &batchPart{channel: channel, id: id, kind: kind}
		b.parts[key] = p
	}
	if len(p.buf) == 0 {
		p.ts = ts
	}
	p.buf = append(p.buf, data...)

	var out *flushItem
	if len(p.buf) >= batchMaxBytes {
		out = b.takeLocked(key, p)
	} else if p.timer == nil {
		p.timer = time.AfterFunc(batchInterval, func() { b.flushKey(key) })
	}
	b.mu.Unlock()

	if out != nil {
		b.emit(out.channel, out.id, out.kind, out.data, out.ts)
	}
}

func (b *streamBatcher) flushKey(key string) {
	b.mu.Lock()
	var it *flushItem
	if p := b.parts[key]; p != nil {
		it = b.takeLocked(key, p)
	}
	b.mu.Unlock()
	if it != nil {
		b.emit(it.channel, it.id, it.kind, it.data, it.ts)
	}
}

// flushAll drains every pending batch (used on shutdown).
func (b *streamBatcher) flushAll() {
	b.mu.Lock()
	var items []*flushItem
	for key, p := range b.parts {
		if it := b.takeLocked(key, p); it != nil {
			items = append(items, it)
		}
	}
	b.mu.Unlock()
	for _, it := range items {
		b.emit(it.channel, it.id, it.kind, it.data, it.ts)
	}
}

// takeLocked removes a part from the map and returns its buffered data.
// Caller must hold b.mu.
func (b *streamBatcher) takeLocked(key string, p *batchPart) *flushItem {
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	delete(b.parts, key)
	if len(p.buf) == 0 {
		return nil
	}
	return &flushItem{channel: p.channel, id: p.id, kind: p.kind, data: p.buf, ts: p.ts}
}
