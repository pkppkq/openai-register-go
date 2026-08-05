package authproto

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
)

// ---------------------------------------------------------------------------
// Fake mail backend
//
// NO TEST HERE MAY TOUCH A MAILBOX: no IMAP socket, no Graph token refresh, no
// Cloud Mail API. The adapter is exercised against this in-memory reader, and
// the two factory tests only CONSTRUCT the real readers (which is pure struct
// building in internal/mail — the connection is lazy).
// ---------------------------------------------------------------------------

type waitCall struct {
	ctx          context.Context
	minTimestamp float64
	timeout      int
	lookback     int
}

type fakeMailReader struct {
	waits  []waitCall
	code   string
	err    error
	closes int
}

func (r *fakeMailReader) WaitForCode(ctx context.Context, minTimestamp float64, timeout, lookbackSeconds int) (string, error) {
	r.waits = append(r.waits, waitCall{ctx, minTimestamp, timeout, lookbackSeconds})
	return r.code, r.err
}

func (r *fakeMailReader) Close() error { r.closes++; return nil }
func (r *fakeMailReader) Connect() error {
	return errors.New("fakeMailReader: Connect must not be called")
}

func (r *fakeMailReader) ListFolders() ([]string, error) { return nil, nil }
func (r *fakeMailReader) ListRecentMessages(string, int, string) ([]mail.MailRecord, error) {
	return nil, nil
}
func (r *fakeMailReader) FetchMessage(string, string) (mail.MailRecord, error) {
	return mail.MailRecord{}, nil
}
func (r *fakeMailReader) WaitForTeamInvite(context.Context, float64, int) (string, error) {
	return "", nil
}
func (r *fakeMailReader) WaitForLink(context.Context, string, float64, int) (string, error) {
	return "", nil
}
func (r *fakeMailReader) ScanOpenAIDeactivationNotice(int, int) (mail.DeactivationResult, error) {
	return mail.DeactivationResult{}, nil
}

// ---------------------------------------------------------------------------
// app.py:8229-8233 defaults
// ---------------------------------------------------------------------------

func TestMailOTPReaderUsesPythonDefaults(t *testing.T) {
	if DefaultOTPTimeoutSeconds != 600 {
		t.Errorf("DefaultOTPTimeoutSeconds = %d, want wait_for_code's 600", DefaultOTPTimeoutSeconds)
	}
	if DefaultOTPLookbackSeconds != 300 {
		t.Errorf("DefaultOTPLookbackSeconds = %d, want DEFAULT_EMAIL_OTP_LOOKBACK_SECONDS", DefaultOTPLookbackSeconds)
	}

	backend := &fakeMailReader{code: "123456"}
	reader := NewMailOTPReader(backend, MailOTPOptions{})
	got, err := reader.WaitForCode(1700000000)
	if err != nil || got != "123456" {
		t.Fatalf("WaitForCode = (%q, %v)", got, err)
	}
	if len(backend.waits) != 1 {
		t.Fatalf("waits = %d", len(backend.waits))
	}
	call := backend.waits[0]
	if call.minTimestamp != 1700000000 {
		t.Errorf("minTimestamp = %v", call.minTimestamp)
	}
	if call.timeout != 600 || call.lookback != 300 {
		t.Errorf("timeout/lookback = %d/%d, want 600/300", call.timeout, call.lookback)
	}
	if call.ctx == nil || call.ctx.Err() != nil {
		t.Errorf("ctx = %v, want a live context.Background()", call.ctx)
	}
	if _, ok := call.ctx.Deadline(); ok {
		t.Error("the default context must not carry a deadline; Python's loop had none")
	}

	if err := reader.Close(); err != nil || backend.closes != 1 {
		t.Errorf("Close: err=%v closes=%d", err, backend.closes)
	}

	// The wrapped backend stays reachable for the readers' other capabilities.
	if accessor, ok := reader.(interface{ Reader() mail.Reader }); !ok || accessor.Reader() != mail.Reader(backend) {
		t.Error("Reader() should expose the wrapped mail.Reader")
	}
}

func TestMailOTPOptionsOverrides(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &fakeMailReader{err: errors.New("邮箱取码超时")}
	reader := NewMailOTPReader(backend, MailOTPOptions{
		TimeoutSeconds: 45,
		// A negative lookback is the only way to ask for a REAL zero window;
		// the readers clamp it back with max(0, ...).
		LookbackSeconds: -1,
		Context:         ctx,
	})
	if _, err := reader.WaitForCode(5); err == nil {
		t.Fatal("the backend error must propagate")
	}
	call := backend.waits[0]
	if call.timeout != 45 || call.lookback != -1 {
		t.Errorf("timeout/lookback = %d/%d", call.timeout, call.lookback)
	}
	if call.ctx != ctx {
		t.Error("the supplied context must reach the backend")
	}
}

// ---------------------------------------------------------------------------
// create_mail_reader (app.py:7991-7994)
// ---------------------------------------------------------------------------

func TestDefaultMailReaderFactoryBuildsTheRealBackends(t *testing.T) {
	// Non-cloudmail -> HotmailOtpReader. Constructing it opens nothing.
	reader, err := DefaultMailReaderFactory(&models.MailAccount{
		Email: "User@Outlook.com", RefreshToken: "rt", ClientID: "cid",
	}, nil)
	if err != nil {
		t.Fatalf("DefaultMailReaderFactory: %v", err)
	}
	accessor, ok := reader.(interface{ Reader() mail.Reader })
	if !ok {
		t.Fatal("the factory should return the adapter")
	}
	if _, isHotmail := accessor.Reader().(*mail.HotmailOtpReader); !isHotmail {
		t.Errorf("backend = %T, want *mail.HotmailOtpReader", accessor.Reader())
	}
	if err := reader.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// mail_provider == "cloudmail" -> CloudMailReader (the branch is
	// case-insensitive, like Python's .casefold()).
	reader, err = DefaultMailReaderFactory(&models.MailAccount{
		Email:          "user@cloud.test",
		MailProvider:   " CloudMail ",
		CloudMailBase:  "https://cloud.test",
		CloudMailToken: "tok",
	}, nil)
	if err != nil {
		t.Fatalf("cloudmail factory: %v", err)
	}
	accessor, _ = reader.(interface{ Reader() mail.Reader })
	if _, isCloud := accessor.Reader().(*mail.CloudMailReader); !isCloud {
		t.Errorf("backend = %T, want *mail.CloudMailReader", accessor.Reader())
	}

	// A broken Cloud Mail config raises in Python and errors here; the flow
	// then reports it instead of silently falling back to IMAP.
	if _, err := DefaultMailReaderFactory(&models.MailAccount{
		Email: "user@cloud.test", MailProvider: "cloudmail",
	}, nil); err == nil {
		t.Error("an empty Cloud Mail base URL should fail")
	}
}

func TestNewMailReaderFactoryPassesOptionsThrough(t *testing.T) {
	factory := NewMailReaderFactory(MailOTPOptions{ProxyURL: "http://127.0.0.1:1", TimeoutSeconds: 7})
	reader, err := factory(&models.MailAccount{Email: "u@outlook.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := reader.(*mailOTPReader)
	if !ok {
		t.Fatalf("reader = %T", reader)
	}
	if adapter.timeout != 7 || adapter.lookback != DefaultOTPLookbackSeconds {
		t.Errorf("timeout/lookback = %d/%d", adapter.timeout, adapter.lookback)
	}
}

// ---------------------------------------------------------------------------
// The wiring inside _read_email_otp_code (app.py:8229-8233)
// ---------------------------------------------------------------------------

func TestReadEmailOTPCodeThroughTheAdapter(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })
	backend := &fakeMailReader{code: "246810"}
	f := newTestFlow(t, tr, func(o *Options) {
		o.MailReaderFactory = func(account *models.MailAccount, log Log) (OTPReader, error) {
			// normalize_email_address strips and extracts, it does NOT lowercase.
			if account.Email != "User@Example.com" {
				t.Errorf("factory got account %q, want the normalized address", account.Email)
			}
			return NewMailOTPReader(backend, MailOTPOptions{}), nil
		}
	})
	f.now = func() time.Time { return time.Unix(1700000500, 0) }

	// email_otp_requested_at is still 0.0 (falsy) -> `time.time() - 10`.
	got, err := f.readEmailOTPCode()
	if err != nil || got != "246810" {
		t.Fatalf("readEmailOTPCode = (%q, %v)", got, err)
	}
	if backend.waits[0].minTimestamp != 1700000490 {
		t.Errorf("minTimestamp = %v, want now-10", backend.waits[0].minTimestamp)
	}

	// Once _send_email_otp has stamped it, that timestamp is used verbatim.
	f.emailOTPRequestedAt = 1700000123.5
	if _, err := f.readEmailOTPCode(); err != nil {
		t.Fatal(err)
	}
	if backend.waits[1].minTimestamp != 1700000123.5 {
		t.Errorf("minTimestamp = %v", backend.waits[1].minTimestamp)
	}
	// try/finally: the reader is closed after every attempt.
	if backend.closes != 2 {
		t.Errorf("closes = %d, want one per attempt", backend.closes)
	}
}

func TestReadEmailOTPCodeFactoryErrorPropagates(t *testing.T) {
	tr := newFakeTransport(func(*Request) (*Response, error) { return jsonResponse(200, "{}"), nil })
	f := newTestFlow(t, tr, func(o *Options) {
		o.MailReaderFactory = func(*models.MailAccount, Log) (OTPReader, error) {
			return nil, errors.New("Cloud Mail Base URL 格式错误")
		}
	})
	_, err := f.readEmailOTPCode()
	if err == nil || !strings.Contains(err.Error(), "Cloud Mail Base URL 格式错误") {
		t.Errorf("err = %v", err)
	}

	// No factory at all is still the pre-existing guard.
	f2 := newTestFlow(t, tr, nil)
	if _, err := f2.readEmailOTPCode(); err == nil || err.Error() != "authproto: 未配置邮箱读取器" {
		t.Errorf("err = %v", err)
	}
}
