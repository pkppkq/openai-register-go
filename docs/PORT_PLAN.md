# Browser-worker port plan

Source: `app.py` `OpenAIRegisterPayLinkWorker` (8846-12298, ~3450 lines, 9 clusters).
Full structural map: session scratchpad `worker-map-summary.md` + `worker-map-methods.md`
(distilled from a 9-agent mapping workflow). Constants: `worker-constants.md`.

## What already exists (mechanical core, all live-verified)
- internal/models (structs: MailAccount, ProxyConfig, ProxyHealthResult, DeviceFingerprint, ...)
- internal/state, internal/tlsclient, internal/proxychain (chaining only), internal/smsbower, internal/mail

## What the worker needs that is NOT built yet
The worker is NOT self-contained. Prerequisites, dependency-sorted:

### Layer 0 — pure leaf helpers (no browser, unit-testable)
- **P0a `internal/openai`** — constants (AUTH_* endpoints, OAuth client_id/redirect/audience,
  CF_* timeouts, CF_INTERSTITIAL_DETECT_JS, DEFAULT_USER_AGENT) + auth helpers:
  random_urlsafe_string, pkce_code_challenge, openai_browser_headers, decode_jwt_payload,
  normalize_openai_auth_record.
- **P0b `internal/models` additions** — fingerprint GENERATION (DEVICE_PROFILES,
  COUNTRY_BROWSER_LOCALE, generate_register/team/exit_fingerprint, fingerprint_summary_text);
  random_profile + FIRST/LAST_NAMES; phone/error helpers (classify_phone_rejection,
  normalize_us_phone_for_form, exception_status) + typed errors (PhoneRejectedError,
  AccountDeactivatedError, AmountMismatchError, ProxyExitCheckError); PAYMENT_MODES +
  currency_for_country + COUNTRY_CURRENCY.

### Layer 1 — network helpers over tlsclient/proxychain (live-testable)
- **P1a proxy health detection** — detect_proxy_health_with_retry /
  detect_local_proxy_health_with_retry (ipinfo.io + chatgpt csrf + stripe probes) -> ProxyHealthResult.
- **P1b `internal/opll`** — payment-link synthesis: generate_opll_{paypal,gopay,hosted}_long_link,
  opll_checkout_from_url, opll_is_paypal_success_url. Big. Over tlsclient + 3-stage proxy chains.

### Layer 2 — browser foundation (THE crown jewel)
- **P2 `internal/browser`** — go-rod wrapper + anti-detection primitives (built on cmd/browserpoc):
  persistent+extension launch AND non-persistent launch; EvalOnNewDocument fingerprint injection
  (byte-faithful JS); React-safe native-setter fill; synthetic PointerEvent/MouseEvent clicks +
  real Mouse.MoveTo; session-fetch('/api/auth/session') across ALL pages; cross-origin iframe
  traversal (Turnstile); cookie+localStorage export/replay (storage_state substitute); IsClosed
  helper; DOMContentLoaded-scoped waits; window z-order hack (by PID, not image-path).

### Layer 3 — the worker clusters (depend on P0/P1/P2)
Port order (leaf-first): browser-fingerprint -> cloudflare-turnstile (highest risk) ->
core-register-login -> password-emailotp -> phone-sms -> about-you-form -> team-sso-oauth ->
paylink-trial -> orchestration (KEPT_REGISTER_BROWSER_SESSIONS + entry points + phone_provider bridge).

## Cross-cutting porting rules (from the map's risk findings)
- go-rod has NO `:has-text()` (Playwright-only) — match button text via JS/textContent enumeration.
- go-rod has NO flat frame list — find <iframe> el, el.Frame() -> *rod.Page, click there.
- React inputs: plain value= is reverted — MUST use native prototype value-setter + dispatch input/change/blur.
- Clicks are anti-detection: synthetic event sequences + real mouse coords, NOT el.Click().
- EvalOnNewDocument is per-target — must re-inject on popups/new tabs.
- OTP min_timestamp is unix-epoch-FLOAT-seconds across the mail boundary — unit must match exactly.
- Playwright waits domcontentloaded; go-rod WaitLoad waits full load — use readyState interactive/complete.
- Deadlines: wall-clock 600s loops + short per-probe timeouts (700/800ms) — use context deadline but
  keep the poll cadence identical to avoid racing SPA re-renders.
- DEAD CODE do-not-port: _fill_about_you_inputs_by_dom, _about_you_birth_date_from_values,
  _click_trial_claim_button inline block (returns early), _wait_cf_clearance (verify no caller).
- storage_state has no go-rod equivalent — reconstruct cookies + localStorage manually.
- KEPT_REGISTER_BROWSER_SESSIONS: process-global, concurrent-safe, keyed by lowercased email;
  parks live browsers open past worker return; nil-reassign ownership trick -> explicit 'parked' flag in Go.
- except->_has_chatgpt_session recovery exists ONLY in run/run_auth_only, not team/rt/relink.
