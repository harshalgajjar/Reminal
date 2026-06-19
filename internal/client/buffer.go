package client

import "sync"

// scrollback holds encrypted terminal output keyed by a monotonically
// increasing sequence number. The newest entries are kept; old entries are
// dropped once total ciphertext bytes exceed maxBytes, so a reconnecting
// viewer can replay everything it missed (up to the buffer size).
type scrollback struct {
	mu       sync.Mutex
	entries  []scrollEntry
	nextSeq  uint64
	bytes    int
	maxBytes int
	notify   chan struct{}
}

type scrollEntry struct {
	Seq  uint64
	Data string // base64 ciphertext, ready to send as-is
}

func newScrollback(maxBytes int) *scrollback {
	return &scrollback{
		maxBytes: maxBytes,
		notify:   make(chan struct{}, 1),
	}
}

// Append records a new chunk and returns its assigned sequence number.
// Older entries are evicted until total bytes fit under maxBytes.
func (s *scrollback) Append(data string) uint64 {
	s.mu.Lock()
	s.nextSeq++
	seq := s.nextSeq
	s.entries = append(s.entries, scrollEntry{Seq: seq, Data: data})
	s.bytes += len(data)
	for s.bytes > s.maxBytes && len(s.entries) > 1 {
		s.bytes -= len(s.entries[0].Data)
		s.entries = s.entries[1:]
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return seq
}

// From returns a copy of entries with Seq >= fromSeq.
func (s *scrollback) From(fromSeq uint64) []scrollEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]scrollEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Seq >= fromSeq {
			out = append(out, e)
		}
	}
	return out
}

// Notify returns a channel that receives a struct{} whenever Append happens.
// It is buffered to depth 1 and edge-triggered: a sender that misses a tick
// just loops back and reads the buffer again, so no data is lost.
func (s *scrollback) Notify() <-chan struct{} {
	return s.notify
}
