# API_SURFACE — the committed contract between packages

**Purpose.** Several agents port different packages in parallel. This file is the only place a
signature may be read from. **Do not invent signatures, do not guess field names, do not assume a
symbol exists because app.py has one.** If it is not here, it is not built yet — say so and stop,
or build it and update this file in the same commit.

**Scope.** Exported symbols only (`go doc -all ./internal/<pkg>`), because unexported symbols are not
a contract. Regenerate the spine with:

```sh
# 先按本机安装方式将 Go 加入 PATH
go list ./...
go doc -all ./internal/<pkg>
```

**Language convention.** Technical notes in English. User-facing literals, statuses, log text, group
names and error messages stay in the Chinese the codebase actually ships — they are compared against,
persisted, and shown to the operator; translating them breaks behaviour.

---

## 0. Day-one rules (read before writing any Go in this repo)

This is a line-by-line port of a 24.8k-line Python/Tkinter `app.py`.
"Idiomatic Go" is *not* the goal — behavioural identity is. Where Go's natural behaviour differs from
Python's, **Python wins** and the deviation is commented with the app.py line number.

### 0.1 Go-vs-Python traps this port keeps hitting

| Trap | What breaks | Rule |
|---|---|---|
| **RE2 `\s` is ASCII-only** | Rendered page text carries NBSP / U+2028 / U+0085. Phone-code page detection and `ClassifyPhoneRejection` silently missed their markers. | Use an explicit class: `[\s\p{Z}\x{0085}]`. See `models.reWS`, `worker.cfWhitespaceRE`, `worker.authWhitespaceRE`. |
| **Python `str.strip()` ≠ `strings.TrimSpace`** | `str.strip()` also eats U+001C–U+001F. | Use the package-local `pyStrip` (see `proxypool.NormalizeRouteMode`, `accounts/pyvalue.go`, `sessionconv/pyvalue.go`). |
| **Map iteration order is randomised** | Python dicts are insertion-ordered and that order is *load-bearing*: first-match payload walks, form-body field order (part of the Stripe signature), JSON output shape, UI combo order, and — worst — the Team workspace tie-break. | Never range a map to produce output or to pick a winner. Use the declared slice (`PaymentModeOrder`, `Roles`, `SortColumns`, `FormatOrder`, `ProviderProxyRoles`, `ModelledKeys`, `StatusFilterOptions`) or an ordered container (`sessionconv.OrderedMap`, `openai.DecodeOrderedJSON`). |
| **`json.Marshal` HTML-escapes `<`, `>`, `&`** | `json.dumps` does not. Exported documents are consumed byte-for-byte by other tools. | Encode through `sessionconv.DumpJSON` / `DumpCompactJSON` (an `Encoder` with `SetEscapeHTML(false)`). A `MarshalJSON` result is **re-compacted using the outer encoder's flag**, so `OrderedMap` alone is not enough. |
| **`encoding/json` sorts map keys** | On-disk key order differs from Python's. Tolerated only where no consumer reads by position (`openai.SummarizeChatGPTAccessToken`); everywhere else use `OrderedMap`. | |
| **`sort.Slice` is not stable** | Python's `sorted()` is stable *even with `reverse=True`*, so ties keep list order in both directions. | Always `sort.SliceStable`. See `accounts.SortAccounts`, `openai.accountTeamWorkspaceFromBackendPayload`. |
| **Python truthiness** | `x or default` fires on `""`, `0`, `[]`, `{}`, `None`, `False` — and on the **stripped** value in the `str(v or "").strip() or default` idiom, so `" "` falls back to the default. | Port the chain literally; do not collapse to `if x != ""`. See `phoneprovider.NormalizeSettings`, `settings.FromSnapshot`. |
| **`str.casefold()` ≠ `strings.ToLower()`** | Full Unicode case folding vs simple lowering. | Comparisons in `accounts`, `alias` and `logs` use casefold semantics. Byte-order comparison of casefolded UTF-8 *does* equal Python's code-point order, so `SortKey.Less` is safe. |
| **`fmt` of a float64 uses exponent notation** | `str(1712345678)` in Python is `"1712345678"`; Go's `%v` on `float64` gives `1.712345678e+09`, which corrupted a token id tail. | Decode numbers with `Decoder.UseNumber` (`sessionconv.ParseSessionRecord`) and stringify with the Python-`str()` helper, not `%v`. |
| **CPython rounds timestamps to the microsecond grid (half-to-even) first** | `accountTimestampFromUnixSeconds` was 1 ms low on some inputs. | Round to microseconds before formatting. |
| **`url.Values.Encode()` sorts** | Stripe request bodies and the OAuth authorize URL depend on insertion order. | Use the ordered form encoders (`opll.formPairs`, `TeamSSOFlow.PrepareBrowserOAuthURL`). |
| **Header map iteration randomises `HeaderOrderKey`** | Header order *is* part of the TLS/HTTP2 fingerprint; a random order made runs nondeterministic against Cloudflare. | Build header order from a slice (see `tlsclient.ChromeHeaders`). |
| **Injected JS fails silently** | Two payloads in the Python original were dead (a stray `}`; `\t\n\r` becoming real control chars inside a regex literal) and nobody noticed for months. | Every embedded JS payload is fed to `node --check` by `TestEmbeddedJSSyntax` in `internal/{worker,browser,openai}`. Add new payloads to that test. |
| **Python raises where Go returns a zero value** | A nil map / non-dict payload that would `AttributeError` in Python quietly produces "no fields" in Go. | Documented per-symbol below; do not "fix" these silently. |

### 0.2 go-rod traps

- **`Element.Timeout` inherits the page context.** An element found under a 700 ms page budget carries
  that 700 ms for its whole lifetime, so a multi-step click ladder gets one shared budget where Python
  gave each step its own. Call `CancelTimeout()` on the element (or re-derive) before a long step.
- **`Page.Element` RETRIES until its deadline.** Each *absent* selector costs the full timeout
  (~1.2 s × 9 selectors ≈ 11 s per Turnstile sweep), which starved the 45 s auto-solve loop of
  re-checks. Use the non-retrying lookup (`browser.findNow`) for "is it there right now?" probes.
- **No `:has-text()`** (Playwright-only). Playwright's `:has-text()` folds case *and* normalises
  whitespace on both sides; a raw `textContent.includes()` port silently skips CSS-uppercased or
  text-node-wrapped labels. Match through the JS helpers in `browser` / `worker`, not raw XPath.
- **No flat frame list** (`page.frames`). Find the `<iframe>` element, `el.Frame()` → `*rod.Page`, and
  recurse by **live URL** — the nested inner Turnstile frame and any src-less JS-navigated frame are
  otherwise never entered.
- **`EvalOnNewDocument` is per-target.** Re-register the fingerprint script on every popup/new tab;
  `Browser.NewPage` does this for you, a raw `rod.Page` does not.
- **`WaitLoad` waits for full load; Playwright waited `domcontentloaded`.** Overshoots on
  challenge/SPA pages. Use `Page.Navigate` / `Page.WaitDOMContentLoaded` (readyState
  interactive/complete).
- **`ForceClick` is a JS `.click()`** — untrusted, and it essentially *cannot fail*, so every fallback
  rung below it is unreachable and handlers gated on `isTrusted` / `pointerdown` never fire. Use it
  only inside cross-origin Turnstile frames (where coordinates are not aimable). For main-frame
  buttons use `Page.ForcePointerClick` (real trusted click, can fail).
- **React inputs revert a plain `Input()`.** Use the native-prototype value-setter path
  (`Page.ForceFill`) — *except* on the 6-box split OTP widget, where the extra change/blur dispatch
  makes the widget drop characters; that one uses a plain fill.
- **`storage_state()` has no equivalent.** rod carries cookies across contexts but not
  localStorage/sessionStorage, and the OpenAI session lives in **both**. Use
  `Browser.ExportStorageState` / `ApplyStorageState`; `sessionStorage` cannot cross a tab at all
  (`Page.SeedSessionStorage` injects it into one live tab).
- **Cancellation is cooperative.** A go-rod call is not interruptible mid-call. Check `ctx.Err()`
  between steps, exactly where the Python polled `stop_event`.

### 0.3 Money and safety rules — non-negotiable

1. **Never call a paid or live API from a test.** Tests inject fakes (`phoneprovider.SMSClient`,
   `ClientFactory`, `HTTPGetFunc`, `Sleep`). `phoneprovider.SMSClient` is an interface for exactly one
   reason: so tests cannot bill. Never widen it to the concrete client.
2. **`smsbower.Client.GetNumber` rents a real, billable number** on every successful call. The status
   transitions are what release or burn it: `1` = SMS requested, `6` = finish (charge it), `8` = cancel.
   `Sent` fires **only after OpenAI actually rendered the SMS-code form**; `Good` **only after the code
   was accepted**; `Bad` **only for post-submit failures**. Never reorder or "simplify" these.
3. **`openai.ChatGPTTeamSendInvite` creates a BILLABLE Team seat** and emails the invite immediately.
   No dry-run, no un-invite path. The workspace it lands in is decided by a document-order tie-break —
   see §`internal/openai` and `DecodeOrderedJSON`.
4. **The payment window can complete a real charge unattended.** `worker.Relink` /
   `PayLinkExtractor.ExtractPayLink` / `ExtractTrialShortLinkByClick` drive a live Stripe/PayPal
   checkout. `ui.App.StartJob` therefore documents *SPENDS MONEY* and the frontend must confirm first.
5. **A wrong currency is a wrong charge.** `models.CountryCurrency` was once missing both of app.py's
   `update()` passes and returned USD for GR/CY/HR/AE/IL/TR/ZA/… Regression-tested; do not "tidy" it.
6. **Parked browsers outlive their task** (`worker.ParkBrowser`). The caller must treat the browser as
   *not owned* afterwards (nil the local, or the user's logged-in window is closed under them). Never
   call `ExportStorageState` on a parked browser from a synchronous binding.
7. Live-check tools under `cmd/` are deliberately read-only where money is involved:
   `smsbowercheck` calls balance+price only and never `GetNumber`; `mailcheck` never deletes/moves/sends;
   `statecheck` never writes state.json.

### 0.4 Tooling facts

- **`go test -race` is BROKEN on this machine.** It fails with `0xc0000139` from the race-runtime
  loader on a trivial package too — MinGW-W64 8.1.0 predates what Go's race runtime needs. This is
  **environmental, not a code defect**. Concurrency is reviewed by hand and tested without the
  detector; say so in commit messages rather than claiming race-clean.
- `go build ./...` and `go vet ./...` are clean as of this writing. Keep them that way.
- Go must be available on `PATH`; verify the local installation with `go version`.

### 0.5 Layering

```
models  state  tlsclient  proxychain          (leaf: no internal deps beyond models)
  ├─ proxyhealth  proxypool  smsbower  alias  importer  accounts  settings  logs
  ├─ openai ──────────────┐
  ├─ sessionconv ─(openai, models)
  ├─ mail ─(models, tlsclient)
  ├─ opll ─(models, tlsclient)
  ├─ browser ─(models)
  ├─ phoneprovider ─(models, smsbower)   implements worker.PhoneProvider structurally
  └─ worker ─(all of the above)
       └─ ui ─(state, worker, models)   ← Wails boundary, IN FLUX
```

`alias`, `accounts`, `settings`, `logs`, `importer`, `proxypool`, `sessionconv` are **pure**: no UI,
no network, no state store. Keep them that way — they are the only testable layer.

---

## 1. Package index

| Package | Status | One-line contract |
|---|---|---|
| `internal/models` | stable | Data structs, dict round-trip, line parsers, fingerprint generation, typed errors, payment modes |
| `internal/state` | stable | Debounced atomic state.json persistence + split per-account session files |
| `internal/tlsclient` | stable | Chrome-impersonating HTTP client (the curl_cffi replacement) |
| `internal/proxychain` | stable | Local chaining proxy (local → dynamic), HTTP CONNECT + SOCKS5 |
| `internal/proxyhealth` | stable | Proxy exit geo + ChatGPT/Stripe reachability → `ProxyHealthResult` |
| `internal/proxypool` | **new** | The four manual proxy pools, rotation, and the 全走本地代理 gate |
| `internal/openai` | **grown** | Auth constants/PKCE + account-type inference + Team/K12 REST + `DecodeOrderedJSON` |
| `internal/opll` | stable | Pure-HTTP payment-long-link synthesis over a 3-stage proxy chain |
| `internal/smsbower` | stable | SMSBower handler_api client (**rents billable numbers**) |
| `internal/phoneprovider` | **new** | `worker.PhoneProvider` implementation: SMSBower + manual pool |
| `internal/mail` | stable | Cloud Mail / Hotmail-IMAP / Graph readers behind one `Reader` |
| `internal/alias` | **new** | +别名 and 域名邮箱 mailbox cloning, Cloud Mail runtime config, email lock |
| `internal/importer` | **new** | 导入账号 paste-box parser + the asymmetric upsert |
| `internal/accounts` | **new** | Account-table derived 状态, filter, search, sort |
| `internal/settings` | **new** | The 60 persisted settings keys, typed, round-tripping unmodelled keys |
| `internal/sessionconv` | **new** | The seven session export formats, byte-exact |
| `internal/logs` | **new** | Log classifier / router / ring-buffer store |
| `internal/browser` | stable | go-rod anti-detection layer |
| `internal/worker` | stable | The ported `OpenAIRegisterPayLinkWorker`: 5 entry points + 8 clusters |
| `internal/ui` | **IN FLUX** | Wails-bound `App`. Two agents are editing this and `frontend/` right now |

`cmd/` holds live-verification tools only (`browsercheck`, `browserpoc`, `mailcheck`, `proxycheck`,
`smsbowercheck`, `statecheck`, `storagecheck`, `tlspoc`). They are not part of the app.

---

## 2. internal/models

```go
const (
    AccountDefaultGroup     = "未分组"
    AccountAllGroup         = "全部"
    DefaultDomainMailDomain = "mail.example.com"
    TeamEmailDomain         = "students.example.edu"
)

var CountryBrowserLocale     map[string]string   // proxy exit country -> browser locale (21 entries)
var CountryCurrency          map[string]string   // 63 entries; INCLUDES both app.py update() passes
var OpenAIPhoneErrorReasons  map[string]string   // error code -> Chinese reason
var PaymentModeOrder         []string            // display order; iterate THIS, never PaymentModes
var PaymentModes             map[string]PaymentMode
```

### Types

```go
type MailAccount struct {
    Email, Password, ClientID, RefreshToken, Raw string
    AccountType string // default "free"
    Status      string
    OpenaiRT    string
    AuthPhoneNumber, AuthPhoneSMSURL string
    ReceiveMailbox, MailProvider     string
    CloudMailBase, CloudMailToken    string
    Group       string
    BrowserFingerprint *DeviceFingerprint
}
type PhoneEntry  struct { Number, SMSURL, Status, LastCode, LastError string; ReceiveCount int } // Status default "可用"
type PaymentCard struct { Card, Month, Year, CVV, Status string }                                 // Status default "未用"
type ProxyConfig struct { LocalProxy, DynamicProxy, ChainURL string }
type PaymentMode struct { Country, Currency, PaymentProvider string; TrialShortLink, ApplePayHosted bool }
type LogRecord   struct { Seq int; TimeText, Message, Email, Scope string } // Scope default "global"

type ProxyHealthResult struct {
    Success bool
    IP, Country, Region, City, Timezone, Org string
    ChatGPTStatus, StripeStatus int
    FailedStage, Error string
}
func (r ProxyHealthResult) Location() string
func (r ProxyHealthResult) Summary() string   // failure descriptor, or the space-joined success line

type DeviceFingerprint struct {
    UserAgent, Locale string; Languages []string; Timezone string
    ViewportWidth, ViewportHeight, ScreenWidth, ScreenHeight, OuterWidth, OuterHeight int
    DeviceScaleFactor float64
    HardwareConcurrency, DeviceMemory int
    Platform, Vendor string   // Vendor default "Google Inc."
    MaxTouchPoints int
}
func (f DeviceFingerprint) AcceptLanguage() string   // first verbatim, then q=0.9,0.8,… floored at 0.5
func (f DeviceFingerprint) ChromeMajor() string
func (f DeviceFingerprint) ChromeFull() string
```

### Serialization / parsing

```go
func AccountToMap(a MailAccount) map[string]any ; func AccountFromMap(v map[string]any) MailAccount
func PhoneToMap(p PhoneEntry) map[string]any    ; func PhoneFromMap(v map[string]any) PhoneEntry
func CardToMap(c PaymentCard) map[string]any    ; func CardFromMap(v map[string]any) PaymentCard
func FingerprintToMap(fp *DeviceFingerprint) map[string]any
func FingerprintFromMap(v map[string]any) *DeviceFingerprint

func ParseAccountLine(line string) (MailAccount, error)
func ParsePhoneLine(line string) (PhoneEntry, error)
func ParsePayPalPhoneLine(line string) (PhoneEntry, error)
func ParsePaymentCardLine(line string) (PaymentCard, error)
func NormalizeEmailAddress(value string) string
```

- `NormalizeEmailAddress` **does not lowercase**. Case is preserved in the stored address; callers
  lowercase separately for dedup/lookup keys (`accounts.KeyOf`, `logs.EmailKey`, `alias.AccountMailboxKey`).
- `*ToMap`/`*FromMap` are the only sanctioned bridge to `state.Store`'s `map[string]any` snapshot.
  Do not hand-build those maps.
- Prefer `importer.ParseLine` over `ParseAccountLine` for the 导入账号 box — see §internal/importer.

### Fingerprints

```go
func GenerateFingerprint(profiles []deviceProfile) DeviceFingerprint  // deviceProfile is UNEXPORTED: pass nil
func GenerateRegisterFingerprint() DeviceFingerprint                  // JP profile
func GenerateTeamFingerprint() DeviceFingerprint                      // US profiles
func GeneratePaymentFingerprint() DeviceFingerprint                   // JP profile
func GenerateFingerprintForLocale(locale string, languages []string, timezone string) DeviceFingerprint
func GenerateFingerprintForExit(exit ProxyHealthResult) (DeviceFingerprint, error)  // err when exit geo unusable
func FingerprintSummaryText(fp *DeviceFingerprint) string
func RandomProfile() (name string, birthdate string)  // age 25-34; year MUST stay inside about-you's 1950..2007
```

`GenerateFingerprintForExit` is the one to use — it makes the browser match its IP. Fall back to
`GenerateFingerprintForLocale("en-US", …, "UTC")` (app.py:9174) only when the exit geo is unavailable.

### Errors — all classified through `ExceptionStatus`

```go
func ExceptionStatus(err error, def string) string   // uses errors.As; works through wrapping

type ProxyExitCheckError    struct{ Msg, Status string } // NewProxyExitCheckError(msg)     — MUST propagate, never retried
type AccountDeactivatedError struct{ Msg, Status string } // NewAccountDeactivatedError()
type PhoneRequiredError     struct{ Msg, Status string }
type PhoneRejectedError     struct{ Msg, Status string } // NewPhoneRejectedError(msg)
type AmountMismatchError    struct{ TargetAmount, ActualAmount, StripeAmountSource string }
```

`AmountMismatchError` carries `StripeAmountSource` because the paylink error formatter appends it
(`PayLinkExtractor.OpllErrorText`). An amount mismatch **aborts immediately** and is never retried.

### Payment / phone helpers

```go
func CurrencyForCountry(country string) string        // mapped currency, else "USD"
func NormalizeUSPhoneForForm(phoneNumber string) string
func ClassifyPhoneRejection(message string) (status, text string)  // "" status = no match
func OpenAIPhoneErrorReason(code string) string
```

---

## 3. internal/state

```go
const SchemaVersion = 2

type Store struct {
    StateFile, DataDir, SessionDir string
    LoadedLegacy        bool
    MissingSessionFiles bool
    Warnings            []string
}
func New(stateFile, dataDir string) *Store   // dataDir defaults to <dir(stateFile)>/state_data
func (s *Store) Load() (map[string]any, error)
func (s *Store) Save(snapshot map[string]any, dirtySessionEmails map[string]bool, flush bool)
func (s *Store) LoadDeferredSession(emailAddr string) map[string]any
```

- The snapshot is an untyped `map[string]any` on purpose: byte-compatible with the state.json the
  **still-running Python app also writes**. Typed conversion belongs to `models` / `settings`.
- `Load()` rebuilds the split per-account session files under the key **`session_results`**, not
  `sessions`. Reading the wrong key returns 0 and looks identical to "no sessions" (this actually
  shipped once — see `ui.TestLoadSummaryAgainstRealState`).
- `Save` debounces 1.5 s and drops stale versions. `dirtySessionEmails == nil` means "all dirty".
  Pass `flush=true` only on task completion and app exit.
- `Save` never returns an error — failures land in `Warnings`.

---

## 4. internal/tlsclient

```go
func ResolveChromeProfile() (string, profiles.ClientProfile)

type Client struct { HTTP tls_client.HttpClient; ProfileName, UserAgent string }
func New(proxyURL string, timeoutSeconds int) (*Client, error)
func NewOrNil(proxyURL string, timeoutSeconds int) *Client       // nil on error
func (c *Client) SetProxy(proxyURL string) error                 // live swap
func (c *Client) ChromeHeaders() http.Header                     // ORDER is part of the fingerprint
func (c *Client) Do(method, url string, body io.Reader, extra http.Header) (int, []byte, error)
func (c *Client) DoSimple(method, url string, body []byte, header map[string]string) (int, []byte, error)
```

- This is the **only** sanctioned HTTP path to OpenAI/Stripe/Cloudflare-fronted hosts. A stock
  `net/http` client is blocked. It replaces Python's `curl_cffi(impersonate="chrome136")`.
- There is **no per-request timeout** in tls-client; the construction-time timeout stands in for
  Python's `timeout=30000`.
- Header order comes from a slice, never a map range.

---

## 5. internal/proxychain

```go
type LogFunc func(string)   // may be nil

type Server struct{ /* unexported */ }
func New(localProxy, dynamicProxy string, log LogFunc) *Server
func (s *Server) Start() error                        // no-op (URL stays "") when neither proxy is set
func (s *Server) URL() string                         // "" when no chaining is configured
func (s *Server) SetDynamicProxy(dynamicProxy string) // drops in-flight conns so new requests re-chain
func (s *Server) Close()
```

`URL()` is what goes into `models.ProxyConfig.ChainURL` and into `browser.LaunchOptions.ProxyServer`.
Chaining order, Basic/SOCKS5 auth and address types are preserved from the Python `ProxyChainServer`.

---

## 6. internal/proxyhealth

```go
type LogFunc func(string)   // nil allowed

func DetectProxyHealth(proxyURL string, timeoutSeconds int) models.ProxyHealthResult
func DetectProxyHealthWithRetry(proxyURL string, timeoutSeconds, attempts int, log LogFunc, label string) models.ProxyHealthResult
func DetectLocalProxyHealth(proxyURL string, timeoutSeconds int) models.ProxyHealthResult
func DetectLocalProxyHealthWithRetry(proxyURL string, timeoutSeconds, attempts int, log LogFunc, label string) models.ProxyHealthResult
```

- Never returns an error: failure is `Success=false` plus `FailedStage`/`Error`. `Summary()` of a
  failure begins with `检测失败`, which is exactly what `worker.PayLinkExtractor` gates on.
- Stage order short-circuits: ipinfo → ChatGPT csrf → Stripe. SOCKS5 proxies are fronted by an
  in-process HTTP chain first (as in Python) so system DNS / fake-ip does not skew the exit geo.
- Retry backoff is `min(2*attempt, 5)s`.
- This package has its own **unexported** minimal `normalizeProxyURL` for already-stored URLs. It is
  **not** interchangeable with `proxypool.NormalizeProxyURL` (the full UI-input parser).

---

## 7. internal/proxypool  *(new — UI_SPEC G5+G6)*

Ports `normalize_proxy_url` (app.py:2396), `parse_proxy_pool_text` (2421), `_rotate_proxy_pool_values`
(17316) and the 代理模式=全走本地代理 gate `_local_proxy_only_enabled` (16712).

```go
const (
    RouteModeDefault   = "照旧"
    RouteModeLocalOnly = "全走本地代理"
)
var RouteModeOptions = []string{RouteModeDefault, RouteModeLocalOnly}

type Role string
const (
    RoleRegister Role = "register"  // 注册/获取 Session 动态代理池   settings key: dynamic_proxies
    RoleCreate   Role = "create"    // 创建长链第一步代理池             settings key: payment_dynamic_proxy
    RoleFollowup Role = "followup"  // 创建长链后续代理池               settings key: followup_dynamic_proxy
    RoleApprove  Role = "approve"   // Approve 代理池                  settings key: approve_dynamic_proxy
)
var Roles = []Role{RoleRegister, RoleCreate, RoleFollowup, RoleApprove}   // iterate THIS, never a map
func (r Role) TitleBase() string
func (r Role) Title(count int) string   // "<base>（剩余 N）", full-width parens, floor 0

func NormalizeProxyURL(value string) string
func NormalizeProxyURLWithScheme(value, defaultScheme string) string
func ParseProxyPoolText(value string) []string
func ParseProxyPoolTextWithScheme(value, defaultScheme string) []string
func NormalizeRouteMode(value string) string   // anything unrecognised (incl. whitespace) -> 照旧, on read AND write
```

```go
type Pool struct{ /* unexported */ }
func NewPool(text string) *Pool
func (p *Pool) SetText(text string)
func (p *Pool) Text() string                    // persisted form: one normalized proxy per line, no trailing \n
func (p *Pool) List() []string                  // copy of current order
func (p *Pool) Peek() string                    // head, no rotation; "" when empty
func (p *Pool) Take() string                    // pop head, append to tail; "" when empty
func (p *Pool) TakeN(n int) []string            // n > len yields the whole pool ONCE and leaves order unchanged
func (p *Pool) Remaining() int                  // NOT gated by route mode — it describes the editor
func (p *Pool) Remove(proxyURL string) bool     // first match only (app.py:17342 stops after one hit)
func (p *Pool) RemoveAll(targets map[string]bool) int
```

```go
type Set struct{ /* unexported */ }
func NewSet() *Set
func (s *Set) Mode() string
func (s *Set) SetMode(value string) string      // normalizes; returns the mode actually stored
func (s *Set) LocalOnly() bool
func (s *Set) Pool(role Role) *Pool             // nil for an unknown role
func (s *Set) SetText(role Role, text string) ; func (s *Set) Text(role Role) string   // Text NOT gated
func (s *Set) List(role Role) []string          // GATED: empty under 全走本地代理
func (s *Set) Peek(role Role) string            // GATED
func (s *Set) Take(role Role) string            // GATED
func (s *Set) TakeN(role Role, n int) []string  // GATED
func (s *Set) TakeAuth(usePaymentProxyForRegister bool) string
func (s *Set) TakeFollowupOrCreate() string     // followup pool first, else the 第一步 pool
func (s *Set) Remaining(role Role) int          // NOT gated
func (s *Set) Remove(role Role, proxyURL string) bool
func (s *Set) RemoveEverywhere(proxyURLs []string) (map[Role]int, int)  // 清理无效代理 prunes ALL four pools
func (s *Set) Reuse(role Role) string           // GATED; "" means "fall back to the pool"
func (s *Set) SetReuse(role Role, value string) // register role accepted and ignored (loop-friendly)
func (s *Set) ReuseText(role Role) string       // NOT gated
func (s *Set) SetOnChange(fn func())            // fired after any mutation, with NO locks held
func (s *Set) Snapshot() Snapshot

type PoolView struct{ Role, Text string; Remaining int; Title string }   // json: role/text/remaining/title
type Snapshot struct{ Mode string; LocalOnly bool; Register, Create, Followup, Approve PoolView }
```

- The 全走本地代理 gate lives on `Set`, not on callers, deliberately: UI_SPEC G6 requires it to be
  unbypassable. A caller that forgot the check would leak a dynamic proxy into a local-only run.
- Provider-pool owners must ALSO consult `LocalOnly()`: `_provider_roles_needed_for_link`
  (app.py:16882) returns no roles at all in that mode and the manager is stopped.
- `Reuse` is only the *first half* of `_reuse_link_proxy_for_region` (app.py:16852). The region
  rewrite/inversion is UI_SPEC G22 and deliberately lives outside this package.
- `Snapshot()` is the `pools-updated` event payload (UI_SPEC §4.2) and replaces the four
  `remove-*-proxy` events and the `take-auth-proxy` cross-thread RPC.

---

## 8. internal/openai  *(grown — account.go, teamapi.go, sessionsummary.go, DecodeOrderedJSON are all new)*

### 8.0 ⚠ `DecodeOrderedJSON` — read this before touching any payload walker

```go
func DecodeOrderedJSON(data []byte) (any, error)
```

Decodes a JSON document **with object key order preserved**, so the payload walkers in this package
iterate members the way CPython does.

> **Anything whose result feeds a Team-workspace choice MUST be decoded through `DecodeOrderedJSON`.**
> `accountTeamWorkspaceFromBackendPayload` breaks a role-score tie by **document order**, and the
> workspace id it returns is **where a BILLABLE Team invite seat gets created**. A payload decoded with
> plain `json.Unmarshal` arrives as `map[string]any` and can only be walked in **sorted** order, so two
> team workspaces with the same role score select a *different* workspace than Python does.

Which symbols are affected:

| Symbol | Behaviour on an ordered `*teamObject` | Behaviour on a plain `map[string]any` |
|---|---|---|
| `InferAccountTypeFromPayload` | document order — matches Python exactly | sorted order; the **returned plan is still correct** (strict priority, booleans→"plus", free is free), only the `detail` string can name a different tying field |
| `MergeChatGPTBackendPlanSummary` | first PAID result wins and returns immediately | same rule, but "first" is order-dependent |
| `SummarizeChatGPTSessionPayload` | accepts either representation | accepts either representation |
| *workspace selection* (internal, via `DetectOpenAIAccountType`) | **correct** | **can pick the wrong workspace → wrong billable seat** |

### 8.1 Constants

```go
const ChatGPTBaseURL = "https://chatgpt.com"
const AuthBaseURL    = "https://auth.openai.com"

const (   // auth.openai.com REST endpoints
    AuthAuthorizeContinueURL = AuthBaseURL + "/api/accounts/authorize/continue"
    AuthEmailOTPSendURL      = AuthBaseURL + "/api/accounts/email-otp/send"
    AuthEmailOTPValidateURL  = AuthBaseURL + "/api/accounts/email-otp/validate"
    AuthWorkspaceSelectURL   = AuthBaseURL + "/api/accounts/workspace/select"
    AuthPhoneSendURL         = AuthBaseURL + "/api/accounts/add-phone/send"
    AuthPhoneOTPValidateURL  = AuthBaseURL + "/api/accounts/phone-otp/validate"
    AuthUserRegisterURL      = AuthBaseURL + "/api/accounts/user/register"
    AuthCreateAccountURL     = AuthBaseURL + "/api/accounts/create_account"
)
const (   // OAuth (PKCE)
    DefaultRedirectURI = "http://localhost:1455/auth/callback"   // DEAD on purpose — see AuthorizeRTFromBrowser
    DefaultClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
    AuthOAuthAudience  = "https://api.openai.com/v1"
    Auth0ClientHeader  = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9" <!-- gitleaks:allow，公开 Auth0 客户端元数据 -->
)
const (
    CurlCffiImpersonate = "chrome136"
    DefaultUserAgent    = "Mozilla/5.0 (Windows NT 10.0; …) Chrome/146.0.0.0 Safari/537.36"  // must match the tlsclient JA3 profile UA
)
const (
    CFAutoSolveTimeout              = 45   // seconds
    CFManualWaitTimeout             = 90   // seconds
    TurnstileSolverDefaultURL       = "http://127.0.0.1:8888"
    AuthProxyFailureRemoveThreshold = 2
)
const (
    DefaultPayPalExtensionDir = ""
    DefaultK12WorkspaceID     = "workspace-example"
)
const CFInterstitialDetectJS = `…`   // byte-for-byte from app.py; the pass/fail heuristic depends on it

var AuthOAuthTokenURLs   = []string{AuthBaseURL + "/api/oauth/oauth2/token", AuthBaseURL + "/oauth/token"}  // tried IN ORDER
var CFClearanceURLs      = []string{…}
var SMSBowerCountryIDs   = map[string]string{"BR":"73", "CO":"33", "JP":"182", "US":"187"}
var AccountPaidPlanTypes = map[string]bool{"plus":true, "team":true, "k12":true, "pro":true}
```

`AccountPaidPlanTypes` contains `"pro"` even though `ClassifyChatGPTPlanText` never *returns* it (Pro
folds into plus) — the set is also consulted with raw strings from the UI 套餐覆盖 combo, which offers Pro.

### 8.2 Pure helpers (auth.go)

```go
func RandomURLSafeString(length int) string          // max(1,length) random bytes -> urlsafe b64 -> sliced to exactly length
func PKCECodeChallenge(codeVerifier string) string   // base64url(sha256(verifier)), padding stripped
func DecodeJWTPayload(token string) map[string]any   // EMPTY MAP on any failure — never nil, never an error
func GetNestedRecord(payload map[string]any, key string) map[string]any  // {} when not an object
func FirstNonEmpty(values ...any) string             // Python str() semantics: no exponent, True/False, 0/false WIN
func OpenAIBrowserHeaders(extra map[string]string) map[string]string

type AuthRecord struct {   // json tags mirror normalize_openai_auth_record exactly
    AccessToken  string `json:"access_token"`
    AccountID    string `json:"account_id"`
    Disabled     bool   `json:"disabled"`
    Email        string `json:"email"`
    Expired      string `json:"expired"`
    IDToken      string `json:"id_token"`
    LastRefresh  string `json:"last_refresh"`
    RefreshToken string `json:"refresh_token"`
    Type         string `json:"type"`
    Websockets   bool   `json:"websockets"`
}
func NormalizeOpenAIAuthRecord(emailAddr string, payload map[string]any) (AuthRecord, error)
```

`NormalizeOpenAIAuthRecord` **fails loudly** on any missing field (access/refresh/id token, account_id,
exp). Callers must NOT fall through to another endpoint on that error — see `ExchangeBrowserCodeForToken`.

### 8.3 Cloudflare text matchers (cloudflare.go)

```go
func IsCloudflareChallengeText(text string) bool
func ExtractCloudflareChallengeURL(text string) string   // "" when nothing matches
```

`IsCloudflareChallengeText` and `CFInterstitialDetectJS` **jointly** decide pass/fail; drift between
them causes false passes or hangs. `ExtractCloudflareChallengeURL` keys on the OpenAI-build-specific
`cUPMDTk` var name and will silently stop matching when OpenAI rebuilds — the direct
`challenges.cloudflare.com` fallback is the resilient path.

### 8.4 Account type / plan inference (account.go)

```go
func AccountIsOpenAIRefreshToken(value string) bool   // trimmed, starts with "rt_" or "rt."
func AccountNormalizePayloadKey(key string) string    // lowercase then strip everything not [a-z0-9]
func ClassifyChatGPTPlanText(value string) string
func SummarizeChatGPTAccessToken(accessToken string) map[string]any
func ApplyInferredPlanToSummary(summary map[string]any, inferredPlan, inferredDetail, source string) map[string]any
func InferAccountTypeFromPayload(payload any) (string, string)   // (plan, detail)
func MergeChatGPTBackendPlanSummary(summary map[string]any, backendResults any) map[string]any
func RefreshOpenAIAccessToken(refreshToken, proxyURL string) (map[string]any, error)
func DetectOpenAIAccountType(refreshToken, proxyURL string) (accountType, detail, newRefreshToken string, err error)
```

- **`ClassifyChatGPTPlanText` branch order is the whole function** and must not be reordered or merged:
  1. `enterprise|business|team` → `"team"` (so "school team" is team, not k12)
  2. `k12|teacher|school` → `"k12"`
  3. `chatgptplusplan|plus|pro` → `"plus"` (Pro deliberately folds in; substring match means
     "product"/"professional" land here too)
  4. `chatgptfreeplan|noplan|free|none` → `"free"`
  5. otherwise `""`
  Normalization deletes only `_`, `-` and space — unlike `AccountNormalizePayloadKey`.
- `ApplyInferredPlanToSummary` always records `<source>_plan_type` / `<source>_plan_detail`, but only
  promotes to top-level `plan_type` when the new plan is paid OR the current one is empty/unknown/free.
  So a backend "plus" beats a stale "free" in the token, and a backend "free" never demotes a paid plan.
  **Divergence:** Python raises on a `None` summary; a nil Go map is silently replaced (returned).
- `MergeChatGPTBackendPlanSummary`: the **first result that infers a PAID plan wins and returns
  immediately** — a later "team" cannot beat an earlier "plus". Only the *first* free result is
  remembered, as a fallback. `backendResults` accepts `[]any`, `[]map[string]any` or nil.
- `SummarizeChatGPTAccessToken` returns a **mutable bag, not a struct**, because
  `ApplyInferredPlanToSummary` writes dynamic keys into it and the whole thing is persisted as the
  account's `access_summary`. Deviation: Go sorts the on-disk keys; no consumer reads by position.
- `RefreshOpenAIAccessToken` walks `AuthOAuthTokenURLs` in order, 3 attempts each, retrying **only** on
  a transient transport error or HTTP 5xx. Returns the raw token payload.
- `DetectOpenAIAccountType` returns the ROTATED refresh token when the grant issued one, else the input
  RT. The 刷新类型 caller writes it back to the account.

### 8.5 Session summary (sessionsummary.go)

```go
func SummarizeChatGPTSessionPayload(sessionPayload any, accessToken string) map[string]any
```

Three layers, in this order: (1) the accessToken claims, (2) `account_id`/`user_id` salvaged from the
`/api/auth/session` body **only when the token did not already carry them**, (3) whatever plan the full
payload walk infers, recorded under the `"session"` source key. `sessionPayload` is `any` because
Python's parameter is untyped. **Divergence:** Python's unguarded `session_payload.get("account_id")`
raises on a non-dict payload; Go contributes no id fields instead. Safe — all four call sites pass a
decoded dict.

### 8.6 Team / K12 REST  ⚠ SPENDS MONEY (teamapi.go)

```go
func K12RequestWorkspaceInvite(accessToken, workspaceID, proxyURL string) (int, string, error)
func ChatGPTTeamSendInvite(accessToken, accountID, targetEmail, proxyURL string) (int, string, error)
func TeamInviteFailureHint(status int, body string) string   // "" when no hint applies
func ChatGPTTeamLeaveWorkspace(accessToken, accountID, memberEmail, proxyURL string) (int, string, TeamLeaveDetail, error)
func TeamLeaveFailureHint(status int) string                 // "" when no hint applies

type TeamLeaveDetail struct {
    Role          string `json:"role"`
    MemberID      string `json:"member_id"`
    AccountUserID string `json:"account_user_id"`
    TokenUserID   string `json:"token_user_id"`
    UsersStatus   int    `json:"users_status"`
}
```

| Symbol | Contract |
|---|---|
| `ChatGPTTeamSendInvite` | **A successful call adds a BILLABLE Team seat** to the inviter's workspace and emails the invite immediately. No dry-run, no un-invite path (UI_SPEC row 60: "Invite only"). **Never call from a test; never call speculatively.** Returns the raw `(status, body)`; classification is the caller's job — 2xx → `Team邀请已发送`, else `Team邀请失败` (+ `TeamInviteFailureHint`). |
| `K12RequestWorkspaceInvite` | Empty POST to `/backend-api/accounts/{workspace_id}/invites/request` signed with the account's own AT. Raw `(status, body)`; caller classifies — 2xx → `K12请求成功`, else `K12失败`. A transport failure is `err != nil` with status 0. |
| `ChatGPTTeamLeaveWorkspace` | Three round trips: membership check (refuses for an Owner) → resolve member id (best effort) → DELETE. **Leaving releases the seat and invalidates the workspace session — the caller must refresh the session afterwards.** UI_SPEC G12 abbreviates this as `(token, proxy)`; that form is wrong, all four arguments are needed. |

---

## 9. internal/opll

Pure-HTTP payment-long-link synthesis. Three-stage proxy chain, verbatim from Python:

```
create   proxy -> POST chatgpt.com/backend-api/payments/checkout
followup proxy -> POST api.stripe.com/v1/payment_pages/{cs}/init
                  POST api.stripe.com/v1/payment_methods
                  POST api.stripe.com/v1/payment_pages/{cs}/confirm
                  GET  api.stripe.com/v1/payment_pages/{cs}   (poll)
approve  proxy -> POST chatgpt.com/backend-api/payments/checkout/approve
```

```go
type Checkout struct {
    CSID, ProcessorEntity, StripePublishableKey, BillingCountry, Currency string
    CheckoutURL string // only set by OpllCheckoutFromURL, never by OpllCreateCheckout
}
func OpllCreateCheckout(accessToken, country, currency, proxyURL string) (Checkout, error)
func OpllCheckoutFromURL(rawURL, country, currency string) (Checkout, error)

type LinkResult struct {   // json tags match the Python dict keys one-for-one
    CSID, ProcessorEntity, StripePublishableKey, BillingCountry, Currency, CheckoutURL string
    PaymentMethodCountry, PaymentMethodID, PaymentMethodType string
    StripeHostedURL, StripeRedirectURL, ProviderRedirectURL, LongURL string
    Fallback      bool    // PayPal succeeded on a DIFFERENT country combo than requested
    ProviderError string  // "; "-joined earlier combo failures
    StripeAmount, StripeAmountSource, TargetAmount, AmountCheck string
}

func GenerateOpllPaypalLongLink(accessToken, country, currency, createProxyURL, followupProxyURL, approveProxyURL, targetAmount string, forceLegacyPaypal bool) (*LinkResult, error)
func GenerateOpllPaypalLongLinkFromCheckout(accessToken string, checkout Checkout, followupProxyURL, approveProxyURL, targetAmount string, forceLegacyPaypal bool) (*LinkResult, error)
func GenerateOpllGopayLongLink(accessToken, country, currency, createProxyURL, followupProxyURL, approveProxyURL, targetAmount string) (*LinkResult, error)
func GenerateOpllHostedLongLink(accessToken, country, currency, createProxyURL, followupProxyURL, approveProxyURL, targetAmount string) (*LinkResult, error)

func OpllIsPaypalSuccessURL(value string) bool
func OpllIsNonRetryableLinkError(err error) bool
func OpllNonRetryableStatus(err error) string   // account-row status text
func OpllNonRetryableHint(err error) string     // long operator-facing explanation
func OpllShortError(detail string, limit int) string   // limit <= 0 -> Python's default 260

type PaymentMethodNotSupportedError struct{ PaymentMethod, MethodsSummary string }  // NEVER retryable

type TrialEligibility struct {
    Eligible bool; Status, Amount, AmountSource, Currency, Country, CheckoutSessionID, ProcessorEntity string
}
func DetectPlusTrialEligibility(accessToken, proxyURL, country string) (TrialEligibility, error)
```

- **Signature-fidelity arguments that are deliberately ignored** (kept because Python has them):
  `OpllCreateCheckout`'s `currency` (overwritten by `currency_for_country`), and
  `GenerateOpllHostedLongLink`'s `approveProxyURL` (hosted never approves).
- Proxy cascade inside the generators: followup defaults to create, approve defaults to followup.
- `GenerateOpllPaypalLongLink` walks `comboAttemptOrder` (DE additionally retries via US). **An amount
  mismatch aborts immediately and is never retried**; any other error moves to the next combo.
- `GenerateOpllPaypalLongLinkFromCheckout` does **not** mutate the caller's `Checkout` (Python did);
  the normalized country/currency come back on the `LinkResult`.
- `DetectPlusTrialEligibility` is a **money decision** and is deliberately inconsistent with its
  neighbour: it does set membership over `{"0","0.0","0.00"}` while `opll_apply_amount_check` does an
  exact-string compare, because Stripe spells the zero amount differently per processor entity.
  `country` defaults to `"US"` both when empty and when it normalises away.

---

## 10. internal/smsbower  ⚠ SPENDS MONEY

```go
const (
    DefaultAPIURL  = "https://smsbower.page/stubs/handler_api.php"
    DefaultService = "dr"
    DefaultCountry = "33"
)
const (
    StatusReadyToReceive = 1 // number received, SMS requested
    StatusRequestAnother = 3
    StatusFinish         = 6 // activation complete — CHARGES the rental
    StatusCancel         = 8 // cancel the activation — RELEASES it
)

type Error struct{ Msg string }                    // every user-facing failure wraps *Error
func IsTransientError(v any) bool                  // accepts an error OR a string, like the Python

type Number     struct{ ActivationID, Number string }
type PriceQuote struct{ Cost float64; Count int }
type PriceTier  struct{ Cost float64; Count int }

type Client struct{ /* unexported */ }
func NewClient(apiKey, apiURL, proxyURL string) (*Client, error)
func (c *Client) GetBalance(ctx context.Context) (string, error)
func (c *Client) GetNumber(ctx context.Context, service, country, maxPrice string) (Number, error)   // ⚠ RENTS A BILLABLE NUMBER
func (c *Client) GetPriceQuote(ctx context.Context, service, country string) (PriceQuote, error)
func (c *Client) GetPriceTiers(ctx context.Context, service, country string) ([]PriceTier, error)    // sorted asc by cost, count<=0 dropped
func (c *Client) GetStatus(ctx context.Context, activationID string) (status, value string, err error)
func (c *Client) SetStatus(ctx context.Context, activationID string, status int) (string, error)
func (c *Client) WaitForCode(ctx context.Context, activationID string, timeout, interval int) (string, error)
```

**Never call `GetNumber` from a test.** Go through `phoneprovider` and its injectable `SMSClient`.
`timeout`/`interval` are seconds; `ctx` cancellation aborts the poll early.

---

## 11. internal/phoneprovider  *(new — UI_SPEC G3; implements `worker.PhoneProvider`)*

⚠ **`Provider.Next` rents a billable number on every successful call.** Status transitions are what
release or burn it: `1` sent, `6` finish/charge, `8` cancel. Sending `6` for a number that never
delivered pays for nothing; skipping `8` leaves the rental hanging.

```go
const ( StatusAvailable = "可用"; StatusUnusable = "不可用"; StatusFrozen = "冻结"; StatusInUse = "使用中" )

var (   // exact Python ValueError texts — logged verbatim as "SMSBower 设置无效，改用手工手机号池: {exc}"
    ErrBadService  = errors.New("SMSBower 服务代码格式不正确")
    ErrBadCountry  = errors.New("SMSBower 国家 ID 必须是数字")
    ErrBadMaxPrice = errors.New("SMSBower 最高单价必须是大于 0 的数字，或留空")
)

func ParseReceiveLimit(text string) int                                   // max(0, int(v or 0)); every failure -> 0 (unlimited)
func IsFrozen(phone models.PhoneEntry, receiveLimit int) bool             // limit 0 = unlimited; comparison is >=
```

```go
type Settings struct{ Enabled bool; APIKey, Service, Country, MaxPrice string }  // MaxPrice stays a STRING
type Raw      struct{ Enabled bool; APIKey, Service, Country, MaxPrice, PhoneMaxReceiveCount string }
func NormalizeSettings(r Raw) (Settings, error)
func (r Raw) SMSBowerEnabled() bool
func (r Raw) SMSBowerSettings() (Settings, error)
func (r Raw) SMSBowerAPIKey() string
func (r Raw) PhoneReceiveLimit() int

type SettingsSource interface {   // read LIVE on every action, exactly like the Tk vars were
    SMSBowerEnabled() bool          // checked BEFORE validation, so a bad service code on a disabled provider is silent
    SMSBowerSettings() (Settings, error)
    SMSBowerAPIKey() string         // RAW, unvalidated — sent/good/bad/code keep working with an invalid service/country
    PhoneReceiveLimit() int
}
```

```go
type AuthPhoneLookup struct {
    Found  bool          // true only when the account exists AND has BOTH auth_phone_number and auth_phone_sms_url
    Number, SMSURL string
    Saved   models.PhoneEntry
    SavedOK bool
}
type Pool interface {
    AccountAuthPhone(email string) AuthPhoneLookup
    ReserveNext(email, requestedCountry string, receiveLimit int) (models.PhoneEntry, bool)
    MarkUnusable(number, smsURL, status, errText string, createMissing bool)
    RecordCode(number, code string, receiveLimit int)
}
type MemoryPool struct {
    OnPhonesUpdated  func()
    OnAccountUpdated func(email string)
    /* unexported */
}
func NewMemoryPool(accounts []*models.MailAccount, phones []*models.PhoneEntry) *MemoryPool
// + the four Pool methods, plus Phones() []models.PhoneEntry (snapshot copy for rendering)
```

`MemoryPool` stores **pointers** because Python mutated the shared objects in place — binding a number
to an account must be visible to everything holding it. `OnPhonesUpdated`/`OnAccountUpdated` are invoked
**under the lock** (so event order matches mutation order, as in Python) and must therefore be
non-blocking and must not call back into the pool.

```go
type SMSClient interface {   // narrow ON PURPOSE: tests must be able to fake it. NEVER widen to *smsbower.Client.
    GetNumber(ctx, service, country, maxPrice string) (smsbower.Number, error)
    GetPriceQuote(ctx, service, country string) (smsbower.PriceQuote, error)
    GetPriceTiers(ctx, service, country string) ([]smsbower.PriceTier, error)
    SetStatus(ctx, activationID string, status int) (string, error)
    WaitForCode(ctx, activationID string, timeout, interval int) (string, error)
}
type ClientFactory func(apiKey string) (SMSClient, error)
func DefaultClientFactory(apiKey string) (SMSClient, error)   // no proxy, matching Python's plain requests session
type HTTPGetFunc func(ctx context.Context, url string, timeout time.Duration) (string, error)
type LogFunc func(email, msg string)   // == self._emit_log(msg, email_addr)

type Config struct {
    Settings SettingsSource   // required
    Pool     Pool             // may be nil -> SMSBower-only
    Log      LogFunc
    NewClient ClientFactory   // default DefaultClientFactory
    HTTPGet   HTTPGetFunc     // default defaultHTTPGet; backs the manual pool's SMS-URL poll
    Context   context.Context // nil -> Background(); Python had no cancellation here at all
    Sleep     func(time.Duration)
}
type Provider struct{ /* unexported */ }
func New(cfg Config) *Provider
func (p *Provider) Next(email string, opts map[string]string) (map[string]string, error)  // NEVER returns an error
func (p *Provider) Sent(email string, phone map[string]string) error                      // ALWAYS returns nil
func (p *Provider) Code(email string, phone map[string]string) (string, error)
func (p *Provider) Good(email string, phone map[string]string) error
func (p *Provider) Bad(email string, phone map[string]string) error
func (p *Provider) AttemptCount(email string) int   // the "本账号第 N 次" counter
func (p *Provider) ResetAttempts()
```

- `Next` and `Sent` never fail on purpose: `worker` treats a non-nil error as a **hard abort of the
  whole registration**, and the Python branches could not raise (failures were swallowed into `{}` /
  a no-op). Keep it that way.
- `Bad`: for SMSBower both branches send status 8 = cancel; only the label and log line differ,
  because a network wobble is not the number's fault but the activation still has to be released.

---

## 12. internal/mail

```go
const DefaultEmailOTPLookbackSeconds = 300

type Log func(msg string)

type MailRecord struct {
    ID, Folder, Kind, Code, Subject, From, To, Date string
    MailTime float64; MailTimeISO string
    Snippet string; Body string `json:"body,omitempty"`
}
type DeactivationResult struct {
    Found bool; Count int; Latest *MailRecord; Matches []MailRecord
    Days, MaxMessagesPerFolder, ScannedMessages, AliasMismatchCount int
    CheckedAt string
}
type EmailSecurityInterruptError struct{ Message, Status string }  // mailbox locked by Microsoft
type CloudMailError struct{ Message string }

type Reader interface {
    Connect() error
    Close() error
    WaitForCode(ctx context.Context, minTimestamp float64, timeout, lookbackSeconds int) (string, error)
    ListFolders() ([]string, error)
    ListRecentMessages(folder string, maxMessages int, query string) ([]MailRecord, error)
    FetchMessage(folder, messageID string) (MailRecord, error)
    WaitForTeamInvite(ctx context.Context, minTimestamp float64, timeout int) (string, error)
    WaitForLink(ctx context.Context, keyword string, minTimestamp float64, timeout int) (string, error)
    ScanOpenAIDeactivationNotice(days, maxMessagesPerFolder int) (DeactivationResult, error)
}
func CreateMailReader(account *models.MailAccount, log Log, proxyURL string) (Reader, error)   // ← use this, not the concrete types

type HotmailOtpReader struct{ MailMode string; ScanFolders []string; /* … */ }
func NewHotmailOtpReader(account *models.MailAccount, log Log, proxyURL string) *HotmailOtpReader
type CloudMailReader struct{ MailMode string; ScanFolders []string; /* … */ }
func NewCloudMailReader(account *models.MailAccount, log Log, baseURL, token string) (*CloudMailReader, error)
type CloudMailClient struct{ BaseURL, Token string; /* … */ }
func NewCloudMailClient(baseURL, token string) (*CloudMailClient, error)
func CloudMailGenerateToken(baseURL, adminEmail, adminPassword string) (string, error)
func (c *CloudMailClient) ListMails(toEmail, keyword string, size int) ([]map[string]interface{}, error)
```

- **`minTimestamp` is unix-epoch FLOAT seconds** across the whole mail boundary. The unit must match
  exactly on both sides — see `worker.RegisterOTPLookback` (`now - 120`).
- `HotmailOtpReader` falls back IMAP(XOAUTH2) → Microsoft Graph → legacy Outlook REST, refreshing the
  access token from the refresh token as it goes.
- The `Log` sink is progress text only; parsing and error classification never go through it.

---

## 13. internal/alias  *(new — UI_SPEC G26)*

Pure. Nothing touches the UI, the state store or the network; callers own the account slice and decide
when to persist.

```go
const (
    MaxPlusAliasesPerMailbox = 4          // UI_SPEC §5.6.6 said UNKNOWN; the source says 4
    AccountEmailLockedGroup     = "邮箱锁定"
    AccountEmailLockedStatus    = "邮箱锁定"   // same literal as the group name
    AccountDomainMailMainGroup  = "域名邮箱主"
    AccountDomainMailChildGroup = "域名邮箱分"
    PlusAliasPendingStatus      = "别名待注册"
    DomainAliasPendingStatus    = "域名邮箱待注册"
)
var MicrosoftPersonalMailDomains = map[string]bool{"outlook.com":true,"hotmail.com":true,"live.com":true,"msn.com":true}

var (   // exact Python ValueError texts — the UI shows them
    ErrPlusAliasBadEmail       = errors.New("邮箱格式错误，无法生成 + 别名")
    ErrPlusAliasNoDigits       = errors.New("别名后缀必须包含数字")
    ErrPlusAliasDuplicate      = errors.New("随机别名重复过多，未生成")
    ErrDomainMailDomainFormat  = errors.New("域名格式错误，例如 mail.example.com")
    ErrDomainAliasNoReceiveBox = errors.New("缺少实际接收邮件的主 Outlook 邮箱")
    ErrDomainAliasDuplicate    = errors.New("随机域名邮箱重复过多，请重试")
    ErrCloudMailBaseURL        = errors.New("Cloud Mail Base URL 格式错误")
)
```

```go
func MailboxEmailForPlusAlias(emailAddr string) string   // strip "+tag" -> mother mailbox
func IsPlusAliasEmail(emailAddr string) bool
func AccountMailboxKey(emailAddr string) string          // mother mailbox, case-folded — groups an account with its aliases
func PlusAliasEmail(emailAddr, suffix string) (string, error)     // built from the MOTHER local part (an existing +tag is replaced, not nested)
func CountPlusAliasesForMailbox(accounts []models.MailAccount, emailAddr string) int
func CloneAccountForPlusAlias(account models.MailAccount, aliasEmail string) models.MailAccount
func GeneratePlusAliases(accounts, selected []models.MailAccount, count int) (created []models.MailAccount, errs []string)

func NormalizeDomainMailDomain(value string) (string, error)
func RandomDomainAliasEmail(targetDomain string, existingEmails map[string]bool, localLength int) (string, error)  // 0 -> Python's default 12
func CloneAccountForDomainAlias(account models.MailAccount, aliasEmail, receiveMailbox, mailProvider string) (models.MailAccount, error)
func DomainAliasReceiveMailbox(source models.MailAccount) string   // "" means the UI must prompt
func NewCloudMailDomainAccount(aliasEmail, password, cloudMailBase, cloudMailToken, group string) models.MailAccount

type CloudMailSettings struct{ Enabled bool; BaseURL, Token, Domain string }
func CloudMailSettingsFrom(baseURL, token string, enabled bool) (CloudMailSettings, error)
func ApplyCloudMailRuntimeConfig(accounts []*models.MailAccount, baseURL, token string, enabled bool)
func AccountUsesCloudMail(account *models.MailAccount, baseURL, token string, enabled bool) bool
func IsAccountEmailLocked(accounts []models.MailAccount, account models.MailAccount) bool
```

- `GeneratePlusAliases` mutates **nothing** — the caller appends `created` to its own list. `errs`
  holds user-facing skip/failure lines in Python's order and wording; the caller shows the first five.
  A non-positive `count` is Python's `if not count: return`.
- `AccountUsesCloudMail` is a **MUTATING read**: Python re-applies the runtime config first, so it can
  attach or clear Cloud Mail credentials as a side effect. Pass a pointer to the live account.
- `ApplyCloudMailRuntimeConfig`: an invalid base URL changes **nothing at all**, not even the strip
  branch (Python's exception escaped before both). That is why it parses the settings itself.
- `CloudMailSettings.Domain` is ALWAYS `models.DefaultDomainMailDomain` — app.py hard-codes it and
  ignores `settings.domain_mail_domain`. Hence no domain argument.
- `IsAccountEmailLocked`: one locked +alias locks every sibling. Status compared with `==`, **not**
  case-insensitively.
- `NewCloudMailDomainAccount` takes the password from the caller because
  `generate_openai_compatible_password` is ported as `worker.GeneratePassword` and `alias` must stay a
  leaf package.

---

## 14. internal/importer  *(new — UI_SPEC S14)*

Format: `email----password----client_id----refresh_token[----extra...]`, where each extra is a
`key=value` pair or a bare positional value sniffed by shape.

```go
const Separator = "----"

type Extras struct{ OpenAIRT, AuthPhoneNumber, AuthPhoneSMSURL, ReceiveMailbox, MailProvider, AccountType string }
func ExtractExtras(parts []string) Extras

type LineError struct{ Line int; Err error }
func (e LineError) Error() string

func ParseLine(line string) (models.MailAccount, error)
func ParseText(text string) ([]models.MailAccount, []LineError)   // bad lines collected, NOT fatal
func MergeInto(existing, imported []models.MailAccount, importGroup string) []models.MailAccount
```

- **`MergeInto` is deliberately asymmetric**: on an existing account the imported row does **not** get
  to change `account_type`, `status` or `group` (worker- and user-owned), while `openai_rt` and the
  phone/mailbox fields fall back to the old value only when the imported one is empty.
- The import parser accepts only `free`/`plus`/`team` for `type=` — **not** `k12`/`pro`, unlike the rest
  of the app. Verified against app.py; do not "fix".
- `ParseText` skips blank lines and continues past a failure, reporting the set at the end.

---

## 15. internal/accounts  *(new — UI_SPEC §1.6 as corrected by §7.1)*

Pure. The three app-level lookup tables are passed in as `Lookups` rather than reached for as globals,
so the whole file is testable with no UI, no store and no network.

```go
const ( GroupAll = "全部"; GroupDefault = "未分组" )
const (
    StatusFilterAll = "全部状态"; StatusFilterPending = "待处理"; StatusFilterSession = "有 Session"
    StatusFilterPlus = "Plus";   StatusFilterTeam = "Team";     StatusFilterLinked = "提链成功"
    StatusFilterFailed = "失败"
)
const ( ColumnEmail="email"; ColumnType="type"; ColumnStatus="status"; ColumnAttempts="attempts" )
const ( SortCustom="custom"; SortAsc="asc"; SortDesc="desc" )
const (   // derived statuses only; the ~90 worker strings pass through StatusText unchanged
    StatusLinkExtracted="长链已提取"; StatusSessionAcquired="Session已获取"; StatusSuccess="成功"
    StatusPending="待处理"; StatusFree="Free"; StatusK12Success="K12请求成功"
    StatusK12SuccessRefreshed="K12请求成功/Session已刷新"; StatusNeedRTWithAuthPhone="待获取RT(带授权手机号)"
    StatusSessionRefreshed="Session已刷新"; StatusK12SessionRefreshed="K12 Session已刷新"
    StatusPlusSessionRefreshed="Plus/Session已刷新"
)
var SortColumns        = []string{ColumnEmail, ColumnType, ColumnStatus, ColumnAttempts}  // iterate THIS
var SortLabels         = map[string]string{…}   // 邮箱 / 类型 / 状态 / 撞链次数  (fourth column is 撞链次数, NOT 次数)
var StatusFilterOptions = []string{…}           // picker order; a slice because Go maps randomise
```

```go
type Lookups struct {
    Results        map[string]any   // self.results        : email -> extracted long link
    SessionResults map[string]any   // self.session_results: email -> payload dict
    LinkAttempts   map[string]any   // self.link_attempt_counts: email -> int
}
func (lk Lookups) HasLink(email string) bool
func (lk Lookups) HasSession(email string) bool        // access_token / session_json test
func (lk Lookups) HasK12Success(email string) bool     // k12_status all-digits in [200,300)
func (lk Lookups) AttemptCount(email string) int       // max(0, int(v or 0))

type Filter struct{ Group, Status, Search string }     // zero value matches everything
type Row     struct{ Key, Email, Type, Status string; Attempts int }
type SortKey struct{ Text string; Num int }            // exactly ONE field is meaningful per column
func (k SortKey) Less(other SortKey) bool
```

```go
func Key(acc models.MailAccount) string          // canonical row identity: trimmed, lowercased email
func KeyOf(email string) string
func GroupOf(acc models.MailAccount) string      // account.group or 未分组
func SearchTerms(search string) []string         // strip, casefold, split on Python whitespace; terms are AND-ed
func StatusText(acc models.MailAccount, lk Lookups) string
func RefreshStatusText(email string, result map[string]any, lk Lookups) string   // result may be nil
func ContainsFailureWord(statusText string) bool
func MatchesStatusFilter(acc models.MailAccount, statusFilter string, lk Lookups) bool  // unknown filter passes everything
func Matches(acc models.MailAccount, f Filter, lk Lookups) bool
func Visible(accs []models.MailAccount, f Filter, lk Lookups) []models.MailAccount
func VisibleKeys(accs []models.MailAccount, f Filter, lk Lookups) []string
func VisibleIndices(accs []models.MailAccount, f Filter, lk Lookups) []int
func SortAccounts(accs []models.MailAccount, column, direction string, lk Lookups) []models.MailAccount
func SortKeyOf(acc models.MailAccount, column string, lk Lookups) SortKey
func RowOf(acc models.MailAccount, lk Lookups) Row
func Display(accs []models.MailAccount, f Filter, column, direction string, lk Lookups) []models.MailAccount
```

**`StatusText` precedence** (app.py:19057-19068) — five steps then two overlays, in this exact order:

1. `长链已提取` when `results[email]` is non-blank
2. `account.Status` when set (any of the ~90 worker strings)
3. `Session已获取` when the session payload carries `access_token`/`session_json`
4. `成功` when the email is merely PRESENT in `results`
5. `待处理`
6. overlay: `K12请求成功` / `K12请求成功/Session已刷新` when `k12_status` is 2xx **and** the status so far is
   in the k12-overlay whitelist
7. overlay: `待获取RT(带授权手机号)` when `OpenaiRT` is empty, both auth-phone fields are set, and the
   status is **exactly** `待处理` — step 7 can only ever see `待处理`, so a status rewritten by step 6
   never reaches it

Other contracts:

- **`失败` is a substring match** over 失败/错误/耗尽/停用/封禁/不可用/拒绝/超时 — not an enum (UI_SPEC §7.1 correction).
- `SortAccounts` is **stable**; `SortCustom` (or any unknown direction) keeps list order, an unknown
  column falls back to email. Returns a new slice.
- **`Lookups` maps are keyed by the RAW email**, exactly as Python keys them. Python is inconsistent
  about normalising those keys (UI_SPEC §7.4.11) and this package does **not** paper over it. Callers
  wanting the canonical key must normalise both sides themselves.
- **Row identity is `Key`, never an index.** `VisibleIndices` exists only for the manual drag-reorder
  path and is valid only until `accs` is reordered — never store it on a row. `SortKeyOf` deliberately
  deviates from Python's `(index, column)` signature for the same reason (UI_SPEC §0.3, §7.4.1).

---

## 16. internal/settings  *(new — UI_SPEC §3, all 60 keys)*

> **HARD REQUIREMENT.** The user's real state.json is shared with the **still-running Python app**.
> `ToSnapshot` must copy through every key it does not model, verbatim, at the top level, inside
> `"settings"`, and inside each `provider_proxy_configs` role.

```go
const SettingsKey = "settings"

const DefaultLocalProxy = "http://127.0.0.1:7890"
const ( ProxyRouteModeDefault = "照旧"; ProxyRouteModeLocalOnly = "全走本地代理" )
const ( LinkProxyRegionAuto = "自动(跟随支付地区)"; LinkProxyRegionAny = "不限" )
const ( DefaultAuthConcurrency = 10; MaxAuthConcurrency = 30; DefaultK12Concurrency = 1 )
const ( DefaultLinkRaceConcurrency = 1; MaxLinkRaceConcurrency = 30 )
const ( DefaultLinkProxyPrecheckLimit = 500; DefaultLinkProxyPrecheckConcurrency = 100; MaxLinkProxyPrecheckConcurrency = 300 )
const ( DefaultLinkAttemptLimit = 3; MinLinkAttemptLimit = 1; MaxLinkAttemptLimit = 10000 )
const ( DefaultProviderProxyDuration = 5; MinProviderProxyDuration = 1; MaxProviderProxyDuration = 120; DefaultProviderProxyRegions = "JP" )
const (
    DefaultPaypalExtensionDir   = ""
    DefaultK12WorkspaceID       = "workspace-example"
    AudioDefaultDeviceLabel     = "系统默认"
    SMSBowerDefaultService      = "dr"
    SMSBowerDefaultCountry      = "33"
    SMSBowerDefaultMaxPrice     = "0.07"
    TurnstileSolverDefaultURL   = "http://127.0.0.1:8888"
    DefaultDomainMailDomain     = "mail.example.com"
    DefaultCloudMailBase        = "https://cloud-mail.example.com"
    DefaultSessionConvertFormat = "sub2api"
)
const (
    AccountAllGroup = "全部"; AccountDefaultGroup = "未分组"
    AccountSortCustom = "custom"; AccountSortAsc = "asc"; AccountSortDesc = "desc"
    AccountStatusFilterAll = "全部状态"
)
const DefaultWorkspacePage = "workbench"
const ( UILayoutVersion = 4; DefaultMainSashRatio = 0.27; DefaultLogSashRatio = 0.5; DefaultBodySashRatio = 0.43
        MinMainSashRatio = 0.2; MaxMainSashRatio = 0.85; MinLogSashRatio = 0.2; MaxLogSashRatio = 0.8
        MinBodySashRatio = 0.2; MaxBodySashRatio = 0.8 )

var ModelledKeys               []string          // 60 keys, in app.py:14234-14297 WRITE order. Not in this list => copied through verbatim.
var ProviderProxyRoles         = []string{"create","followup","approve"}   // order drives dialog layout AND save order
var ProviderProxyRoleLabels    = map[string]string{"create":"第一步","followup":"后续","approve":"Approve"}
var ProxyRouteModeOptions      []string
var LinkProxyRegionOptions     []string          // auto, any, then the 21 "CC 中文名" labels in declaration order
var AccountSortColumns         = []string{"email","type","status","attempts"}
var AccountSortDirections      []string
var AccountStatusFilterOptions []string
var WorkspacePages             = []string{"workbench","mail","phone","proxy","payment","team","k12","actions","settings"}
var SessionConvertFormats      []string          // key order == the Python dict's insertion order
var SessionConvertFormatLabels map[string]string
var PaymentModeAliases         map[string]string // {name.replace("长链接","短链"): name}; built from models.PaymentModeOrder so it is deterministic
```

```go
type Settings struct { /* 60 json-tagged fields, tags are the ON-DISK key names; safe to hand to Wails */ }
func Defaults() Settings
func FromSnapshot(snapshot map[string]any) Settings
func ToSnapshot(s Settings, prior map[string]any) map[string]any

func (s Settings) CloudMailBaseURL() (string, error)
func (s Settings) ValidateCloudMail() error
func (s Settings) ValidateSMSBower() error
func (s Settings) ValidateProviderProxies() error

type ProviderProxyConfig struct {
    Enabled  bool   `json:"enabled"`
    Username string `json:"username"`
    Password string `json:"password"`
    Endpoint string `json:"endpoint"`
    Duration int    `json:"duration"`
    Regions  string `json:"regions"`   // RAW textarea content; parse with ParseProviderRegions
}
func DefaultProviderProxyConfig() ProviderProxyConfig
func (c ProviderProxyConfig) Validate() error          // a DISABLED role is never validated, exactly as in Python
func ParseProviderRegions(value string) ([]string, error)
```

`Settings` field groups (json tag → UI_SPEC section):

| Group | Keys |
|---|---|
| S7 | `payment_mode`, `target_amount`, `headless` |
| S17 proxy | `local_proxy`, `proxy_route_mode`, `dynamic_proxies`, `payment_dynamic_proxy`, `followup_dynamic_proxy`, `approve_dynamic_proxy`, `reuse_payment_proxy`, `reuse_followup_proxy`, `reuse_approve_proxy`, `link_proxy_region`, `require_japan_extract_proxy`, `register_with_payment_proxy`, `force_legacy_paypal`, `auth_concurrency`, `k12_concurrency`, `link_race_concurrency`, `link_proxy_precheck_limit`, `link_proxy_precheck_concurrency`, `link_attempt_limit` |
| S20 | `provider_proxy_configs` (map keyed by `ProviderProxyRoles`) |
| S16 PayPal | `payment_extension_dir`, `paypal_phone`, `paypal_card`, `paypal_sms_url`, `paypal_phone_pool`, `paypal_phone_pool_index` |
| Mail | `domain_mail_domain`, `cloud_mail_enabled`, `cloud_mail_base`, `cloud_mail_token` |
| S19 | `k12_workspace_id`, `session_convert_format`, `manual_email_otp` |
| S15 phone/SMS | `phone_max_receive_count`, `smsbower_enabled`, `smsbower_api_key`, `smsbower_service`, `smsbower_country`, `smsbower_max_price`, `turnstile_solver_enabled`, `turnstile_solver_url` |
| S18 sound | `success_sound_enabled`, `success_audio_device`, `pause_others_on_link_success` |
| S8/S9 table | `account_groups`, `account_group_filter`, `account_status_filter`, `workspace_page`, `account_sort_column`, `account_sort_direction` |
| Tk-only layout | `export_name_prefix`, `window_geometry`, `window_zoomed`, `ui_layout_version`, `main_sash_ratio`, `log_sash_ratio`, `body_sash_ratio` |

Contracts:

- `Defaults()` = the Tk `StringVar/BooleanVar/IntVar` initialisers (app.py:12337-12433). A key **absent**
  from the snapshot keeps its default, because Python guards most assignments with `if "key" in settings`.
- `FromSnapshot` also reads **`accounts`**, because Python folds every account's group into
  `account_groups` before validating `account_group_filter` (app.py:14059-14064).
- `ToSnapshot(s, prior)` — `prior` is the **FULL top-level state.json map**, not just the settings
  object. It is not mutated; the return is a fresh shallow copy at the two levels this package rewrites.
- **`ToSnapshot` does NOT refresh `updated_at`.** Python stamps it in `_build_state_snapshot`; in Go
  that belongs to whoever owns the clock, so the prior value is preserved verbatim.
- The Tk-only layout keys round-trip for compatibility, but the web layout must be driven from
  CSS/localStorage — do not lay out the webview from `main_sash_ratio`.

---

## 17. internal/sessionconv  *(new — UI_SPEC G23)*

Byte-exact export. Consumed by other tools, so key ORDER and HTML-escaping are part of the contract.

```go
const DefaultFormat = "sub2api"
const AxonHubPlaceholderRefreshToken = "__missing_refresh_token__"
var ErrMissingAccessToken = errors.New("缺少 accessToken")   // shown verbatim in the per-account skip list
var FormatOrder  = []string{"sub2api","cpa","cockpit","9router","codex","axonhub","codexmanager"}  // combobox order
var FormatLabels = map[string]string{…}
func NormalizeFormat(value string) string
func FormatLabel(value string) string
```

```go
type OrderedMap struct{ /* unexported */ }   // the Go stand-in for a Python dict literal
func NewOrderedMap() *OrderedMap
func (m *OrderedMap) Set(key string, value any) *OrderedMap        // re-set overwrites value, KEEPS position
func (m *OrderedMap) SetNotNil(key string, value any) *OrderedMap  // drops nil; keeps "" / 0 / false
func (m *OrderedMap) SetTruthy(key string, value any) *OrderedMap  // Python `if v` filter — drops "" too
func (m *OrderedMap) Get(key string) (any, bool)
func (m *OrderedMap) Keys() []string
func (m *OrderedMap) Len() int
func (m *OrderedMap) MarshalJSON() ([]byte, error)
```

⚠ `MarshalJSON` writes unescaped, but `encoding/json` **re-compacts the result with the OUTER
encoder's escapeHTML flag**. `<`, `>`, `&` only survive when dumped through `DumpJSON` /
`DumpCompactJSON` (or any `Encoder` with `SetEscapeHTML(false)`). A plain `json.Marshal` re-escapes them.

```go
type Converted struct {
    Email, Name, ExpiresAt string; AccessTokenExpiresAt int64
    CPA, Cockpit, CodexAuthJSON, CodexManager *OrderedMap
    NineRouter, AxonHub, Sub2APIAccount any   // *OrderedMap, or nil if strip_unavailable emptied it
}
func ConvertChatGPTSessionRecord(record map[string]any, sourceName string, now time.Time) (Converted, error)
func BuildSessionConversionDocument(converted []Converted, outputFormat string, now time.Time) any
func ParseSessionRecord(data []byte) (map[string]any, error)   // UseNumber — hand-built records must use json.Number/int64
func BuildSub2APIAccount(record map[string]any) *OrderedMap
func BuildSub2APIExport(records []map[string]any, now time.Time) *OrderedMap   // exported_at at SECOND precision
func AuthRecordMap(r openai.AuthRecord) map[string]any
```

- Pass the **zero `time.Time`** for Python's `now or datetime.now(utc)`.
- `BuildSessionConversionDocument`: sub2api gets an `{exported_at, proxies, accounts}` envelope; every
  other format returns the bare per-account payload — a single object for exactly ONE account,
  otherwise a JSON array. An unrecognised non-empty format falls through to the sub2api **member**
  WITHOUT the envelope (`key_map.get(fmt, "sub2apiAccount")`).

```go
func AccountExportLine(account models.MailAccount, namePrefix string) string
func AccountExportText(accounts []models.MailAccount, namePrefix string) string  // EMPTY list still yields "\n"
func SessionConversionZipEntryName(emailAddr, outputFormat string, usedNames map[string]bool) string  // nil = no dedup; a non-nil set IS MUTATED
func EmailKey(emailAddr string) string
func DumpJSON(value any) (string, error)          // json.dumps(..., ensure_ascii=False, indent=2) + "\n"
func DumpCompactJSON(value any) (string, error)   // separators=(",",":"), no trailing newline
func EncodeBase64URLJSON(value any) (string, error)
func StripUnavailable(value any) any              // 0 and false SURVIVE; an emptied LIST stays [], an emptied DICT becomes nil
func SyntheticCodexIDToken(emailAddr, accountID, planType, userID, expiresAt string) string  // "" with no account_id
func NormalizeISOTimestamp(value any) string
func TimestampFromUnixSeconds(value any) string   // goes through float(), so numeric STRINGS are accepted
func EpochSecondsFromValue(value any) int64
func UnixSecondsFromJWTExp(value any) int64
func ParseExpiredTime(value any) int64            // only a TRAILING "Z" is rewritten; no-offset values read as LOCAL time
func GetExpiresIn(expiresAt string, now time.Time) any     // nil for Python None so strip_unavailable can drop it; 0 is real
func GetAxonHubLastRefresh(expiresAt string, now time.Time) string
func ResolveOrganizationID(idClaims, accessClaims map[string]any) string
func IsOpenAIRefreshToken(value string) bool
func ClassifyPlanText(value string) string        // LOCAL COPY of openai.ClassifyChatGPTPlanText — keep in sync

type CPAGate struct{ Refresh bool; Refreshable []models.MailAccount; Note string }
func CPARefreshGate(outputFormat string, accounts []models.MailAccount, sessionRT map[string]string) CPAGate
```

`CPARefreshGate` is only the **pure** half of `_start_cpa_rt_refresh_for_conversion`: only CPA
pre-refreshes, and only when at least one selected account has an `rt_`/`rt.` token. The running-task
guard, the proxy chain and the worker thread belong to the task registry, not here.

`DumpJSON` known deviation: Go always escapes U+2028/U+2029 inside strings and Python does not. No
token/email/plan string carries those.

---

## 18. internal/logs  *(new — UI_SPEC G25)*

```go
const ( MaxLogRecordsPerView = 2000; MaxTotalLogRecords = 10000 )   // app.py:58-59
const ( ScopeGlobal = "global"; ScopeAccount = "account" )
const DefaultModule = "系统"
var KnownModules = []string{"系统","代理","认证","邮箱","手机","Session","支付链接","支付窗口","导出"}

type Level string
const ( LevelNormal="normal"; LevelError="error"; LevelSuccess="success"; LevelAttention="attention" )
func (l Level) TkTag() string   // the Tk text tag; "" for normal

type Record struct {
    Seq int `json:"seq"`; TimeText string `json:"ts"`; Message string `json:"message"`
    Email string `json:"email,omitempty"`; Scope string `json:"scope"`
    Level Level `json:"level"`; Module string `json:"module"`
}
func (r Record) ToModel() models.LogRecord   // drops Level/Module — use Record on the event path
type Entry struct{ Message, Email string }

func EmailKey(email string) string       // strip then lower-case
func InferEmail(message string) string   // leading "[user@host]" bracket of the RAW, unstripped message
func Route(message, email string) string // explicit email wins, else the bracket; "" = global pane
func Normalize(message string) string
func Classify(message string) (text, module string)
func Tag(message string) Level           // runs on the NORMALIZED message, i.e. after the module prefix
func FormatLine(r Record) string         // trailing newline included
```

```go
type Store struct{ /* unexported */ }
func NewStore() *Store
func (s *Store) Append(message, email string) Record
func (s *Store) AppendBatch(entries []Entry) []Record     // ONE lock acquisition per drain tick
func (s *Store) AllRecords() []Record                     // 10000-entry combined buffer
func (s *Store) GlobalRecords() []Record                  // 全局日志 pane
func (s *Store) AccountRecords(email string) []Record     // nil for an unknown address
func (s *Store) Selected() string ; func (s *Store) SetSelected(email string)
func (s *Store) Visible(r Record) bool
func (s *Store) SplitVisible(records []Record) (account, global []Record)   // account pane first, then global
func (s *Store) Counts() (total, global, accounts int)
func (s *Store) Seq() int
func (s *Store) SetNowFunc(f func() time.Time)            // TESTS ONLY
```

Python trap fixed here: the Tk queue path inferred the address from a leading `[a@b]` bracket, but
`App.log()` on the Tk thread went straight to `_append_log_record` and skipped that inference — the
same line could land in a different pane depending on which thread produced it. In this port **every**
log crosses the event path, so inference always runs; the queue behaviour wins.

`Record` is the `log` event payload of UI_SPEC §4.2. Batch on the Go side (~50 ms / 200 lines) and cap
the frontend buffers to the same 2000/10000 — a naive per-line emit drowns the webview.

---

## 19. internal/browser

go-rod anti-detection layer: launches Chromium with the palpay MV3 extension, injects a
device-fingerprint spoof + matching client-hint headers on every document, and provides the React-safe
fill / synthetic-click / session-probe / Turnstile-iframe primitives the worker clusters build on.

```go
func ResolveChromeBin() string                 // env CHROME_BIN wins; "" lets go-rod look up/download
func ExtensionManifestExists(dir string) bool
func FingerprintInitScript(fp models.DeviceFingerprint) string   // register via EvalOnNewDocument — PER TARGET
func FingerprintHeaders(fp models.DeviceFingerprint) map[string]string
func ForceClick(el *rod.Element) bool          // JS .click(): use ONLY inside cross-origin Turnstile frames

type LaunchOptions struct {
    Fingerprint  models.DeviceFingerprint   // REQUIRED: drives launch args AND per-page emulation
    Headless     bool
    ProxyServer  string   // == ProxyConfig.ChainURL; "" for direct
    ChromeBin    string   // "" -> ResolveChromeBin()
    ExtensionDir string   // "" -> none (register path); set -> load MV3 unpacked
    UserDataDir  string   // "" -> ephemeral; set -> persistent context
}
type Browser struct{ Rod *rod.Browser; /* unexported */ }
func Launch(opts LaunchOptions) (*Browser, error)
func (b *Browser) NewPage() (*Page, error)     // full emulation applied BEFORE any navigation. Navigate after.
func (b *Browser) Fingerprint() models.DeviceFingerprint
func (b *Browser) ClearCookies() error
func (b *Browser) Close()
func (b *Browser) HasChatGPTSession() bool     // fetches /api/auth/session across ALL chatgpt.com pages
func (b *Browser) ContextHasChatGPTPage() bool
func (b *Browser) HasCloudflareClearance() bool
func (b *Browser) ExportStorageState() (*StorageState, error)
func (b *Browser) ApplyStorageState(state *StorageState) error

type StorageState  struct{ Cookies []*proto.NetworkCookieParam `json:"cookies"`; Origins []OriginStorage `json:"origins"` }
type OriginStorage struct{ Origin string; LocalStorage, SessionStorage map[string]string }

type Page struct{ Rod *rod.Page; /* unexported */ }
func (p *Page) URL() string ; func (p *Page) IsClosed() bool ; func (p *Page) Close()
func (p *Page) Navigate(url string, timeout time.Duration) error    // waits DOMContentLoaded, not full load
func (p *Page) WaitDOMContentLoaded(timeout time.Duration) error
func (p *Page) ForceFill(el *rod.Element, value string) bool        // React-safe; never errors (matches the Python try/except)
func (p *Page) ForcePointerClick(el *rod.Element) error             // real TRUSTED click on a main-frame element; CAN fail
func (p *Page) ClickButtonByText(texts []string) (bool, string)     // real mouse click at the button centre
func (p *Page) ClickSubmitButtonByDOM() bool
func (p *Page) ClickTurnstileCheckbox() (bool, string)
func (p *Page) IsCloudflareChallengePage() bool
func (p *Page) VisibleInputs(selectors []string) []*rod.Element     // up to 12 per selector
func (p *Page) SeedSessionStorage(entries map[string]string) error
```

- `HasCloudflareClearance`: **only `expires>0 AND expires<now` counts as expired.** Session cookies
  (rod reports `Expires` −1 or 0) are VALID. Inverting this rejects a good clearance.
- `IsCloudflareChallengePage` stage order matters — on a live challenge the top-level body is often
  near-empty because the content lives in the iframe, so the injected JS carries the detection.
- `OriginStorage.SessionStorage` is captured but **not replayed** by `ApplyStorageState` (sessionStorage
  is tab-scoped). Use `Page.SeedSessionStorage` on the exact live tab.
- `ClearCookies` is normally a no-op (fresh profile per launch) but the preloaded extension can seed
  cookies before the flow starts.

---

## 20. internal/worker

The port of `OpenAIRegisterPayLinkWorker` (app.py:8846-12298). Nine files: `worker.go` (state, ctor,
teardown, parked-browser registry), `run.go` (the five entry points), and one file per cluster.

### 20.1 Construction and entry points

```go
type LogFunc             func(string)
type InputFunc           func(kind, email, prompt string) string   // "" when the user cancels
type FingerprintCallback func(email string, fp models.DeviceFingerprint)

type Config struct {
    Account       *models.MailAccount
    PaymentMode   string      // a key of models.PaymentModes
    TargetAmount  string
    Headless      bool
    RegisterProxy models.ProxyConfig
    ExtractProxy  models.ProxyConfig
    Log           LogFunc
    PhoneProvider PhoneProvider
    InputCallback InputFunc

    // CASCADE: create<-extract, followup<-create, approve<-followup.
    // A naive per-field default BREAKS the chain when only some are supplied.
    LinkCreateProxy, LinkFollowupProxy, LinkApproveProxy *models.ProxyConfig

    RequireJapanExtractProxy bool
    ForceLegacyPayPal        bool
    ManualEmailOTP           bool
    TrialClaimScoreFallback  bool   // OFF by default — the Python equivalent never actually ran
    SavedFingerprint    *models.DeviceFingerprint
    FingerprintCallback FingerprintCallback
    ExtensionDir string
}

type Worker struct {
    Account     *models.MailAccount
    Fingerprint models.DeviceFingerprint
    CurrentProxyHealth *models.ProxyHealthResult
    LinkCreateProxy, LinkFollowupProxy, LinkApproveProxy models.ProxyConfig
    ActiveRegisterPhone map[string]string
}
func New(cfg Config) *Worker

func (w *Worker) Run(ctx context.Context) (*SessionInfo, error)                    // register/login, keep the window, read the session
func (w *Worker) RunAuthOnly(ctx context.Context) error                            // register/login, keep the window, read nothing
func (w *Worker) RunTeam(ctx context.Context) (*AuthResult, error)                 // Team SSO signup -> refresh token
func (w *Worker) RunRegisterAndAuthorizeRT(ctx context.Context) (*AuthResult, error) // register, then an OAuth round-trip
func (w *Worker) Relink(ctx context.Context) (*PayLinkResult, error)               // ⚠ login on the register proxy, hand over to the extract proxy, mint a live payment link

func (w *Worker) Log(msg string)
func (w *Worker) PrepareFingerprintForProxy(proxy models.ProxyConfig, label string) (models.ProxyHealthResult, error)
func (w *Worker) NewBrowser(proxy models.ProxyConfig) (*browser.Browser, error)
func (w *Worker) LogBrowserProxyStatus(label string)
func (w *Worker) CleanupProfileDir(dir string)   // Windows holds file locks briefly after exit — retries with backoff
func CloseBrowser(b *browser.Browser)

type SessionInfo struct{ URL, AccessToken, SessionJSON, StorageStateJSON string }
type AuthResult  struct{ SessionInfo; OpenAIRT string `json:"openai_rt"` }   // url and storage_state_json are ALWAYS empty here
```

- `PrepareFingerprintForProxy` fixes the fingerprint to the exit geo **exactly once** and persists it
  onto the account.
- The `except -> _has_chatgpt_session` recovery (a late page error after a successful login is
  salvaged) exists **only in `Run` / `RunAuthOnly`** — not in team/rt/relink.
- `RunTeam` never preconnects a mail reader (Team SSO has no email OTP step), so its teardown has no
  reader close either.

### 20.2 Parked browsers (process-global)

```go
type KeptSession struct{ Browser *browser.Browser; DynamicProxy string }
func ParkBrowser(email string, b *browser.Browser, dynamicProxy string)   // closes any previous browser for that email
func TakeParkedBrowser(email string) *KeptSession
func CloseParkedBrowser(email string)
```

Keyed by the **lowercased** email. After `ParkBrowser` the caller must treat the browser as **NOT
owned** — mirror Python's nil-reassign trick by nilling the captured local, or the `defer close`
kills the user's logged-in window. Parked browsers are reaped only at app exit.

### 20.3 Register / login state machine (register.go)

```go
const RegisterLoopDeadline           = 600 * time.Second  // the outer budget of BOTH state machines
const RegisterPollInterval           =   2 * time.Second
const RegisterCFRecheckDelay         =   1 * time.Second
const RegisterAboutYouRetryThrottle  =  10 * time.Second  // TWO independent >=10s guards; both must pass
const RegisterOTPLookback            = 120 * time.Second  // otp_min_timestamp = now - 120

type RegisterFlow struct {
    Browser *browser.Browser; Page *browser.Page; Account *models.MailAccount; Log func(string)
    Auth *AuthURLBuilder; CF *CFSolver; Team *TeamSSOFlow; OTP *OTPHandler; AboutYou *AboutYouFiller; Phone *PhoneHandler
}
func NewRegisterFlow(b *browser.Browser, p *browser.Page, account *models.MailAccount, log func(string)) *RegisterFlow
func (f *RegisterFlow) Register(ctx context.Context) error
func (f *RegisterFlow) LoginExisting(ctx context.Context, autoCloudflare bool) error
```

**Branch order inside `Register`'s loop is verbatim Python and MUST NOT be reordered** — the SPA can
satisfy several probes at once (the about-you page renders inputs that also match the OTP selectors;
the phone page matches the generic continue ladder), so the first matching branch wins:

```
Cloudflare challenge -> route error -> Team workspace picker -> session probe
-> add-phone/phone-verification route -> phone form anywhere
-> password page -> about-you page -> email-verification page -> email form
```

Termination is a **VALID SESSION**, never a URL — the SPA reaches chatgpt.com before the session cookie
is usable. Poll cadence (2s/1s + sub-second probes) is tuned to the SPA's re-render timing; a tighter
loop reads a half-mounted DOM. Flag bookkeeping (`email_code_submitted`, `about_you_submitted`,
`about_you_submitted_at`, `about_you_submit_retry_at`) is selectively reset on nearly every branch:
resetting too much re-submits forever, too little gives up while the page is still progressing.
`ctx` only adds cancellability — it does not change the 600 s budget.

`LoginExisting` is deliberately **not** `Register` with a flag. It differs in three ways:
`autoCloudflare == false` disables auto-solving entirely (notify once via a latch, keep polling at 2 s,
clear the latch when the page is no longer a challenge so a LATER challenge notifies again); the phone
and password pages are **terminal errors** (this entry point must never spend a phone number or mutate
the password); there is no about-you branch at all.

`Phone` may be nil ("this run has no phone pool"). Every other collaborator is **required** and is
checked up front rather than degrading a branch to "not present", which would spin the loop to 600 s.

### 20.4 Core auth support (coreauth.go)

```go
const RouteErrorMaxRetries = 3
const RouteErrorRetryDelay = 5 * time.Second
var EmailInputSelectors = []string{`input[type="email"]`, `input[name="email"]`, `input[name="username"]`, `input[autocomplete="email"]`}  // PRIORITY ladder

type AuthURLBuilder struct {
    Page *browser.Page
    Browser *browser.Browser        // owns the cookie jar the auth POST must reuse
    Account *models.MailAccount
    Fingerprint models.DeviceFingerprint
    Client *tlsclient.Client        // MUST be bound to the SAME proxy as Browser, and MUST be Chrome-impersonating
    Log func(string)
}
func NewAuthURLBuilder(page *browser.Page, br *browser.Browser, account *models.MailAccount,
                       fingerprint models.DeviceFingerprint, client *tlsclient.Client, log func(string)) *AuthURLBuilder
func (a *AuthURLBuilder) CreateOpenAISigninURL() (string, error)   // screen_hint=signup
func (a *AuthURLBuilder) CreateLoginURL() (string, error)          // screen_hint=login
func (a *AuthURLBuilder) ChatGPTCSRFAndDevice() (string, string)   // either may be ""
func (a *AuthURLBuilder) DetectRouteError() string                 // 700ms budget; "" on any read failure
func (a *AuthURLBuilder) RetryRouteError() bool
func (a *AuthURLBuilder) ClickContinue() bool
func (a *AuthURLBuilder) FillEmailIfVisible() bool                 // returns whether the FORM was found
```

Playwright minted the signin URL through `context.request` — the browser's cookie jar, proxy and TLS
stack. Go has no shared request context, so cookies are read out of rod and attached by hand and
`Client` must already be on the same proxy chain. **Sending this POST from a plain net/http client
fails Cloudflare.** Callers of `RetryRouteError` must keep the cadence: at most `RouteErrorMaxRetries`,
`RouteErrorRetryDelay` between them, then raise the localized `OpenAI 页面错误…` failure.

### 20.5 Cloudflare / Turnstile (cloudflare.go)

```go
type CFSolver struct {
    Browser *browser.Browser; Page *browser.Page; Headless bool; Log func(string)
    LowerWindows    func(retries int)               // nil = no-op (no Go equivalent of the Win32 z-order demotion yet)
    HasAboutYouForm func(p *browser.Page) bool      // owned by the about-you cluster
    HasOTPInput     func(p *browser.Page) bool      // owned by the password/OTP cluster
}
func NewCFSolver(b *browser.Browser, p *browser.Page, headless bool, log func(string)) *CFSolver
func (s *CFSolver) IsCloudflareChallenge(text string) bool
func (s *CFSolver) ExtractCloudflareChallengeURL(text string) string
func (s *CFSolver) IsCloudflareChallengePage(p *browser.Page) bool   // nil/closed page reads as "no challenge"
func (s *CFSolver) HasCloudflareClearance() bool
func (s *CFSolver) ClickTurnstileCheckbox(p *browser.Page) bool
func (s *CFSolver) TryPassCloudflare(p *browser.Page, allowManual bool, reason string) bool   // p == nil -> primary page
func (s *CFSolver) HandleCloudflareChallenge(challengeHTML string) error
func (s *CFSolver) WaitAfterOTPSubmit(timeoutSeconds int) error      // 0 -> Python's default 20
func (s *CFSolver) PageTextSummary(p *browser.Page, maxLength int) string   // maxLength <= 0 -> 300
```

`TryPassCloudflare` shape, verbatim:

```
no challenge              -> true immediately
headless                  -> log + false  (HARD GATE: no human can solve it)
auto phase   45s: re-check, click at most once per 1.0s, sleep 0.6s
re-check after the auto deadline
allowManual == false      -> log + false
manual phase 90s: re-check, progress log every 10s, click EVERY iteration, sleep 1.0s
final re-check
```

The success heuristic is deliberately lenient (cf_clearance present **OR** the markers are gone) because
a solved managed challenge does not always materialise a cookie on the page's own origin.

`HandleCloudflareChallenge` opens a **fallback tab** when the primary navigation fails; that tab is
closed on **every** exit path so it never leaks into the parked browser session.

**Leaving `HasAboutYouForm`/`HasOTPInput` nil degrades `WaitAfterOTPSubmit` to URL-only detection**, which
returns early where Python kept polling. Wire them up in the assembled worker (`buildFlows` does, and
`TestBuildFlowsWiring` guards it — a nil hook compiles fine and only surfaces mid-registration,
possibly after a phone number has been paid for).

### 20.6 Password / email OTP (emailotp.go)

```go
func GeneratePassword() string   // 13 chars from the ambiguity-free alphabet + fixed "!A7" suffix

type OTPHandler struct {
    Page *browser.Page; Browser *browser.Browser; Account *models.MailAccount
    Reader mail.Reader          // may be nil: lazily created via mail.CreateMailReader(account, log, "")
    ManualEmailOTP bool
    InputCallback  func(kind, email, prompt string) string
    Log func(string)
    ClickContinue                  func() bool   // nil -> DOM tail only
    HasAboutYouForm                func() bool   // REQUIRED for the 1st negative guard of HasOTPInput
    LooksLikeRegisterPhoneCodePage func() bool   // REQUIRED for the 2nd negative guard of HasOTPInput
    WaitAfterOTPSubmit             func() error
    HandleCloudflareChallenge      func(challengeHTML string) error
}
func (h *OTPHandler) HasVisiblePassword() bool
func (h *OTPHandler) FillPasswordStep() error
func (h *OTPHandler) OpenAIPasswordForAccount() string
func (h *OTPHandler) HasOTPInput() bool
func (h *OTPHandler) SubmitEmailCode(minTimestamp float64) error
func (h *OTPHandler) ValidateEmailCodeAPI(code string) (string, error)   // returns continue_url (may be "")
func (h *OTPHandler) ReadEmailOTPCode(minTimestamp float64, timeout int) (string, error)
```

- **The two negative guards on `HasOTPInput` are MANDATORY**: the about-you page and the register
  phone-code page both render inputs matching the OTP selectors, and treating either as an email-OTP
  prompt derails the flow.
- `OpenAIPasswordForAccount` writes back to the account **only** on the EMPTY-password branch. A short
  imported password is NOT mutated — a longer OpenAI-usable variant is derived from
  `sha256("email:password")` while the import line keeps its original value.
- `SubmitEmailCode`: two attempts (600 s then 180 s for the code). When the first code is rejected as
  stale, the wait floor rewinds to `now-5` so the mail reader waits for a genuinely NEWER email.
- `ValidateEmailCodeAPI`'s inner 3-attempt loop is the **Cloudflare** retry ladder and is intentionally
  separate — a CF challenge must NOT consume an OTP attempt.
- `minTimestamp` is unix-epoch FLOAT seconds throughout.

### 20.7 Phone / SMS (phone.go)  ⚠ money-critical ordering

```go
type PhoneProvider interface {
    Next(email string, opts map[string]string) (map[string]string, error)  // opts carries {"country":"US"}
    Sent(email string, phone map[string]string) error
    Code(email string, phone map[string]string) (string, error)
    Good(email string, phone map[string]string) error
    Bad(email string, phone map[string]string) error                       // phone carries "error" and "status"
}

type PhoneHandler struct {
    ActiveRegisterPhone map[string]string   // nil until a number passed verification
    HasAboutYouForm     func() bool         // optional; only WaitAfterRegisterPhoneCodeSubmit consults it
}
func NewPhoneHandler(page *browser.Page, br *browser.Browser, provider PhoneProvider,
                     account *models.MailAccount, log func(string)) *PhoneHandler   // provider may be nil
func (h *PhoneHandler) HandlePhoneContinueIfVisible() (bool, error)   // (false, nil) = "nothing to do", not a failure
func (h *PhoneHandler) HasRegisterPhoneNumberForm() bool
func (h *PhoneHandler) ClickUsePhoneNumberContinue() bool
func (h *PhoneHandler) SelectUSPhoneCountry() bool
func (h *PhoneHandler) FillRegisterPhoneNumber(localNumber string) error
func (h *PhoneHandler) LooksLikeRegisterPhoneCodePage() bool
func (h *PhoneHandler) RegisterPhoneCodeInputs() []*rod.Element
func (h *PhoneHandler) WaitForRegisterPhoneCodeForm(timeout time.Duration) error
func (h *PhoneHandler) SubmitRegisterPhoneCode(code string) error
func (h *PhoneHandler) WaitAfterRegisterPhoneCodeSubmit(timeout time.Duration) error
func (h *PhoneHandler) ResetPhoneRegistrationForNextNumber() bool
```

**Provider ordering is enforced by `HandlePhoneContinueIfVisible` and must not be reordered:**

| Call | Fires when | If you get it wrong |
|---|---|---|
| `Next` | reserve the next usable number | error = **hard abort of the whole flow** (Python called it OUTSIDE the try); an empty map = pool exhausted |
| `Sent` | **only after OpenAI actually rendered the SMS-code form** (`WaitForRegisterPhoneCodeForm` is that gate) | firing earlier bills a number that never got an SMS |
| `Code` | blocks for the inbound code | |
| `Good` | **only after the code was submitted and accepted** (status 6 = charge it) | |
| `Bad` | **only for POST-submit failures** | a PRE-submit failure must abort the flow, not rotate |

Pool rotation caps at 30 numbers. Two fidelity fixes that must not regress: the continue ladder must
**reject disabled buttons** (Python's plain `click()` auto-waits for enabled and times out; counting a
disabled button as clicked burned up to 30 rented numbers where Python burns 0), and the 6-box OTP
widget uses a **plain fill**, not `ForceFill` (the extra change/blur dispatch makes it drop characters
and wastes an SMS that already arrived). Note the paylink cluster has the *opposite* rule for its
role button — both are correct, because the Python calls differ.

### 20.8 About-you profile form (aboutyou.go)

```go
type AboutYouFieldMeta struct{ Index int; Tag, Type, Name, ID, Placeholder, Autocomplete, Inputmode, AriaLabel, TestID, Label, Value string }
type AboutYouFill struct{ Index int; Kind, Value string }

type AboutYouFiller struct {   // four nil-safe hooks owned by OTHER clusters
    HasChatGPTSession          func() bool
    HasRegisterPhoneNumberForm func() bool
    HasVisiblePassword         func() bool
    ClickContinue              func() bool
}
func NewAboutYouFiller(page *browser.Page, log func(string)) *AboutYouFiller

func AboutYouSecondFieldKindFromContext(context string) string   // ORDER MATTERS: year-only cues before full-date cues (CJK 生年 / 生年月日)
func AboutYouSecondFieldValue(kind, birthYear, age, birthdate, context string) string
func AboutYouSecondFieldSelectors(kind string) []string          // ORDER IS PRIORITY
func AboutYouClassifyField(meta AboutYouFieldMeta) string        // name / birth_date / birth_year / birth_month / birth_day / age / ignore / unknown
func AboutYouValuesOK(values []string, secondKind string) bool   // years 1950..2007, age 18..100; a bare year never confirms a full birth_date
func ParseAboutYouBirthdate(birthdate string) (year, month, day string)

func (f *AboutYouFiller) HasAboutYouForm() bool
func (f *AboutYouFiller) FillAboutYou() error
func (f *AboutYouFiller) WaitForAboutYouInputs(timeout time.Duration) error
func (f *AboutYouFiller) AboutYouFieldMeta() []AboutYouFieldMeta
func (f *AboutYouFiller) AboutYouSecondFieldContext() string
func (f *AboutYouFiller) AboutYouSecondFieldKind() string
func (f *AboutYouFiller) AboutYouPlanFills(name, birthdate, birthYear, age string) []AboutYouFill
func (f *AboutYouFiller) FillAboutYouInputs(name, birthdate, birthYear, age string)   // NEVER fails the caller
func (f *AboutYouFiller) FillVisibleInputByKeyboard(index int, value string) error
func (f *AboutYouFiller) VisibleInputValues() []string
func (f *AboutYouFiller) AboutYouCurrentValuesOK() bool
func (f *AboutYouFiller) FocusAboutYouSubmitOrBody()
func (f *AboutYouFiller) FillFirstVisible(selectors []string, value string) bool
func (f *AboutYouFiller) ForceFillLocator(el *rod.Element, value string) bool
func (f *AboutYouFiller) SubmitAboutYou() (bool, error)
func (f *AboutYouFiller) AboutYouSubmitDone(beforeURL string) (bool, error)   // error only for the closed-page case
func (f *AboutYouFiller) ClickFinishCreatingAccount() bool
func (f *AboutYouFiller) ClickButtonByText(texts []string) bool
```

`FillAboutYouInputs` is a **4-tier fallback** — (1) mouse+keyboard by planned index, (2) DOM
native-setter fill by planned index, (3) selector-driven force fill, (4) keyboard retry — each tier
verified with `AboutYouValuesOK`. It never fails the caller: every failure degrades to a log line,
matching the Python contract that the operator can finish the form by hand.

`FocusAboutYouSubmitOrBody`'s 0.2 s tail and `FillAboutYou`'s 1.5 s settle are part of the
anti-detection cadence, not padding. A nil hook behaves as "false" / "not clicked", which only makes
`AboutYouSubmitDone` fall back to its URL and form checks.

### 20.9 Team SSO + OAuth (teamsso.go)

```go
type TeamSSOHooks struct {
    CreateSigninURL   func() (string, error)
    TryPassCloudflare func(reason string) bool
    DetectRouteError  func() string
    RetryRouteError   func() bool
    FillEmailIfVisible func() bool
}
type TeamSSOFlow struct{ Hooks TeamSSOHooks }
func NewTeamSSOFlow(b *browser.Browser, p *browser.Page, account *models.MailAccount, client *tlsclient.Client, log func(string)) *TeamSSOFlow

func (f *TeamSSOFlow) RegisterTeamSSO() error                                        // Hooks REQUIRED
func (f *TeamSSOFlow) WaitTeamSSOProgress(label string, timeout time.Duration) error // never fails on timeout; DOES error on the bad-gateway cap
func (f *TeamSSOFlow) RefreshBadGatewayIfVisible(refreshCount int, label string) (bool, int, error)
func (f *TeamSSOFlow) PageHasText(texts []string) bool     // CASE-SENSITIVE plain substring over body.innerText
func (f *TeamSSOFlow) SelectTeamWorkspaceIfVisible() bool
func (f *TeamSSOFlow) TeamOnboardingPending() bool
func (f *TeamSSOFlow) CompleteTeamOnboardingIfVisible() bool
func (f *TeamSSOFlow) ApproveSSOLoginIfVisible() bool
func (f *TeamSSOFlow) PrepareBrowserOAuthURL() (url, codeVerifier string)
func (f *TeamSSOFlow) AuthorizeRTFromBrowser() (openai.AuthRecord, error)
func (f *TeamSSOFlow) ClickCodexConsentIfVisible() bool
func (f *TeamSSOFlow) ExtractOAuthCallbackFromURL(callbackURL string) (OAuthCallback, error)
func (f *TeamSSOFlow) ExchangeBrowserCodeForToken(code, codeVerifier string) (openai.AuthRecord, error)
func (f *TeamSSOFlow) SessionPayloadFromRecord(record openai.AuthRecord) map[string]any

type OAuthCallback struct{ CallbackURL, Code string }
```

- **`AuthorizeRTFromBrowser` ends when the browser tries to navigate to the DEAD `localhost:1455`
  callback. That navigation ALWAYS fails (connection refused).** Success is detected by the page URL
  carrying the redirect-URI prefix, never by navigation success — the initial `Navigate` error is
  logged and swallowed. 180 s budget.
- `ExchangeBrowserCodeForToken` POSTs to each endpoint of `openai.AuthOAuthTokenURLs` **in order**;
  a `NormalizeOpenAIAuthRecord` failure is returned as-is and does **not** fall through to the next
  endpoint. It goes through the TLS-impersonating client (a page-side fetch from the dead
  localhost origin would be CORS-blocked), and it **must carry the browser cookies** — without
  `cf_clearance`, Cloudflare can challenge it.
- `PrepareBrowserOAuthURL` preserves Python's `urlencode(dict)` insertion order (`url.Values.Encode`
  would sort).
- `SelectTeamWorkspaceIfVisible` clicks with a **real mouse press at x = 92 % of the row width** with an
  80 ms press delay — `element.click()` lands on the legal/link text on the left and looks non-human.
  800 ms settle afterwards.
- `RegisterTeamSSO`'s three retry counters accumulate **across** loop iterations: bad-gateway refreshes
  cap at 8, route-error retries at 3, workspace clicks at 5; the approve click is a one-shot latch.
  `WaitTeamSSOProgress`'s bad-gateway counter is LOCAL to each call — that asymmetry is intentional.

### 20.10 Payment link / trial short link (paylink.go)  ⚠ SPENDS MONEY

```go
type AmountFields struct{ StripeAmount, StripeAmountSource, TargetAmount, AmountCheck string }
func AmountFieldsFromLinkResult(r *opll.LinkResult) AmountFields
type LinkProxyExits struct{ Create, Followup, Approve string }   // a struct, not a map: gating order must be deterministic

type PayLinkResult struct {
    URL, CheckoutURL, AccessToken, SessionJSON string
    LinkProxy, LinkProxyLabel, LinkProxyExit string
    LinkCreateProxy,  LinkCreateProxyLabel,  LinkCreateProxyExit  string
    LinkFollowupProxy, LinkFollowupProxyLabel, LinkFollowupProxyExit string
    LinkApproveProxy, LinkApproveProxyLabel, LinkApproveProxyExit string
    PaymentLinkType string
    AmountFields
}
type TrialLinkResult struct { URL, LongURL, ProviderRedirectURL, CheckoutURL, AccessToken, SessionJSON string; AmountFields }

type PayLinkExtractor struct {
    PaymentMode, TargetAmount string
    ForceLegacyPayPal, RequireJapanExtractProxy, TrialClaimScoreFallback bool
    LinkCreateProxy, LinkFollowupProxy, LinkApproveProxy models.ProxyConfig   // ALREADY cascade-resolved by worker.New
    LowerWindows func(retries int)   // optional; nil is a no-op
}
func NewPayLinkExtractor(b *browser.Browser, p *browser.Page, log func(string)) *PayLinkExtractor
func NewPayLinkExtractorFromWorker(w *Worker, b *browser.Browser, p *browser.Page) *PayLinkExtractor  // ← prefer this
func (e *PayLinkExtractor) Page() *browser.Page
func (e *PayLinkExtractor) TargetAmountText() string
func (e *PayLinkExtractor) OpllAmountFields(in AmountFields) AmountFields
func (e *PayLinkExtractor) OpllAmountLogText(emailAddr string, in AmountFields) string   // feed it the RAW opll fields
func (e *PayLinkExtractor) OpllErrorText(err error) string
func (e *PayLinkExtractor) DetectProxyExit(proxyURL string) string       // failure summary starts with "检测失败"
func (e *PayLinkExtractor) ProxyExitIsJapan(proxyExit string) bool
func (e *PayLinkExtractor) DetectLinkProxyExits(create, followup, approve string) (LinkProxyExits, error)
func (e *PayLinkExtractor) ExtractPayLink() (*PayLinkResult, error)
func (e *PayLinkExtractor) ExtractSessionInfo() (*SessionInfo, error)
func (e *PayLinkExtractor) ClickTrialClaimButton(beforeClick func()) bool
func (e *PayLinkExtractor) ExtractTrialShortLinkByClick(create, followup, approve, targetAmount string) (*TrialLinkResult, error)
func ClickTrialClaimButtonOnPage(p *browser.Page, beforeClick func(), log func(string), scoreFallback bool) bool
```

- `ExtractPayLink` retry envelope, faithfully preserved: **dual cap of 15 attempts AND 120 s elapsed**
  (the elapsed check *breaks* the loop, it does not raise), 4 s between attempts. A
  `models.ProxyExitCheckError` is **re-raised immediately and never retried**. Any error whose text says
  the page/target is gone becomes the fatal `浏览器被关闭，…提取已停止`.
- Per-mode success gate: PayPal/GoPay read `provider_redirect_url || long_url` and **require**
  `opll.OpllIsPaypalSuccessURL`; Apple Pay reads `long_url || stripe_hosted_url` and only requires
  non-empty; trial reads `provider_redirect_url || long_url || url` and requires
  `OpllIsPaypalSuccessURL`.
- `DetectLinkProxyExits` probes the three proxies **concurrently**, then gates in a deterministic order:
  every failed probe raises first (create → followup → approve), and only then does the Japan
  requirement apply, **and only to the CREATE exit**.
- **`PayLinkResult` proxy-field asymmetry is deliberate:** `LinkProxy`/`Link*Proxy` report
  `dynamic_proxy or local_proxy` (the chain URL is NEVER reported), while the URLs actually used for
  routing are `chain_url or local_proxy or dynamic_proxy`.
- `ExtractSessionInfo` preserves two Python quirks: the tab is **never closed** (Python leaked it) and
  the window-lowering hook fires in a deferred block whatever happens.
- `ExtractTrialShortLinkByClick`: three outcomes — a direct paypal.com landing (the browser URL IS the
  link, **no HTTP call at all**, `amount_check` is "skipped"/"unknown"); a pay.openai.com /
  checkout.stripe.com landing (rebuild via `OpllCheckoutFromURL` and continue over followup/approve);
  or neither within 60 s (error naming the current URL). `createProxyURL` is accepted for signature
  parity and never used. The country/currency handed to `OpllCheckoutFromURL` are **hard-coded
  "US"/"USD"** regardless of the configured mode — the promo checkout is always US/USD.
- `TrialClaimScoreFallback` defaults **off**: the equivalent Python pass is dead code (a JS
  SyntaxError killed it), so no production run has ever exercised it, and its lowest tier awards points
  to any visible element matching `/claim|get|start|upgrade|subscribe|continue|领取|免费/`. A wrong click
  lands on a *different plan's* checkout rather than failing cleanly.
- `ClickTrialClaimButton` delegates to the module-level clicker; the method body at app.py:12162-12241
  is unreachable dead code and is **not** ported.

---

## 21. internal/ui — ⚠ IN FLUX (do not build against this section)

> **Two other agents are editing `internal/ui/` and `frontend/` right now.** Everything below describes
> only what was on disk at the time of writing (`app.go`, `config.go`, `jobs.go` + tests, last touched
> 2026-07-26 21:00). **Signatures here WILL change.** Coordinate before adding a bound method; do not
> cite this section as a contract, and never edit these files from another task.
>
> **Already stale:** `internal/ui/bindings.go` (+ `bindings_test.go`) landed *while this document was
> being written* and is deliberately NOT documented here — it adds at least `EventPrompt`,
> `AccountFilter`/`AccountRow`/`AccountPage` + `ListAccounts`, `ImportResult` + `ImportAccounts`,
> `LoadSettings`/`SaveSettings`, `StartRegisterRequest` + `StartRegister`,
> `GenerateLinksRequest` + `GenerateLinks`, `StopAll`, `PromptRequest` + `AnswerPrompt`.
> Read the file, not this list. Re-run `go doc -all ./internal/ui` and rewrite this section once the
> UI agents land.

`internal/ui` is the Wails boundary: everything exported on `App` becomes callable from TypeScript, so
the method set **is** the UI's API. Keep it small and explicit; do not expose internal structs whose
shape we would then be unable to change.

```go
const EventLog = "log"    // every backend log line
const EventJob = "job"    // a JobView on create or state change

type App struct{ /* unexported */ }
func New() *App                                     // paths default to the Python tool's REAL state; env-overridable
func (a *App) Startup(ctx context.Context)          // wired to Wails OnStartup
func (a *App) Log(line string)                      // safe before Startup (drops)
func (a *App) Environment() Env
func (a *App) LoadSummary() (StateSummary, error)
func (a *App) StartJob(kind string, email string) (string, error)   // ⚠ SPENDS MONEY — frontend must confirm
func (a *App) CancelJob(id string) error
func (a *App) ListJobs() []JobView                  // newest first

type Env          struct{ GoVersion, StateFile, DataDir string; StateOK bool }
type StateSummary struct{ Accounts, Sessions int; SettingsKeys []string; SchemaFile string }

type JobKind string
const (
    JobRegister      JobKind = "register"        // Worker.Run
    JobAuthOnly      JobKind = "auth_only"       // Worker.RunAuthOnly
    JobTeam          JobKind = "team"            // Worker.RunTeam
    JobRegisterAndRT JobKind = "register_and_rt" // Worker.RunRegisterAndAuthorizeRT
    JobRelink        JobKind = "relink"          // Worker.Relink
)
type JobStatus string
const ( StatusRunning="running"; StatusSucceeded="succeeded"; StatusFailed="failed"; StatusCancelled="cancelled" )
type JobView struct{ ID string; Kind JobKind; Email string; Status JobStatus; Error, Started, Finished string }
```

Current state, honestly:

- `StartJob` returns as soon as the goroutine is running; it never waits. `CancelJob` cancels the
  context the five worker entry points already thread, so it unwinds the run rather than orphaning a
  browser (the Tk version could only set a flag).
- `ListJobs` sorts by creation **sequence**, not timestamp — two jobs started in the same second have
  identical RFC3339 stamps.
- `config.go` (unexported) maps `settings.*` onto `worker.Config`. **Multi-hop proxy chains are refused,
  not approximated**: `chain_url` assembly and `proxy_route_mode` selection are not ported into the UI
  layer, so anything but the degenerate single-hop case errors out naming the offending key.
- **Not wired yet, each a real gap rather than an oversight:** `PhoneProvider` (needs an
  `internal/phoneprovider` adapter — the package now exists, so this is connectable),
  `InputCallback` (needs a frontend prompt round-trip: emit `prompt-request{id,kind,email,prompt}` +
  a bound `AnswerPrompt(id, value)`), `SavedFingerprint`/`FingerprintCallback` persistence, and
  `Link*Proxy`.
- `config.go` does **not** use `internal/settings` — it reads the raw snapshot map with local
  `sStr`/`sBool` helpers. That predates `internal/settings` landing and should migrate.
- Nothing here uses `internal/accounts`, `internal/logs`, `internal/proxypool` or `internal/sessionconv`
  yet. UI_SPEC §4.2's event contract (`status`, `account-updated`, `result`, `pools-updated`, …) is
  **not implemented** — only `log` and `job` exist.
- `app_test.go` deliberately runs against the user's **REAL** state.json, because a binding that returns
  a zero value looks identical to a working one on screen. That test already caught a wrong snapshot key.

---

## 22. cmd/ — live-verification tools (not part of the app)

| Tool | What it proves | Safety |
|---|---|---|
| `cmd/tlspoc` | tls-client reproduces a Chrome JA3/JA4/HTTP2 and differs from Go's stdlib; passes auth.openai.com | read-only |
| `cmd/browserpoc` | persistent user-data-dir + unpacked MV3 extension + pre-script fingerprint injection | local only |
| `cmd/browsercheck` | launch + per-page emulation + init-script pipeline end-to-end (local file, no network) | local only |
| `cmd/storagecheck` | the `storage_state` substitute round-trips cookies + localStorage across two browsers | local HTTP origin |
| `cmd/proxycheck` | the chain server tunnels through two in-process upstreams to a real HTTPS target | self-contained |
| `cmd/mailcheck` | real Hotmail refresh-token → Microsoft AT → IMAP XOAUTH2 → list folders/messages | **READ-ONLY**: never deletes/moves/sends |
| `cmd/smsbowercheck` | the smsbower client against the live API | **READ-ONLY**: balance + price only, **never `GetNumber`** |
| `cmd/statecheck` | the Go state+models port loads and round-trips the real Python state.json | **never writes** |

---

## 23. Known duplication — resolve before it drifts

These pairs are deliberate (leaf packages must not import each other) but they *will* drift. If you
change one, change the other in the same commit.

| Concept | Copies |
|---|---|
| Plan classification | `openai.ClassifyChatGPTPlanText` ↔ `sessionconv.ClassifyPlanText` (the latter is documented as a copy) |
| Account line parsing | `models.ParseAccountLine` ↔ `importer.ParseLine` (importer accepts only free/plus/team for `type=`) |
| 全部 / 未分组 | `models.AccountAllGroup`/`AccountDefaultGroup` ↔ `settings.AccountAllGroup`/`AccountDefaultGroup` ↔ `accounts.GroupAll`/`GroupDefault` |
| Status filter values | `settings.AccountStatusFilterOptions` ↔ `accounts.StatusFilterOptions` |
| Sort columns / directions | `settings.AccountSortColumns`/`AccountSortDirections` ↔ `accounts.SortColumns`/`Sort*` |
| Route modes | `settings.ProxyRouteMode*` ↔ `proxypool.RouteMode*` |
| Session-convert formats | `settings.SessionConvertFormats`/`Labels` ↔ `sessionconv.FormatOrder`/`FormatLabels` |
| K12 workspace / Turnstile URL / PayPal extension dir | `settings.*` ↔ `openai.DefaultK12WorkspaceID`/`TurnstileSolverDefaultURL`/`DefaultPayPalExtensionDir` |
| SMSBower defaults | `settings.SMSBowerDefault*` ↔ `smsbower.Default*` |
| +alias mailbox helpers | `alias.MailboxEmailForPlusAlias`/`IsPlusAliasEmail` ↔ unexported copies inside `internal/mail` |
| Proxy URL normalization | `proxypool.NormalizeProxyURL` (full UI-input parser) ↔ `proxyhealth.normalizeProxyURL` (minimal, for stored URLs) — **NOT interchangeable** |
