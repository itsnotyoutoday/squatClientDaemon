package scrollback

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Defaults.
const (
	DefaultRingSize     = 50000
	DefaultSyncInterval = time.Second
	LogName             = "log.ndjson"
	indexEvery          = 256 // sparse offset index granularity (entries)
)

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("scrollback: closed")

// Options configures a Store.
type Options struct {
	RingSize     int           // in-memory entries (default 50000)
	LogDir       string        // directory for log.ndjson; empty = memory only
	SyncInterval time.Duration // fsync batching period (default 1s)
}

// Gap says how many entries a replaying client missed.
type Gap struct {
	Missed uint64 `json:"missed"`
}

// Replay is the answer to an ATTACH request (docs/PROTOCOL.md 5.1).
type Replay struct {
	FirstSeq   uint64  // oldest retained seq (0 if empty)
	LastSeq    uint64  // newest seq (0 if empty)
	ReplayFrom uint64  // first seq in Entries (0 if none)
	Gap        *Gap    // non-nil when entries were lost between the client's position and ReplayFrom
	Entries    []Entry // ascending seq
}

// Store is a seq-stamped entry store. Safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	opts Options

	ring  []Entry
	last  uint64 // newest seq
	first uint64 // oldest seq retained anywhere (disk if any, else ring); 0 if empty

	path   string
	f      *os.File
	size   int64   // current log size = offset of next append
	index  []int64 // offset of entry with seq = first + k*indexEvery
	dirty  bool
	closed bool

	stop chan struct{}
	wg   sync.WaitGroup
}

// New opens a Store, loading any existing log in LogDir.
func New(opts Options) (*Store, error) {
	if opts.RingSize <= 0 {
		opts.RingSize = DefaultRingSize
	}
	if opts.SyncInterval <= 0 {
		opts.SyncInterval = DefaultSyncInterval
	}
	s := &Store{opts: opts, ring: make([]Entry, opts.RingSize), stop: make(chan struct{})}
	if opts.LogDir != "" {
		if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
			return nil, fmt.Errorf("scrollback: %w", err)
		}
		s.path = filepath.Join(opts.LogDir, LogName)
		if err := s.load(); err != nil {
			return nil, err
		}
		s.wg.Add(1)
		go s.syncLoop()
	}
	return s, nil
}

func maxu(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func (s *Store) slot(seq uint64) *Entry { return &s.ring[seq%uint64(len(s.ring))] }

// load reads an existing log: restores seqs, fills the ring and the offset
// index, and truncates a torn/corrupt tail.
func (s *Store) load() error {
	f, err := os.OpenFile(s.path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("scrollback: %w", err)
	}
	r := bufio.NewReaderSize(f, 1<<20)
	var off int64
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) == 0 && rerr != nil {
			break
		}
		var e Entry
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if !complete || json.Unmarshal(bytes.TrimSpace(line), &e) != nil || e.Seq() == 0 ||
			(s.last != 0 && e.Seq() != s.last+1) {
			slog.Warn("scrollback: truncating bad log tail", "path", s.path, "offset", off)
			if err := f.Truncate(off); err != nil {
				f.Close()
				return fmt.Errorf("scrollback: %w", err)
			}
			break
		}
		if s.first == 0 {
			s.first = e.Seq()
		}
		if (e.Seq()-s.first)%indexEvery == 0 {
			s.index = append(s.index, off)
		}
		*s.slot(e.Seq()) = e
		s.last = e.Seq()
		off += int64(len(line))
		if rerr != nil {
			break
		}
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		f.Close()
		return fmt.Errorf("scrollback: %w", err)
	}
	s.f, s.size = f, off
	return nil
}

func (s *Store) syncLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.opts.SyncInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.Sync()
		case <-s.stop:
			return
		}
	}
}

// Sync fsyncs the log if anything was appended since the last sync.
func (s *Store) Sync() error {
	s.mu.Lock()
	if s.f == nil || !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.dirty = false
	f := s.f
	s.mu.Unlock()
	return f.Sync()
}

// Close syncs and closes the log and stops the background syncer. Reads of the
// in-memory ring keep working; Append fails afterwards.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	f := s.f
	s.mu.Unlock()
	s.wg.Wait()
	if f == nil {
		return nil
	}
	err := f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	s.mu.Lock()
	s.f = nil
	s.mu.Unlock()
	return err
}

// Append stamps e with the next sequence number, stores it and returns the
// seq. The caller supplies session id and timestamp; Seq/V/T fields of e are
// overwritten. e is not modified.
func (s *Store) Append(e Entry) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	seq := s.last + 1
	stamped, err := e.withSeq(seq)
	if err != nil {
		return 0, err
	}
	if s.f != nil {
		b, err := json.Marshal(stamped)
		if err != nil {
			return 0, fmt.Errorf("scrollback: %w", err)
		}
		b = append(b, '\n')
		if n, err := s.f.Write(b); err != nil {
			if n > 0 { // don't leave a torn line behind
				_ = s.f.Truncate(s.size)
				_, _ = s.f.Seek(s.size, io.SeekStart)
			}
			return 0, fmt.Errorf("scrollback: %w", err)
		}
		if s.first == 0 {
			s.first = seq
		}
		if (seq-s.first)%indexEvery == 0 {
			s.index = append(s.index, s.size)
		}
		s.size += int64(len(b))
		s.dirty = true
	} else if s.first == 0 {
		s.first = seq
	}
	*s.slot(seq) = stamped
	s.last = seq
	return seq, nil
}

// Bounds returns the oldest retained and the newest seq (0,0 when empty).
func (s *Store) Bounds() (first, last uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.firstLocked(), s.last
}

func (s *Store) firstLocked() uint64 {
	if s.last == 0 {
		return 0
	}
	if s.path != "" {
		return s.first
	}
	return s.ringFirstMem()
}

// ringFirstMem is the oldest seq present in the ring.
func (s *Store) ringFirstMem() uint64 {
	n := uint64(len(s.ring))
	lo := uint64(1)
	if s.last > n {
		lo = s.last - n + 1
	}
	return maxu(lo, s.first)
}

// Range returns entries with from <= seq <= to in ascending order, clipped to
// what is retained. It reads from disk when the ring no longer holds from.
func (s *Store) Range(from, to uint64) ([]Entry, error) {
	var out []Entry
	err := s.iterate(from, to, func(e Entry) bool { out = append(out, e); return true })
	return out, err
}

// Since returns up to max entries with seq > since (max <= 0: all).
func (s *Store) Since(since uint64, max int) ([]Entry, error) {
	first, last := s.Bounds()
	if last == 0 || since >= last {
		return nil, nil
	}
	from := maxu(since+1, first)
	to := last
	if max > 0 && to-from+1 > uint64(max) {
		to = from + uint64(max) - 1
	}
	return s.Range(from, to)
}

// Last returns the newest n entries in ascending order.
func (s *Store) Last(n int) ([]Entry, error) {
	first, last := s.Bounds()
	if last == 0 || n <= 0 {
		return nil, nil
	}
	from := first
	if uint64(n) < last-first+1 {
		from = last - uint64(n) + 1
	}
	return s.Range(from, last)
}

// Replay implements the attach rules of docs/PROTOCOL.md 5.1. hasSince=false
// means the client has nothing. maxBacklog caps the replay (default 2000).
func (s *Store) Replay(since uint64, hasSince bool, maxBacklog int) (Replay, error) {
	if maxBacklog <= 0 {
		maxBacklog = 2000
	}
	first, last := s.Bounds()
	rp := Replay{FirstSeq: first, LastSeq: last}
	if last == 0 {
		return rp, nil
	}
	lastN := func() uint64 { // first seq of the newest maxBacklog entries
		if uint64(maxBacklog) >= last-first+1 {
			return first
		}
		return last - uint64(maxBacklog) + 1
	}
	var from uint64
	switch {
	case !hasSince || since > last:
		from = lastN()
	case since+1 >= first:
		from = since + 1
		if last-from+1 > uint64(maxBacklog) {
			from = lastN()
			rp.Gap = &Gap{Missed: from - since - 1}
		}
	default: // fell out of retention
		from = lastN()
		rp.Gap = &Gap{Missed: from - since - 1}
	}
	if from > last { // since == last: nothing to replay
		return rp, nil
	}
	ents, err := s.Range(from, last)
	if err != nil {
		return rp, err
	}
	if len(ents) > 0 {
		rp.ReplayFrom = ents[0].Seq()
	}
	rp.Entries = ents
	return rp, nil
}

// iterate calls fn for each retained entry in [from,to] ascending until fn
// returns false.
func (s *Store) iterate(from, to uint64, fn func(Entry) bool) error {
	s.mu.RLock()
	first, last := s.firstLocked(), s.last
	if from < first {
		from = first
	}
	if to > last {
		to = last
	}
	if last == 0 || from > to {
		s.mu.RUnlock()
		return nil
	}
	rf := s.ringFirstMem()
	// ring part
	ringFrom := maxu(from, rf)
	var ringEnts []Entry
	if ringFrom <= to {
		ringEnts = make([]Entry, 0, to-ringFrom+1)
		for q := ringFrom; q <= to; q++ {
			ringEnts = append(ringEnts, *s.slot(q))
		}
	}
	var diskTo uint64
	var startOff int64
	needDisk := from < rf && s.path != ""
	if needDisk {
		diskTo = rf - 1
		if diskTo > to {
			diskTo = to
		}
		startOff = s.index[(from-s.first)/indexEvery]
	}
	path := s.path
	s.mu.RUnlock()

	if needDisk {
		if err := readDisk(path, startOff, from, diskTo, fn); err != nil {
			return err
		}
		if diskTo >= to {
			return nil
		}
	}
	for _, e := range ringEnts {
		if !fn(e) {
			return nil
		}
	}
	return nil
}

func readDisk(path string, off int64, from, to uint64, fn func(Entry) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("scrollback: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("scrollback: %w", err)
	}
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var e Entry
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				return fmt.Errorf("scrollback: corrupt log: %w", jerr)
			}
			switch q := e.Seq(); {
			case q > to:
				return nil
			case q >= from:
				if !fn(e) {
					return nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("scrollback: %w", err)
		}
	}
}
