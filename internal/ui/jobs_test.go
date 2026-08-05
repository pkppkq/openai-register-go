package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// startStubJob mirrors startJob's bookkeeping but runs a caller-supplied
// function instead of a real worker entry point. The registry, the status
// transitions and the cancellation path are what need testing; actually driving
// a browser would cost money and cannot run in CI.
func (a *App) startStubJob(id string, run func(ctx context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())

	a.jobs.mu.Lock()
	a.jobs.seq++
	j := &job{
		seq:    a.jobs.seq,
		view:   JobView{ID: id, Status: StatusRunning, Started: time.Now().Format(time.RFC3339Nano)},
		cancel: cancel,
	}
	a.jobs.jobs[id] = j
	a.jobs.mu.Unlock()

	go func() {
		defer cancel()
		err := run(ctx)
		// markJobFinished, NOT finishJob: a stub never ran a worker, so it has no
		// outcome to persist, and going through finishJob would have every registry
		// test write to whatever state.json the App points at.
		a.markJobFinished(id, nil, err, ctx.Err() != nil)
	}()
}

func waitStatus(t *testing.T, a *App, id string, want JobStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.jobs.mu.Lock()
		got := a.jobs.jobs[id].view.Status
		a.jobs.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.jobs.mu.Lock()
	got := a.jobs.jobs[id].view.Status
	a.jobs.mu.Unlock()
	t.Fatalf("job %s: status = %q, want %q", id, got, want)
}

// TestJobCancellation is the contract the whole UI depends on: a long run must
// be interruptible. The Tk app could only ever set a flag; here cancellation
// goes through the context the worker entry points already thread, so it
// unwinds the run instead of orphaning a browser.
func TestJobCancellation(t *testing.T) {
	// newTempApp, not New(): New() points at the user's REAL state.json, which is
	// shared with the still-running Python app.
	a := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})

	released := make(chan struct{})
	a.startStubJob("j1", func(ctx context.Context) error {
		<-ctx.Done()
		close(released)
		return ctx.Err()
	})

	if err := a.CancelJob("j1"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not reach the running job")
	}
	waitStatus(t, a, "j1", StatusCancelled)

	// Cancelling a finished job must report, not panic or re-cancel.
	if err := a.CancelJob("j1"); err == nil {
		t.Error("expected an error cancelling an already-finished job")
	}
	if err := a.CancelJob("nope"); err == nil {
		t.Error("expected an error cancelling an unknown job")
	}
}

func TestJobSuccessAndFailure(t *testing.T) {
	// newTempApp, not New(): New() points at the user's REAL state.json, which is
	// shared with the still-running Python app.
	a := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	a.startStubJob("ok", func(context.Context) error { return nil })
	a.startStubJob("bad", func(context.Context) error { return context.DeadlineExceeded })

	waitStatus(t, a, "ok", StatusSucceeded)
	waitStatus(t, a, "bad", StatusFailed)

	jobs := a.ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("ListJobs returned %d jobs, want 2", len(jobs))
	}
	// Newest first, by creation order — timestamps alone tie within a second.
	if jobs[0].ID != "bad" {
		t.Errorf("ListJobs not newest-first: got %q first", jobs[0].ID)
	}
	for _, j := range jobs {
		if j.ID == "bad" && j.Error == "" {
			t.Error("failed job carries no error text")
		}
		if j.Finished == "" {
			t.Errorf("job %s has no finish time", j.ID)
		}
	}
}

// TestJobRegistryConcurrency is meant to be run under -race: it hammers the
// registry from many goroutines the way a fan-out of accounts would.
func TestJobRegistryConcurrency(t *testing.T) {
	// newTempApp, not New(): New() points at the user's REAL state.json, which is
	// shared with the still-running Python app.
	a := newTempApp(t, map[string]any{"schema_version": 2, "accounts": []any{}})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		id := "c" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		a.startStubJob(id, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		wg.Add(2)
		go func() { defer wg.Done(); _ = a.ListJobs() }()
		go func() { defer wg.Done(); _ = a.CancelJob(id) }()
	}
	wg.Wait()
	for _, j := range a.ListJobs() {
		if j.Status == StatusRunning {
			// Give the goroutine a moment to observe its cancelled context.
			waitStatus(t, a, j.ID, StatusCancelled)
		}
	}
}

// TestInternalStartJobRejectsUnknownKind guards the internal dispatch switch.
//
// The account MUST exist, or accountByEmail refuses first and the switch is
// never reached — which is what this test used to do, so it passed no matter
// what the switch said. Reaching the switch with a resolvable account is safe
// precisely because an unknown kind returns before any worker is constructed;
// that ordering is itself part of what is being pinned.
func TestInternalStartJobRejectsUnknownKind(t *testing.T) {
	a := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{accountMap("known@example.com", "free", "", "未分组")},
	})
	_, err := a.startJob("definitely_not_a_kind", "known@example.com")
	if err == nil {
		t.Fatal("expected an error for an unknown job kind")
	}
	if !strings.Contains(err.Error(), "未知的任务类型") {
		t.Fatalf("unexpected error: %v", err)
	}
	if jobs := a.ListJobs(); len(jobs) != 0 {
		t.Fatalf("an unknown kind created %d job(s)", len(jobs))
	}
}

// The two refusals app.py:16690-16696 makes before a worker exists. Both are
// money-safety: see preflight.
func TestInternalStartJobPreflightRefusals(t *testing.T) {
	locked := accountMap("locked@example.com", "free", "", "未分组")
	locked["status"] = statusEmailLocked
	// A +alias of the SAME mother mailbox. Python locks the mailbox, not the
	// address, so this one is refused too even though its own status is clean.
	sibling := accountMap("locked+2@example.com", "free", "", "未分组")

	cloud := accountMap("cloud@"+models.DefaultDomainMailDomain, "free", "", "未分组")
	cloud["mail_provider"] = "cloudmail"

	a := newTempApp(t, map[string]any{
		"schema_version": 2,
		"accounts":       []any{locked, sibling, cloud},
		"settings": map[string]any{
			"cloud_mail_enabled": true,
			"cloud_mail_base":    "https://mail.example.test",
			"cloud_mail_token":   "   ",
		},
	})

	for _, tc := range []struct{ email, want string }{
		{"locked@example.com", statusEmailLocked},
		{"locked+2@example.com", statusEmailLocked},
		{"cloud@" + models.DefaultDomainMailDomain, statusCloudMailUnset},
	} {
		if _, err := a.startJob(string(JobRegister), tc.email); err == nil {
			t.Errorf("%s: expected a refusal, got none", tc.email)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.email, err, tc.want)
		}
	}
	if jobs := a.ListJobs(); len(jobs) != 0 {
		t.Fatalf("a refused startJob created %d job(s)", len(jobs))
	}
}
