package phoneprovider

// smsboweradapter.go closes UI_SPEC gap G3: worker.Config.PhoneProvider had no
// implementation that could be built from the persisted settings, so a
// registration that reached phone verification could not complete.
//
// It adds exactly two things on top of the already-ported Provider:
//
//  1. SnapshotSettings — a SettingsSource that reads the smsbower_* keys out of
//     the live state.json snapshot on every action, the way the Tk app re-read
//     its StringVars on every action (app.py:14367-14369, 16409, 16526, 16586).
//  2. SMSBowerProvider — a worker.PhoneProvider that remembers which SMSBower
//     activations are still outstanding and cancels them when the job ends.
//
// MONEY: (2) is a deliberate DIVERGENCE from Python and the reason this file
// exists. Python only ever released a rental from _phone_provider("bad"), which
// the worker calls for POST-submit failures ONLY (app.py:10259-10271): a
// pre-submit abort (app.py:10260 `raise`), a stopped task, or any error between
// "next" and "bad" left the rented number hanging until SMSBower expired it.
// Go has a cancellable context, so every such exit sends status 8 instead.
// Cancelling is never billable — it releases a hold — so the divergence can only
// save the user money, never spend it.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkppkq/openai-register-go/internal/settings"
	"github.com/pkppkq/openai-register-go/internal/smsbower"
	"github.com/pkppkq/openai-register-go/internal/worker"
)

const (
	// releaseTimeout bounds the end-of-job cleanup. The release runs on a FRESH
	// context (see releaseAll), so it must carry its own deadline or a dead
	// network would hang Close. smsbower.Client retries a transient failure 4
	// times with a <=8s backoff, so this leaves room for one full retry cycle.
	releaseTimeout = 45 * time.Second

	// cancelLabel is the label Python passes for a status-8 call (app.py:16612);
	// it is user-visible through "SMSBower 激活已取消: ..." (app.py:16531).
	cancelLabel = "取消"
)

// ErrClosed is returned by Next after Close: the supply is shut down and must
// not rent anything else.
var ErrClosed = errors.New("手机号供给已关闭")

// ---------------------------------------------------------------------------
// SnapshotSettings — the smsbower_* keys of state.json
// ---------------------------------------------------------------------------

// SnapshotFunc returns the FULL state.json map, i.e. what state.Store.Load
// returns (settings.FromSnapshot reads its "settings" sub-object). It is a
// function, not a value, because Python read the Tk variables at the moment of
// each provider action: editing the API key mid-run took effect on the next
// call, and this keeps that true.
type SnapshotFunc func() map[string]any

// SnapshotSettings implements SettingsSource over the persisted snapshot.
//
// Keys read (verified against the user's real state.json), all inside
// "settings":
//
//	smsbower_enabled         bool    -> SMSBowerEnabled / Settings.Enabled
//	smsbower_api_key         string  -> SMSBowerAPIKey  / Settings.APIKey
//	smsbower_service         string  -> Settings.Service   (default "dr")
//	smsbower_country         string  -> Settings.Country   (default "33")
//	smsbower_max_price       string  -> Settings.MaxPrice  (default "0.07")
//	phone_max_receive_count  int     -> PhoneReceiveLimit
//
// The decode is delegated to settings.FromSnapshot (the port of GUI.load_state)
// rather than re-read here, so there is exactly one implementation of the
// per-key coercions. That matters most for smsbower_max_price: load turns a
// stored "" back into "0.07" (app.py:14190) even though save writes ""
// (app.py:14278). Reading the key naively would produce "" = NO PRICE CAP and
// rent at any price — the asymmetry is a money guard, not a quirk.
type SnapshotSettings struct {
	Snapshot SnapshotFunc
}

var _ SettingsSource = SnapshotSettings{}

// raw is one live read. A nil Snapshot yields the zero Raw, whose Enabled is
// false — i.e. an unwired adapter silently uses the manual pool and can never
// rent, which is the safe direction.
func (s SnapshotSettings) raw() Raw {
	if s.Snapshot == nil {
		return Raw{}
	}
	decoded := settings.FromSnapshot(s.Snapshot())
	return Raw{
		Enabled:  decoded.SMSBowerEnabled,
		APIKey:   decoded.SMSBowerAPIKey,
		Service:  decoded.SMSBowerService,
		Country:  decoded.SMSBowerCountry,
		MaxPrice: decoded.SMSBowerMaxPrice,
		// Raw holds the widget text; settings has already applied
		// `max(0, int(value or 0))`, and ParseReceiveLimit re-parses it
		// unchanged.
		PhoneMaxReceiveCount: strconv.Itoa(decoded.PhoneMaxReceiveCount),
	}
}

func (s SnapshotSettings) SMSBowerEnabled() bool { return s.raw().SMSBowerEnabled() }

func (s SnapshotSettings) SMSBowerSettings() (Settings, error) { return s.raw().SMSBowerSettings() }

func (s SnapshotSettings) SMSBowerAPIKey() string { return s.raw().SMSBowerAPIKey() }

func (s SnapshotSettings) PhoneReceiveLimit() int { return s.raw().PhoneReceiveLimit() }

// ---------------------------------------------------------------------------
// SMSBowerProvider — worker.PhoneProvider with a guaranteed release path
// ---------------------------------------------------------------------------

// SMSBowerConfig wires the adapter. Only Snapshot and Context are worth setting
// in production; the rest are test seams or optional pools.
type SMSBowerConfig struct {
	// Snapshot is the live state.json reader. Nil means "no SMSBower": the
	// provider then serves only the manual phone pool.
	Snapshot SnapshotFunc

	// Pool is the imported-phone/account pool. Nil behaves like an empty
	// self.phones/self.accounts.
	Pool Pool

	// Log is the account-scoped logger (`self._emit_log(msg, email_addr)`).
	Log LogFunc

	// Context aborts the blocking waits AND triggers the release of every
	// outstanding rental. Wire the job context here: the UI cancels it both on
	// Stop and on normal job completion, which is what makes the release
	// automatic.
	Context context.Context

	// NewClient / HTTPGet / Sleep default to the package defaults. Tests MUST
	// override NewClient — a real GetNumber rents a billable number.
	NewClient ClientFactory
	HTTPGet   HTTPGetFunc
	Sleep     func(time.Duration)
}

// rental is one number this adapter is responsible for releasing.
type rental struct {
	email        string
	number       string
	activationID string
	// cancelTried records that Provider.Bad already sent status 8. It is kept on
	// the list anyway: SMSBower refuses a cancel inside the first minutes
	// (EARLY_CANCEL_DENIED, smsbower.go error map) and
	// _smsbower_set_activation_status swallows the outcome (app.py:16532), so
	// the only way to know the release stuck is to try again at the end.
	cancelTried bool
}

// SMSBowerProvider is the worker.PhoneProvider the UI wires into
// worker.Config.PhoneProvider.
//
// Next/Sent/Code/Good/Bad behave exactly like the embedded Provider (the
// line-faithful port of _phone_provider); the wrapper only adds the rental
// bookkeeping and the cancellation guard.
type SMSBowerProvider struct {
	*Provider

	// mu guards outstanding and closed. It is never held across an HTTP call.
	mu sync.Mutex
	// outstanding is a SLICE, not a map: releases go out in rental order. Go map
	// iteration is randomised, which would make the release order (and the log)
	// non-deterministic.
	outstanding []rental
	closed      bool

	stop     chan struct{}
	stopOnce sync.Once
}

var _ worker.PhoneProvider = (*SMSBowerProvider)(nil)

// NewSMSBowerProvider builds the provider. When cfg.Context is cancellable, a
// watchdog releases every outstanding rental as soon as it is done, so a stopped
// or finished job never leaves a number rented.
func NewSMSBowerProvider(cfg SMSBowerConfig) *SMSBowerProvider {
	inner := New(Config{
		Settings:  SnapshotSettings{Snapshot: cfg.Snapshot},
		Pool:      cfg.Pool,
		Log:       cfg.Log,
		NewClient: cfg.NewClient,
		HTTPGet:   cfg.HTTPGet,
		Context:   cfg.Context,
		Sleep:     cfg.Sleep,
	})
	provider := &SMSBowerProvider{Provider: inner, stop: make(chan struct{})}
	if cfg.Context != nil && cfg.Context.Done() != nil {
		go provider.watch(cfg.Context)
	}
	return provider
}

func (s *SMSBowerProvider) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		s.releaseAll()
	case <-s.stop:
	}
}

// Next reserves the next number (app.py:16536-16582) and takes responsibility
// for releasing it.
//
// Two money guards wrap the ported call, both of them DIVERGENCEs Python could
// not have (it had no cancellation):
//   - a cancelled context short-circuits BEFORE the rental, so pressing Stop
//     cannot buy one more number;
//   - a context that dies during the rental releases the number that just
//     arrived instead of returning it into a flow that is already over.
//
// Returning an error aborts the whole registration (worker/phone.go:141-144),
// which is the correct reaction to "the job was cancelled".
func (s *SMSBowerProvider) Next(email string, opts map[string]string) (map[string]string, error) {
	if err := s.stopped(); err != nil {
		return nil, fmt.Errorf("任务已取消，停止取号: %w", err)
	}

	phone, err := s.Provider.Next(email, opts)
	if err != nil || len(phone) == 0 {
		return phone, err
	}
	s.track(email, phone)

	if err := s.stopped(); err != nil {
		s.releaseAll()
		return nil, fmt.Errorf("任务已取消，已释放刚取到的号码: %w", err)
	}
	return phone, nil
}

// Good marks the activation complete (status 6, app.py:16596-16601) and drops it
// from the release list: after a finish the rental is paid for and a later
// status 8 must never be sent.
func (s *SMSBowerProvider) Good(email string, phone map[string]string) error {
	err := s.Provider.Good(email, phone)
	s.forget(phone)
	return err
}

// Bad cancels the activation (status 8, app.py:16602-16636) but KEEPS it on the
// release list, because that first cancel is the one most likely to be refused
// with EARLY_CANCEL_DENIED — the rotation reaches "bad" within a minute or two
// of the rental. The retry at Close/ctx.Done happens well after that window.
func (s *SMSBowerProvider) Bad(email string, phone map[string]string) error {
	err := s.Provider.Bad(email, phone)
	s.markCancelTried(phone)
	return err
}

// Release cancels every outstanding rental now, without shutting the provider
// down. Safe to call at any time and as often as wanted.
func (s *SMSBowerProvider) Release() { s.releaseAll() }

// Close stops the watchdog, refuses further rentals and releases everything
// still outstanding. Idempotent; `defer provider.Close()` around a job is the
// intended usage even when the context is already wired.
func (s *SMSBowerProvider) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.releaseAll()
}

// OutstandingActivations lists the activation ids this adapter would still
// release, in rental order. Diagnostics only.
func (s *SMSBowerProvider) OutstandingActivations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.outstanding))
	for _, item := range s.outstanding {
		out = append(out, item.activationID)
	}
	return out
}

// stopped reports why no further number may be rented, or nil.
func (s *SMSBowerProvider) stopped() error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return s.Provider.context().Err()
}

func (s *SMSBowerProvider) track(email string, phone map[string]string) {
	if !isSMSBower(phone) {
		return
	}
	// Same read as _smsbower_set_activation_status (app.py:16524): an id that is
	// blank after stripping can never be released, so it is not worth tracking.
	activationID := strings.TrimSpace(phone["activation_id"])
	if activationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.outstanding {
		if s.outstanding[i].activationID == activationID {
			return
		}
	}
	s.outstanding = append(s.outstanding, rental{
		email:        email,
		number:       phone["number"],
		activationID: activationID,
	})
}

func (s *SMSBowerProvider) forget(phone map[string]string) {
	activationID := strings.TrimSpace(phone["activation_id"])
	if activationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.outstanding {
		if s.outstanding[i].activationID == activationID {
			s.outstanding = append(s.outstanding[:i], s.outstanding[i+1:]...)
			return
		}
	}
}

func (s *SMSBowerProvider) markCancelTried(phone map[string]string) {
	activationID := strings.TrimSpace(phone["activation_id"])
	if activationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.outstanding {
		if s.outstanding[i].activationID == activationID {
			s.outstanding[i].cancelTried = true
			return
		}
	}
}

// releaseAll sends status 8 for every outstanding rental.
//
// It drains the list under the lock first, so a concurrent Close and ctx.Done
// cannot both release the same activation, and so the HTTP calls run unlocked.
//
// The calls go out on a FRESH context: the usual trigger is exactly the
// cancellation of s.Provider.context(), and reusing that dead context would make
// every release request fail before it left the process — the silent leak this
// whole file exists to prevent. Everything else (client construction, the
// swallow-and-log error handling, the "SMSBower 激活已取消" wording) is reused
// from the ported _smsbower_set_activation_status via a second Provider that
// differs only in its context.
func (s *SMSBowerProvider) releaseAll() {
	s.mu.Lock()
	pending := s.outstanding
	s.outstanding = nil
	s.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	releaser := New(Config{
		Settings:  s.cfg.Settings,
		Log:       s.cfg.Log,
		NewClient: s.cfg.NewClient,
		Context:   ctx,
		Sleep:     s.cfg.Sleep,
	})

	for _, item := range pending {
		if item.cancelTried {
			s.logf(item.email, "SMSBower 重试释放未完成的激活: %s 激活ID=%s", item.number, item.activationID)
		} else {
			s.logf(item.email, "SMSBower 释放未完成的激活: %s 激活ID=%s", item.number, item.activationID)
		}
		releaser.setActivationStatus(item.email, map[string]string{
			"activation_id": item.activationID,
			"provider":      "smsbower",
		}, smsbower.StatusCancel, cancelLabel)
	}
}
