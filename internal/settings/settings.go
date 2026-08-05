// Package settings models the 60 persisted keys under state.json's "settings"
// object (UI_SPEC §3) and converts between that untyped snapshot and a typed
// Go struct.
//
// 参考实现为旧版 Python/Tkinter 的 app.py：
//   - load  : GUI.load_state          app.py:14026-14213
//   - save  : GUI._build_state_snapshot app.py:14225-14299
//
// Where Go's natural behaviour differs from Python's, Python wins; every such
// spot carries a comment citing the app.py line.
//
// HARD REQUIREMENT: the user's real state.json is shared with the still-running
// Python app. ToSnapshot must therefore copy through every key it does not
// model, verbatim, at both the top level and inside "settings".
package settings

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// Defaults, mirroring the module-level constants in app.py.
const (
	// app.py:12340
	DefaultLocalProxy = "http://127.0.0.1:7890"

	// app.py:282-284
	ProxyRouteModeDefault   = "照旧"
	ProxyRouteModeLocalOnly = "全走本地代理"

	// app.py:491-492
	LinkProxyRegionAuto = "自动(跟随支付地区)"
	LinkProxyRegionAny  = "不限"

	// app.py:274-276
	DefaultAuthConcurrency = 10
	MaxAuthConcurrency     = 30
	DefaultK12Concurrency  = 1

	// app.py:12354, app.py:17029-17033
	DefaultLinkRaceConcurrency = 1
	MaxLinkRaceConcurrency     = 30

	// app.py:285-287
	DefaultLinkProxyPrecheckLimit       = 500
	DefaultLinkProxyPrecheckConcurrency = 100
	MaxLinkProxyPrecheckConcurrency     = 300

	// app.py:12357, app.py:12473-12477
	DefaultLinkAttemptLimit = 3
	MinLinkAttemptLimit     = 1
	MaxLinkAttemptLimit     = 10000

	// app.py:958-964 (ProxyProviderConfig field defaults) and app.py:986-987
	DefaultProviderProxyDuration = 5
	MinProviderProxyDuration     = 1
	MaxProviderProxyDuration     = 120
	DefaultProviderProxyRegions  = "JP"

	// app.py:66-67, 72, 82-84, 139, 303-304
	// 发布版不能默认依赖开发机盘符。留空表示不加载扩展；需要 PayPal/
	// Stripe 自动填充时，由用户在支付资料页选择本机解压后的扩展目录。
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

	// app.py:296-297, 305-307, 316
	AccountAllGroup        = "全部"
	AccountDefaultGroup    = "未分组"
	AccountSortCustom      = "custom"
	AccountSortAsc         = "asc"
	AccountSortDesc        = "desc"
	AccountStatusFilterAll = "全部状态"

	// app.py:12424 / app.py:14012-14021
	DefaultWorkspacePage = "workbench"

	// app.py:54 and app.py:14042-14049
	UILayoutVersion      = 4
	DefaultMainSashRatio = 0.27
	DefaultLogSashRatio  = 0.5
	DefaultBodySashRatio = 0.43
	MinMainSashRatio     = 0.2
	MaxMainSashRatio     = 0.85
	MinLogSashRatio      = 0.2
	MaxLogSashRatio      = 0.8
	MinBodySashRatio     = 0.2
	MaxBodySashRatio     = 0.8
)

// ProxyRouteModeOptions mirrors PROXY_ROUTE_MODE_OPTIONS (app.py:284).
var ProxyRouteModeOptions = []string{ProxyRouteModeDefault, ProxyRouteModeLocalOnly}

// linkProxyRegion is one entry of LINK_PROXY_REGION_NAMES (app.py:493-515).
// Python dicts preserve insertion order and the combo box is built by iterating
// that dict; a Go map iterates randomly, so the order is pinned in a slice.
type linkProxyRegion struct{ Code, Name string }

var linkProxyRegionNames = []linkProxyRegion{
	{"US", "美国"}, {"BR", "巴西"}, {"JP", "日本"}, {"NL", "荷兰"}, {"DE", "德国"},
	{"FR", "法国"}, {"GB", "英国"}, {"CA", "加拿大"}, {"AU", "澳洲"}, {"ID", "印尼"},
	{"SG", "新加坡"}, {"TH", "泰国"}, {"KR", "韩国"}, {"TW", "台湾"}, {"HK", "香港"},
	{"MX", "墨西哥"}, {"ES", "西班牙"}, {"IT", "意大利"}, {"PL", "波兰"}, {"SE", "瑞典"},
	{"NO", "挪威"},
}

// LinkProxyRegionOptions mirrors LINK_PROXY_REGION_OPTIONS (app.py:516-520):
// auto, any, then the 21 `"CC 中文名"` labels in declaration order.
var LinkProxyRegionOptions = buildLinkProxyRegionOptions()

func buildLinkProxyRegionOptions() []string {
	out := make([]string, 0, len(linkProxyRegionNames)+2)
	out = append(out, LinkProxyRegionAuto, LinkProxyRegionAny)
	for _, r := range linkProxyRegionNames {
		out = append(out, r.Code+" "+r.Name)
	}
	return out
}

// PaymentModeAliases mirrors PAYMENT_MODE_ALIASES (app.py:490):
// {name.replace("长链接", "短链"): name for name in PAYMENT_MODES}.
// Built from models.PaymentModeOrder so the construction is deterministic.
var PaymentModeAliases = buildPaymentModeAliases()

func buildPaymentModeAliases() map[string]string {
	out := make(map[string]string, len(models.PaymentModeOrder))
	for _, name := range models.PaymentModeOrder {
		out[strings.ReplaceAll(name, "长链接", "短链")] = name
	}
	return out
}

// SessionConvertFormats mirrors SESSION_CONVERT_FORMATS (app.py:5062-5070).
// Key order is the Python dict's insertion order.
var SessionConvertFormats = []string{
	"sub2api", "cpa", "cockpit", "9router", "codex", "axonhub", "codexmanager",
}

// SessionConvertFormatLabels maps the persisted key to the combo-box label.
var SessionConvertFormatLabels = map[string]string{
	"sub2api":      "sub2api",
	"cpa":          "CPA",
	"cockpit":      "Cockpit",
	"9router":      "9router",
	"codex":        "Codex",
	"axonhub":      "AxonHub",
	"codexmanager": "Codex-Manager",
}

// AccountStatusFilterOptions mirrors ACCOUNT_STATUS_FILTER_OPTIONS (app.py:317-325).
var AccountStatusFilterOptions = []string{
	AccountStatusFilterAll, "待处理", "有 Session", "Plus", "Team", "提链成功", "失败",
}

// WorkspacePages mirrors the keys of self.workspace_pages (app.py:14012-14021).
var WorkspacePages = []string{
	"workbench", "mail", "phone", "proxy", "payment", "team", "k12", "actions", "settings",
}

// AccountSortColumns mirrors ACCOUNT_SORT_COLUMNS (app.py:309).
var AccountSortColumns = []string{"email", "type", "status", "attempts"}

// AccountSortDirections mirrors ACCOUNT_SORT_DIRECTIONS (app.py:308).
var AccountSortDirections = []string{AccountSortCustom, AccountSortAsc, AccountSortDesc}

// ProviderProxyRoles mirrors PROVIDER_PROXY_ROLES (app.py:293). Order matters:
// it drives the dialog layout and (in Python) the save order.
var ProviderProxyRoles = []string{"create", "followup", "approve"}

// ProviderProxyRoleLabels mirrors PROVIDER_PROXY_ROLE_LABELS (app.py:294).
var ProviderProxyRoleLabels = map[string]string{
	"create": "第一步", "followup": "后续", "approve": "Approve",
}

// ProviderProxyConfig is one role of `provider_proxy_configs`.
// Mirrors ProxyProviderConfig.state_dict / from_state (app.py:1006-1030).
type ProviderProxyConfig struct {
	Enabled  bool   `json:"enabled"`
	Username string `json:"username"`
	Password string `json:"password"`
	Endpoint string `json:"endpoint"`
	Duration int    `json:"duration"`
	// Regions is the raw textarea content ("regions" on disk, regions_text in
	// Python). Parse it with ParseProviderRegions.
	Regions string `json:"regions"`
}

// DefaultProviderProxyConfig matches ProxyProviderConfig's dataclass defaults
// (app.py:959-964).
func DefaultProviderProxyConfig() ProviderProxyConfig {
	return ProviderProxyConfig{
		Duration: DefaultProviderProxyDuration,
		Regions:  DefaultProviderProxyRegions,
	}
}

// Settings is the typed view of state.json's "settings" object.
// The json tags are the on-disk key names; the struct is safe to hand to Wails.
type Settings struct {
	// S7 支付模式 / 目标金额 / 无头浏览器
	PaymentMode  string `json:"payment_mode"`
	TargetAmount string `json:"target_amount"`
	Headless     bool   `json:"headless"`

	// S17 代理
	LocalProxy                   string `json:"local_proxy"`
	ProxyRouteMode               string `json:"proxy_route_mode"`
	DynamicProxies               string `json:"dynamic_proxies"`
	PaymentDynamicProxy          string `json:"payment_dynamic_proxy"`
	FollowupDynamicProxy         string `json:"followup_dynamic_proxy"`
	ApproveDynamicProxy          string `json:"approve_dynamic_proxy"`
	ReusePaymentProxy            string `json:"reuse_payment_proxy"`
	ReuseFollowupProxy           string `json:"reuse_followup_proxy"`
	ReuseApproveProxy            string `json:"reuse_approve_proxy"`
	LinkProxyRegion              string `json:"link_proxy_region"`
	RequireJapanExtractProxy     bool   `json:"require_japan_extract_proxy"`
	RegisterWithPaymentProxy     bool   `json:"register_with_payment_proxy"`
	ForceLegacyPaypal            bool   `json:"force_legacy_paypal"`
	AuthConcurrency              int    `json:"auth_concurrency"`
	K12Concurrency               int    `json:"k12_concurrency"`
	LinkRaceConcurrency          int    `json:"link_race_concurrency"`
	LinkProxyPrecheckLimit       int    `json:"link_proxy_precheck_limit"`
	LinkProxyPrecheckConcurrency int    `json:"link_proxy_precheck_concurrency"`
	LinkAttemptLimit             int    `json:"link_attempt_limit"`

	// S20 provider proxy dialog. Keyed by ProviderProxyRoles.
	ProviderProxyConfigs map[string]ProviderProxyConfig `json:"provider_proxy_configs"`

	// S16 PayPal extension material (payment_extension_dir is shared with S17)
	PaypalExtensionDir   string `json:"payment_extension_dir"`
	PaypalPhone          string `json:"paypal_phone"`
	PaypalCard           string `json:"paypal_card"`
	PaypalSMSURL         string `json:"paypal_sms_url"`
	PaypalPhonePool      string `json:"paypal_phone_pool"`
	PaypalPhonePoolIndex int    `json:"paypal_phone_pool_index"`

	ExportNamePrefix string `json:"export_name_prefix"`

	// Mail
	DomainMailDomain string `json:"domain_mail_domain"`
	CloudMailEnabled bool   `json:"cloud_mail_enabled"`
	CloudMailBase    string `json:"cloud_mail_base"`
	CloudMailToken   string `json:"cloud_mail_token"`

	// S19
	K12WorkspaceID       string `json:"k12_workspace_id"`
	SessionConvertFormat string `json:"session_convert_format"`
	ManualEmailOTP       bool   `json:"manual_email_otp"`

	// S15 phone / SMS / turnstile
	PhoneMaxReceiveCount   int    `json:"phone_max_receive_count"`
	SMSBowerEnabled        bool   `json:"smsbower_enabled"`
	SMSBowerAPIKey         string `json:"smsbower_api_key"`
	SMSBowerService        string `json:"smsbower_service"`
	SMSBowerCountry        string `json:"smsbower_country"`
	SMSBowerMaxPrice       string `json:"smsbower_max_price"`
	TurnstileSolverEnabled bool   `json:"turnstile_solver_enabled"`
	TurnstileSolverURL     string `json:"turnstile_solver_url"`

	// S18 sound
	SuccessSoundEnabled      bool   `json:"success_sound_enabled"`
	SuccessAudioDevice       string `json:"success_audio_device"`
	PauseOthersOnLinkSuccess bool   `json:"pause_others_on_link_success"`

	// S8 / S9 account table
	AccountGroups        []string `json:"account_groups"`
	AccountGroupFilter   string   `json:"account_group_filter"`
	AccountStatusFilter  string   `json:"account_status_filter"`
	WorkspacePage        string   `json:"workspace_page"`
	AccountSortColumn    string   `json:"account_sort_column"`
	AccountSortDirection string   `json:"account_sort_direction"`

	// Tk-only layout keys. UI_SPEC §3 says to keep them for round-trip
	// compatibility but drive the web layout from CSS/localStorage.
	WindowGeometry  string  `json:"window_geometry"`
	WindowZoomed    bool    `json:"window_zoomed"`
	UILayoutVersion int     `json:"ui_layout_version"`
	MainSashRatio   float64 `json:"main_sash_ratio"`
	LogSashRatio    float64 `json:"log_sash_ratio"`
	BodySashRatio   float64 `json:"body_sash_ratio"`
}

// Defaults returns the values the Tk app starts with before load_state runs
// (the StringVar/BooleanVar/IntVar initialisers, app.py:12337-12433).
// A key absent from the snapshot keeps its default, because Python guards most
// assignments with `if "key" in settings`.
func Defaults() Settings {
	providers := make(map[string]ProviderProxyConfig, len(ProviderProxyRoles))
	for _, role := range ProviderProxyRoles {
		providers[role] = DefaultProviderProxyConfig()
	}
	return Settings{
		PaymentMode:  models.PaymentModeOrder[0], // "无卡长链接 US/USD" (app.py:12337)
		TargetAmount: "",
		Headless:     false,

		LocalProxy:                   DefaultLocalProxy,
		ProxyRouteMode:               ProxyRouteModeDefault,
		LinkProxyRegion:              LinkProxyRegionAny,
		AuthConcurrency:              DefaultAuthConcurrency,
		K12Concurrency:               DefaultK12Concurrency,
		LinkRaceConcurrency:          DefaultLinkRaceConcurrency,
		LinkProxyPrecheckLimit:       DefaultLinkProxyPrecheckLimit,
		LinkProxyPrecheckConcurrency: DefaultLinkProxyPrecheckConcurrency,
		LinkAttemptLimit:             DefaultLinkAttemptLimit,

		ProviderProxyConfigs: providers,

		PaypalExtensionDir: DefaultPaypalExtensionDir,

		DomainMailDomain: DefaultDomainMailDomain,
		CloudMailBase:    DefaultCloudMailBase,

		K12WorkspaceID:       DefaultK12WorkspaceID,
		SessionConvertFormat: DefaultSessionConvertFormat,

		SMSBowerService:  SMSBowerDefaultService,
		SMSBowerCountry:  SMSBowerDefaultCountry,
		SMSBowerMaxPrice: SMSBowerDefaultMaxPrice,

		TurnstileSolverURL: TurnstileSolverDefaultURL,

		SuccessSoundEnabled:      true,
		SuccessAudioDevice:       AudioDefaultDeviceLabel,
		PauseOthersOnLinkSuccess: true,

		AccountGroups:        []string{AccountDefaultGroup},
		AccountGroupFilter:   AccountAllGroup,
		AccountStatusFilter:  AccountStatusFilterAll,
		WorkspacePage:        DefaultWorkspacePage,
		AccountSortColumn:    "email",
		AccountSortDirection: AccountSortCustom,

		UILayoutVersion: UILayoutVersion,
		MainSashRatio:   DefaultMainSashRatio,
		LogSashRatio:    DefaultLogSashRatio,
		BodySashRatio:   DefaultBodySashRatio,
	}
}

// ---------------------------------------------------------------------------
// Validators
// ---------------------------------------------------------------------------

// providerRegionSplitRe mirrors Python's re.split(r"[\s,，;；]+", …) at
// app.py:943.
//
// TRAP: Python's `\s` on a str pattern is Unicode-aware (it matches U+00A0,
// U+3000, U+2028, …); Go's RE2 `\s` is ASCII-only ([\t\n\f\r ]) and omits even
// \v and the C0 information separators U+001C..U+001F. The class is therefore
// spelled out, the same way internal/models spells reWS.
//
// Verified by brute force over all 1,112,064 codepoints: this class matches
// exactly the set Python's `[\s,，;；]` matches (see TestProviderRegionSplitClass).
var providerRegionSplitRe = regexp.MustCompile(`[\s\p{Z}\x{0085}\x{000B}\x{001C}-\x{001F},，;；]+`)

var twoLetterRegionRe = regexp.MustCompile(`^[A-Z]{2}$`)

// upperFullReplacer covers the codepoints whose Python str.upper() EXPANDS to
// exactly two ASCII letters. Python's str.upper() is full case mapping;
// strings.ToUpper is simple case mapping and leaves all six unchanged, so
// "ﬁ" (U+FB01, a ligature a PDF paste routinely produces) is the region code
// FI to app.py:946 and a validation error to a bare ToUpper. Enumerated by
// scanning all 1,112,064 codepoints for len(chr(cp).upper()) == 2 with both
// halves in A-Z; the longer expansions (ﬃ→FFI, ŉ→ʼN, ǰ→J̌ …) cannot pass the
// two-letter test either way and are deliberately left out.
var upperFullReplacer = strings.NewReplacer(
	"ß", "SS",
	"ﬀ", "FF",
	"ﬁ", "FI",
	"ﬂ", "FL",
	"ﬅ", "ST",
	"ﬆ", "ST",
)

// pyUpper mirrors Python's str.upper() closely enough for a region code.
func pyUpper(s string) string { return strings.ToUpper(upperFullReplacer.Replace(s)) }

var smsbowerServiceRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// httpSchemeRe mirrors re.match(r"^https?://", base_url, flags=re.I)
// (app.py:14458).
var httpSchemeRe = regexp.MustCompile(`(?i)^https?://`)

// ParseProviderRegions mirrors parse_provider_regions (app.py:940-954):
// split, upper-case, require exactly two ASCII letters, de-duplicate keeping
// first-seen order, and reject an empty result.
func ParseProviderRegions(value string) ([]string, error) {
	trimmed := pyStrip(value)
	var regions []string
	seen := make(map[string]bool)
	for _, raw := range providerRegionSplitRe.Split(trimmed, -1) {
		if raw == "" {
			continue
		}
		region := pyUpper(raw)
		if !twoLetterRegionRe.MatchString(region) {
			return nil, fmt.Errorf("国家代码必须是两位字母: %s", raw)
		}
		if !seen[region] {
			seen[region] = true
			regions = append(regions, region)
		}
	}
	if len(regions) == 0 {
		return nil, errors.New("至少填写一个 region 国家代码")
	}
	return regions, nil
}

// Validate mirrors ProxyProviderConfig.validated (app.py:970-991).
// A disabled role is never validated, exactly as in Python.
func (c ProviderProxyConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	username := pyStrip(c.Username)
	password := c.Password
	endpoint := pyStrip(c.Endpoint)
	if username == "" {
		return errors.New("用户名不能为空")
	}
	if password == "" {
		return errors.New("密码不能为空")
	}
	raw := endpoint
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	// pyURLSplit, NOT url.Parse. net/url validates the host against RFC 3986 and
	// splits host:port at the LAST colon; urlsplit validates nothing and splits at
	// the FIRST. See internal/settings/endpoint.go for the endpoints that made the
	// difference in both directions.
	parsed, err := pyURLSplit(raw)
	if err != nil {
		return errBadHostPort()
	}
	// app.py:981 evaluates `not parsed.hostname` first and SHORT-CIRCUITS, so an
	// endpoint with no host never reaches the .port property (whose ValueError is
	// the only thing that can escape validated() with a non-app message).
	if parsed.hostText() == "" {
		return errBadHostPort()
	}
	// Python reads urlsplit(...).port, a property that RAISES ValueError for a
	// non-numeric port AND for one outside 0-65535 (app.py:981) — and, on CPython
	// 3.12, for "3_010" / "+3010" / non-ASCII digits, which int() would accept.
	if _, ok, perr := parsed.port(); perr != nil || !ok {
		return errBadHostPort()
	}
	// app.py:983 tests `parsed.username or parsed.password`, i.e. truthiness of
	// the two components — an empty userinfo ("@host:3010") is falsy and passes.
	if user, pass := parsed.userinfo(); user != "" || pass != "" {
		return errors.New(errTextHostExtras)
	}
	if (parsed.path != "" && parsed.path != "/") || parsed.query != "" || parsed.fragment != "" {
		return errors.New(errTextHostExtras)
	}
	if c.Duration < MinProviderProxyDuration || c.Duration > MaxProviderProxyDuration {
		return errors.New("t 必须在 1–120 之间")
	}
	if _, err := ParseProviderRegions(c.Regions); err != nil {
		return err
	}
	return nil
}

// ValidateProviderProxies validates every role in ProviderProxyRoles order.
func (s Settings) ValidateProviderProxies() error {
	for _, role := range ProviderProxyRoles {
		if err := s.ProviderProxyConfigs[role].Validate(); err != nil {
			return fmt.Errorf("%s: %w", ProviderProxyRoleLabels[role], err)
		}
	}
	return nil
}

// CloudMailBaseURL mirrors _cloud_mail_settings (app.py:14454-14465): trim,
// strip trailing slashes, require an http(s) scheme.
func (s Settings) CloudMailBaseURL() (string, error) {
	base := strings.TrimRight(pyStrip(s.CloudMailBase), "/")
	if !httpSchemeRe.MatchString(base) {
		return "", errors.New("Cloud Mail Base URL 格式错误")
	}
	return base, nil
}

// ValidateCloudMail mirrors save_cloud_mail_settings (app.py:14491-14499):
// the base URL must parse, and a token is required once the feature is on.
func (s Settings) ValidateCloudMail() error {
	if _, err := s.CloudMailBaseURL(); err != nil {
		return err
	}
	if s.CloudMailEnabled && pyStrip(s.CloudMailToken) == "" {
		return errors.New("启用 Cloud Mail API 前请填写程序 Token")
	}
	return nil
}

// ValidateSMSBower mirrors _smsbower_settings + save_smsbower_settings
// (app.py:14366-14391).
func (s Settings) ValidateSMSBower() error {
	service := pyStrip(s.SMSBowerService)
	if service == "" {
		service = SMSBowerDefaultService
	}
	country := pyStrip(s.SMSBowerCountry)
	if country == "" {
		country = SMSBowerDefaultCountry
	}
	maxPrice := pyStrip(s.SMSBowerMaxPrice)
	if !smsbowerServiceRe.MatchString(service) {
		return errors.New("SMSBower 服务代码格式不正确")
	}
	if !pyIsDigit(country) {
		return errors.New("SMSBower 国家 ID 必须是数字")
	}
	if maxPrice != "" {
		// Empty means "no cap"; anything else must parse and be > 0 (app.py:14376).
		// pyParseFloatString, not strconv.ParseFloat: this is a value the user
		// TYPES, and the two disagree on inputs a human can produce —
		// strconv accepts the Go hex-float "0x1p3" that float() rejects, rejects
		// the overflow "1e400" that float() turns into inf (an unbounded cap, and
		// this cap is what stops a rental costing more than intended), and rejects
		// the signed "+nan"/"-nan" that float() accepts. It also folds Unicode
		// decimal digits, so float("１.５") is 1.5 here too.
		//
		// NOTE: Python's `float(x) <= 0` is False for NaN, so "nan" passes there
		// — and it passes here too, for the same reason. Not a divergence.
		v, ok := pyParseFloatString(maxPrice)
		if !ok || v <= 0 {
			return errors.New("SMSBower 最高单价必须是大于 0 的数字，或留空")
		}
	}
	if s.SMSBowerEnabled && pyStrip(s.SMSBowerAPIKey) == "" {
		return errors.New("启用 SMSBower 前请填写 API Key")
	}
	return nil
}

// digitNotNd is the set of code points Python's str.isdigit() accepts that are
// NOT category Nd — Numeric_Type=Digit characters such as ² ³ ¹, the subscript
// and superscript digits, and the circled/parenthesised digit forms.
//
// Enumerated by scanning all 1,112,064 code points for
// `chr(cp).isdigit() and category(chr(cp)) != "Nd"` on CPython 3.12 /
// Unicode 15.0.0: 128 code points, and every Nd code point is also isdigit(),
// so `unicode.IsDigit(r) || in(digitNotNd, r)` is str.isdigit() exactly.
var digitNotNd = &unicode.RangeTable{
	R16: []unicode.Range16{
		{0x00b2, 0x00b3, 1}, {0x00b9, 0x00b9, 1}, {0x1369, 0x1371, 1},
		{0x19da, 0x19da, 1}, {0x2070, 0x2070, 1}, {0x2074, 0x2079, 1},
		{0x2080, 0x2089, 1}, {0x2460, 0x2468, 1}, {0x2474, 0x247c, 1},
		{0x2488, 0x2490, 1}, {0x24ea, 0x24ea, 1}, {0x24f5, 0x24fd, 1},
		{0x24ff, 0x24ff, 1}, {0x2776, 0x277e, 1}, {0x2780, 0x2788, 1},
		{0x278a, 0x2792, 1},
	},
	R32: []unicode.Range32{
		{0x10a40, 0x10a43, 1}, {0x10e60, 0x10e68, 1}, {0x11052, 0x1105a, 1},
		{0x1f100, 0x1f10a, 1},
	},
}

// pyIsDigit mirrors Python's str.isdigit() (app.py:14372 `country.isdigit()`).
// Empty is False in Python too.
func pyIsDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !unicode.Is(digitNotNd, r) {
			return false
		}
	}
	return true
}
