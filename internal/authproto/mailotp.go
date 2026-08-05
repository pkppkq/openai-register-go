package authproto

// The email-OTP injection point, filled in.
//
// This file is the adapter between internal/mail (which already implements
// every backend: IMAP, Microsoft Graph, the legacy Outlook REST API and Cloud
// Mail) and the OTPReader / MailReaderFactory callbacks this package declares.
//
// Ported:
//
//	app.py:7991-7994   create_mail_reader — the cloudmail-vs-Hotmail branch
//	                   itself is mail.CreateMailReader; this file only wires it
//	                   to the callback shape.
//	app.py:8229-8233   the _read_email_otp_code call site:
//	                     otp_reader = create_mail_reader(self.account, self.log, "")
//	                     try:    return otp_reader.wait_for_code(min_timestamp)
//	                     finally: otp_reader.close()
//	                   — proxy_url is "", and wait_for_code is called with ONLY
//	                   min_timestamp, so its defaults apply (see below).
//
// It lives inside package authproto rather than in a package of its own
// because there is no import cycle to avoid: internal/mail imports only
// internal/models and internal/tlsclient, and never internal/authproto.

import (
	"context"
	"errors"

	"github.com/pkppkq/openai-register-go/internal/mail"
	"github.com/pkppkq/openai-register-go/internal/models"
)

const (
	// DefaultOTPTimeoutSeconds is wait_for_code's `timeout: int = 600`, the
	// same default on both readers: CloudMailReader (app.py:6785-6789) and
	// HotmailOtpReader (app.py:7277).
	DefaultOTPTimeoutSeconds = 600
	// DefaultOTPLookbackSeconds is wait_for_code's
	// `lookback_seconds: int = DEFAULT_EMAIL_OTP_LOOKBACK_SECONDS`
	// (app.py:277 = 300), again on both readers.
	DefaultOTPLookbackSeconds = mail.DefaultEmailOTPLookbackSeconds
)

// MailOTPOptions tunes the adapter. Its ZERO VALUE is app.py:8229-8231 exactly:
// no proxy, timeout 600, lookback 300, no cancellation.
type MailOTPOptions struct {
	// ProxyURL is create_mail_reader's third argument. app.py:8229 passes "" —
	// protocol mode does NOT read the mailbox through the flow's proxy, even
	// when the OpenAI requests are proxied. Other create_mail_reader call sites
	// in app.py do pass a proxy (e.g. app.py:21131), hence the knob.
	ProxyURL string
	// TimeoutSeconds is wait_for_code's timeout. 0 means "argument omitted" and
	// selects DefaultOTPTimeoutSeconds; note that passing a literal 0 through to
	// mail.Reader would instead be clamped to a 1-second wait.
	TimeoutSeconds int
	// LookbackSeconds is wait_for_code's lookback_seconds. 0 means "argument
	// omitted" and selects DefaultOTPLookbackSeconds. Use a negative value for
	// a real zero lookback; the readers clamp it back to 0 with max(0, ...).
	LookbackSeconds int
	// Context bounds the blocking wait. nil means context.Background(), i.e.
	// Python's uninterruptible loop.
	Context context.Context
}

func (o MailOTPOptions) timeoutSeconds() int {
	if o.TimeoutSeconds == 0 {
		return DefaultOTPTimeoutSeconds
	}
	return o.TimeoutSeconds
}

func (o MailOTPOptions) lookbackSeconds() int {
	if o.LookbackSeconds == 0 {
		return DefaultOTPLookbackSeconds
	}
	return o.LookbackSeconds
}

func (o MailOTPOptions) context() context.Context {
	if o.Context == nil {
		return context.Background()
	}
	return o.Context
}

// mailOTPReader adapts a mail.Reader to OTPReader: it binds the context and the
// two arguments app.py never passes.
type mailOTPReader struct {
	reader   mail.Reader
	ctx      context.Context
	timeout  int
	lookback int
}

// WaitForCode is otp_reader.wait_for_code(min_timestamp) (app.py:8231).
func (r *mailOTPReader) WaitForCode(minTimestamp float64) (string, error) {
	return r.reader.WaitForCode(r.ctx, minTimestamp, r.timeout, r.lookback)
}

// Close is the `finally: otp_reader.close()` of app.py:8232-8233.
func (r *mailOTPReader) Close() error { return r.reader.Close() }

// Reader exposes the wrapped backend, for callers that need the reader's other
// capabilities (folder listing, deactivation scan) on the same connection.
func (r *mailOTPReader) Reader() mail.Reader { return r.reader }

// NewMailOTPReader adapts an already-constructed mail.Reader to OTPReader.
//
// It does NOT connect: HotmailOtpReader.WaitForCode dials lazily (app.py:7278-
// 7283) and CloudMailReader needs no connection at all, so this matches Python,
// where create_mail_reader also returns an unconnected reader.
func NewMailOTPReader(reader mail.Reader, opts MailOTPOptions) OTPReader {
	return &mailOTPReader{
		reader:   reader,
		ctx:      opts.context(),
		timeout:  opts.timeoutSeconds(),
		lookback: opts.lookbackSeconds(),
	}
}

// NewMailReaderFactory returns the MailReaderFactory for the given options.
//
//	authproto.Options{MailReaderFactory: authproto.NewMailReaderFactory(
//	    authproto.MailOTPOptions{Context: ctx},
//	)}
func NewMailReaderFactory(opts MailOTPOptions) MailReaderFactory {
	return func(account *models.MailAccount, log Log) (OTPReader, error) {
		// app.py:7991-7994. mail.Log and Log are the same underlying type; a
		// nil Log converts to a nil mail.Log, which mail's own emit() tolerates.
		reader, err := mail.CreateMailReader(account, mail.Log(log), opts.ProxyURL)
		if err != nil {
			// CloudMailReader's constructor raises on a bad base URL / empty
			// token, and Python let that propagate out of _read_email_otp_code
			// the same way.
			return nil, err
		}
		if reader == nil {
			return nil, errors.New("authproto: 邮箱读取器创建失败")
		}
		return NewMailOTPReader(reader, opts), nil
	}
}

// DefaultMailReaderFactory is app.py:8229's wiring verbatim — no proxy, the
// stock 600s timeout and 300s lookback, no cancellation. Assign it straight in:
//
//	authproto.Options{MailReaderFactory: authproto.DefaultMailReaderFactory}
func DefaultMailReaderFactory(account *models.MailAccount, log Log) (OTPReader, error) {
	return NewMailReaderFactory(MailOTPOptions{})(account, log)
}
