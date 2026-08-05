package logs

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	ts := time.Date(2026, 7, 26, 9, 8, 7, 0, time.Local)
	return func() time.Time { return ts }
}

func newTestStore() *Store {
	s := NewStore()
	s.SetNowFunc(fixedClock())
	return s
}

func TestAppendRoutesAndTags(t *testing.T) {
	s := newTestStore()

	global := s.Append("代理检测失败", "")
	if global.Email != "" || global.Scope != ScopeGlobal {
		t.Fatalf("global record = %+v", global)
	}
	if global.Seq != 1 || global.TimeText != "09:08:07" {
		t.Fatalf("seq/time = %d %q", global.Seq, global.TimeText)
	}
	if global.Level != LevelError || global.Module != "代理" {
		t.Fatalf("level/module = %q %q", global.Level, global.Module)
	}
	if want := "[09:08:07] [代理] 代理检测失败\n"; FormatLine(global) != want {
		t.Fatalf("FormatLine = %q", FormatLine(global))
	}

	// A bare [email] bracket routes to the account pane and is removed from the
	// stored text (app.py:18453 + 18487).
	acct := s.Append("[A@B.com] 已保存", "")
	if acct.Email != "a@b.com" || acct.Scope != ScopeAccount {
		t.Fatalf("account record = %+v", acct)
	}
	if acct.Message != "[系统] 已保存" {
		t.Fatalf("account message = %q", acct.Message)
	}

	if got := len(s.GlobalRecords()); got != 1 {
		t.Fatalf("global view = %d", got)
	}
	if got := len(s.AccountRecords("a@b.com")); got != 1 {
		t.Fatalf("account view = %d", got)
	}
	// The combined buffer holds both.
	if got := len(s.AllRecords()); got != 2 {
		t.Fatalf("all = %d", got)
	}
	// Unknown / empty address behaves like logs_by_email.get(key, []).
	if got := s.AccountRecords("nobody@x"); got != nil {
		t.Fatalf("unknown account = %v", got)
	}
	if got := s.AccountRecords(""); got != nil {
		t.Fatalf("empty account key = %v", got)
	}
}

func TestPerViewRingCaps(t *testing.T) {
	s := newTestStore()
	for i := 0; i < MaxLogRecordsPerView+50; i++ {
		s.Append("g"+strconv.Itoa(i), "")
		s.Append("a"+strconv.Itoa(i), "a@b")
	}

	globals := s.GlobalRecords()
	if len(globals) != MaxLogRecordsPerView {
		t.Fatalf("global view len = %d", len(globals))
	}
	// Oldest dropped, newest kept, order preserved.
	if globals[0].Message != "[系统] g50" {
		t.Fatalf("global head = %q", globals[0].Message)
	}
	if last := globals[len(globals)-1]; last.Message != "[系统] g2049" {
		t.Fatalf("global tail = %q", last.Message)
	}
	for i := 1; i < len(globals); i++ {
		if globals[i].Seq <= globals[i-1].Seq {
			t.Fatalf("global view out of order at %d", i)
		}
	}

	accounts := s.AccountRecords("a@b")
	if len(accounts) != MaxLogRecordsPerView {
		t.Fatalf("account view len = %d", len(accounts))
	}
	if accounts[0].Message != "[系统] a50" {
		t.Fatalf("account head = %q", accounts[0].Message)
	}
}

// app.py:18479 trims log_records at 10000 but never touches the per-view lists,
// so a record can be gone from the combined buffer and still be on screen.
func TestTotalCapIsIndependentOfViewCaps(t *testing.T) {
	s := newTestStore()
	for i := 0; i < MaxTotalLogRecords+5; i++ {
		s.Append("m"+strconv.Itoa(i), "")
	}
	all := s.AllRecords()
	if len(all) != MaxTotalLogRecords {
		t.Fatalf("all len = %d", len(all))
	}
	if all[0].Seq != 6 {
		t.Fatalf("all head seq = %d, want 6", all[0].Seq)
	}
	if len(s.GlobalRecords()) != MaxLogRecordsPerView {
		t.Fatalf("global view len = %d", len(s.GlobalRecords()))
	}
	total, global, accounts := s.Counts()
	if total != MaxTotalLogRecords || global != MaxLogRecordsPerView || accounts != 0 {
		t.Fatalf("counts = %d %d %d", total, global, accounts)
	}
}

// Snapshots must be detached copies: the UI reads them while workers keep
// writing into the same backing array.
func TestSnapshotIsACopy(t *testing.T) {
	s := newTestStore()
	s.Append("hello", "")
	snap := s.GlobalRecords()
	snap[0].Message = "mutated"
	if again := s.GlobalRecords(); again[0].Message == "mutated" {
		t.Fatal("snapshot aliases the ring buffer")
	}
	// Overwriting the ring after wrap must not disturb an earlier snapshot.
	small := &ring{max: 2}
	small.push(Record{Seq: 1})
	small.push(Record{Seq: 2})
	before := small.snapshot()
	small.push(Record{Seq: 3})
	if before[0].Seq != 1 || before[1].Seq != 2 {
		t.Fatalf("snapshot changed under wrap: %+v", before)
	}
	after := small.snapshot()
	if after[0].Seq != 2 || after[1].Seq != 3 {
		t.Fatalf("wrapped order wrong: %+v", after)
	}
}

func TestVisibleAndSplit(t *testing.T) {
	s := newTestStore()
	g := s.Append("全局", "")
	a := s.Append("账户", "a@b")
	other := s.Append("别人", "c@d")

	// No selection: account lines are stored but not drawn (app.py:18482).
	if !s.Visible(g) || s.Visible(a) {
		t.Fatal("with no selection only global lines are visible")
	}

	s.SetSelected("A@B")
	if s.Selected() != "a@b" {
		t.Fatalf("selected = %q", s.Selected())
	}
	if !s.Visible(a) || s.Visible(other) {
		t.Fatal("only the selected account is visible")
	}

	acct, glob := s.SplitVisible([]Record{g, a, other})
	if len(acct) != 1 || acct[0].Email != "a@b" {
		t.Fatalf("account split = %+v", acct)
	}
	if len(glob) != 1 || glob[0].Email != "" {
		t.Fatalf("global split = %+v", glob)
	}
}

func TestAppendBatchOrderAndSeq(t *testing.T) {
	s := newTestStore()
	if got := s.AppendBatch(nil); got != nil {
		t.Fatalf("empty batch = %v", got)
	}
	out := s.AppendBatch([]Entry{
		{Message: "一"},
		{Message: "[x@y] 二"},
		{Message: "三", Email: "Z@Q "},
	})
	if len(out) != 3 {
		t.Fatalf("batch len = %d", len(out))
	}
	for i, r := range out {
		if r.Seq != i+1 {
			t.Fatalf("record %d seq = %d", i, r.Seq)
		}
	}
	if out[1].Email != "x@y" || out[2].Email != "z@q" {
		t.Fatalf("batch routing = %q %q", out[1].Email, out[2].Email)
	}
	if s.Seq() != 3 {
		t.Fatalf("store seq = %d", s.Seq())
	}
}

func TestToModel(t *testing.T) {
	s := newTestStore()
	r := s.Append("[a@b] 完成", "")
	m := r.ToModel()
	if m.Seq != r.Seq || m.TimeText != r.TimeText || m.Message != r.Message ||
		m.Email != r.Email || m.Scope != r.Scope {
		t.Fatalf("ToModel mismatch: %+v vs %+v", m, r)
	}
}

// Many producers, one reader — must be race-clean and must hand out a
// contiguous, unique sequence.
func TestConcurrentAppend(t *testing.T) {
	s := newTestStore()
	const writers, perWriter = 8, 200

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			email := fmt.Sprintf("u%d@x", w%3)
			for i := 0; i < perWriter; i++ {
				if i%2 == 0 {
					s.Append("消息", email)
				} else {
					s.Append("消息", "")
				}
			}
		}(w)
	}
	stop := make(chan struct{})
	var readWG sync.WaitGroup
	readWG.Add(1)
	go func() {
		defer readWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.GlobalRecords()
				_ = s.AccountRecords("u0@x")
				_, _, _ = s.Counts()
			}
		}
	}()
	wg.Wait()
	close(stop)
	readWG.Wait()

	if got := s.Seq(); got != writers*perWriter {
		t.Fatalf("seq = %d, want %d", got, writers*perWriter)
	}
	all := s.AllRecords()
	if len(all) != writers*perWriter {
		t.Fatalf("all len = %d", len(all))
	}
	seen := make(map[int]bool, len(all))
	for _, r := range all {
		if seen[r.Seq] {
			t.Fatalf("duplicate seq %d", r.Seq)
		}
		seen[r.Seq] = true
	}
}
