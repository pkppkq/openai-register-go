package logs

import (
	"sync"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// Record is one stored log line. It is the LogRecord dataclass of app.py:1342
// plus the Level/Module the Tk side recomputed on every widget insert; the
// JSON shape is the `log` event payload of UI_SPEC §4.2.
type Record struct {
	Seq      int    `json:"seq"`
	TimeText string `json:"ts"`
	Message  string `json:"message"`
	Email    string `json:"email,omitempty"`
	Scope    string `json:"scope"`
	Level    Level  `json:"level"`
	Module   string `json:"module"`
}

// Scope values of app.py:18466.
const (
	ScopeGlobal  = "global"
	ScopeAccount = "account"
)

// Entry is one pending (message, email) pair, i.e. an element of the log_batch
// list built by the drain loop (app.py:18601).
type Entry struct {
	Message string
	Email   string
}

// ToModel converts to the shared record type. models.LogRecord has no
// level/module fields, so those are dropped — use Record on the event path.
func (r Record) ToModel() models.LogRecord {
	return models.LogRecord{
		Seq:      r.Seq,
		TimeText: r.TimeText,
		Message:  r.Message,
		Email:    r.Email,
		Scope:    r.Scope,
	}
}

// FormatLine is _format_log_record (app.py:18575-18576), trailing newline included.
func FormatLine(r Record) string {
	return "[" + r.TimeText + "] " + r.Message + "\n"
}

// ring is a fixed-capacity FIFO of records. The Python did
// `del records[:len(records)-LIMIT]` on every overflow (app.py:18473); a ring
// gives the same visible window with O(1) appends, which matters because every
// worker goroutine writes here.
type ring struct {
	buf   []Record
	start int
	max   int
}

func newRing(max int) *ring {
	// Deliberately NOT preallocated: there is one ring per account email and
	// logs_by_email is never pruned (app.py:18471), so 2000 slots per seen
	// address up front would be tens of MB for a large account list.
	return &ring{max: max}
}

func (r *ring) push(rec Record) {
	if len(r.buf) < r.max {
		r.buf = append(r.buf, rec) // still filling; start is 0 by construction
		return
	}
	r.buf[r.start] = rec
	r.start = (r.start + 1) % r.max
}

func (r *ring) len() int { return len(r.buf) }

func (r *ring) snapshot() []Record {
	out := make([]Record, len(r.buf))
	if r.start == 0 {
		copy(out, r.buf)
		return out
	}
	n := copy(out, r.buf[r.start:])
	copy(out[n:], r.buf[:r.start])
	return out
}

// Store is the log model: the three views the Tk app kept (log_records,
// global_logs, logs_by_email) behind one mutex, written by every worker
// goroutine and read by the UI.
type Store struct {
	mu       sync.RWMutex
	seq      int
	all      *ring
	global   *ring
	byEmail  map[string]*ring
	selected string
	now      func() time.Time
}

// NewStore builds an empty store with the app.py:58-59 bounds.
func NewStore() *Store {
	return &Store{
		all:     newRing(MaxTotalLogRecords),
		global:  newRing(MaxLogRecordsPerView),
		byEmail: make(map[string]*ring),
		now:     time.Now,
	}
}

// SetNowFunc overrides the clock. Tests only.
func (s *Store) SetNowFunc(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f == nil {
		f = time.Now
	}
	s.now = f
}

// SetSelected sets the account whose pane is on screen (_selected_log_email_key,
// app.py:18411); "" means no account is selected.
func (s *Store) SetSelected(email string) {
	key := EmailKey(email)
	s.mu.Lock()
	s.selected = key
	s.mu.Unlock()
}

// Selected returns the current per-account pane key.
func (s *Store) Selected() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selected
}

// Append classifies, routes and stores one line, returning the stored record.
//
// Python trap (app.py:18516 vs 18443): the queue path inferred the address from
// a leading "[a@b]" bracket, but App.log() called on the Tk thread went straight
// to _append_log_record and skipped that inference, so the same line could land
// in a different pane depending on which thread produced it. Every log now
// crosses the event path, so this port always infers — the queue behaviour wins.
func (s *Store) Append(message, email string) Record {
	key := Route(message, email)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store(message, key)
}

// AppendBatch is _append_log_records (app.py:18523): one lock acquisition for a
// whole drain tick, records returned in arrival order.
func (s *Store) AppendBatch(entries []Entry) []Record {
	if len(entries) == 0 {
		return nil
	}
	out := make([]Record, 0, len(entries))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		out = append(out, s.store(e.Message, Route(e.Message, e.Email)))
	}
	return out
}

// store is _store_log_record (app.py:18458-18483). Caller holds the write lock.
func (s *Store) store(message, emailKey string) Record {
	text, module := Classify(message)
	s.seq++
	scope := ScopeGlobal
	if emailKey != "" {
		scope = ScopeAccount
	}
	rec := Record{
		Seq:      s.seq,
		TimeText: s.now().Format("15:04:05"), // datetime.now().strftime("%H:%M:%S"), local time
		Message:  text,
		Email:    emailKey,
		Scope:    scope,
		Level:    Tag(text),
		Module:   module,
	}
	// The total buffer and the per-view buffers are independent copies, exactly
	// as in Python: a record evicted from log_records at 10000 stays visible in
	// its 2000-entry view (app.py:18479 never touches the view lists).
	s.all.push(rec)
	if emailKey != "" {
		view, ok := s.byEmail[emailKey]
		if !ok {
			view = newRing(MaxLogRecordsPerView)
			s.byEmail[emailKey] = view
		}
		view.push(rec)
	} else {
		s.global.push(rec)
	}
	return rec
}

// Visible is the render gate of app.py:18481-18483: global lines always show,
// account lines only when their account is the selected one (so with no
// selection at all, account lines are stored but not drawn).
func (s *Store) Visible(r Record) bool {
	if r.Email == "" {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selected != "" && s.selected == r.Email
}

// SplitVisible partitions records the way _append_log_records feeds its two
// widgets (app.py:18527-18532): account pane first, global pane second.
func (s *Store) SplitVisible(records []Record) (account, global []Record) {
	s.mu.RLock()
	selected := s.selected
	s.mu.RUnlock()
	for _, r := range records {
		if r.Email == "" {
			global = append(global, r)
			continue
		}
		if selected != "" && selected == r.Email {
			account = append(account, r)
		}
	}
	return account, global
}

// AccountRecords is the per-account pane buffer (_render_log_view, app.py:18587).
// Unknown address returns nil, matching logs_by_email.get(key, []).
func (s *Store) AccountRecords(email string) []Record {
	key := EmailKey(email)
	s.mu.RLock()
	defer s.mu.RUnlock()
	view, ok := s.byEmail[key]
	if !ok || key == "" {
		return nil
	}
	return view.snapshot()
}

// GlobalRecords is the 全局日志 pane buffer.
func (s *Store) GlobalRecords() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.global.snapshot()
}

// AllRecords is the 10000-entry combined buffer.
func (s *Store) AllRecords() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.all.snapshot()
}

// Counts reports buffer occupancy without copying anything.
func (s *Store) Counts() (total, global, accounts int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.all.len(), s.global.len(), len(s.byEmail)
}

// Seq is the last sequence number handed out.
func (s *Store) Seq() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seq
}
