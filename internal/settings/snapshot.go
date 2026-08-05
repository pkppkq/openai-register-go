package settings

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// SettingsKey is the top-level state.json key holding the settings object.
const SettingsKey = "settings"

// ModelledKeys lists, in the order app.py:14234-14297 writes them, every
// settings key this package owns. A key not in this list is copied through
// verbatim by ToSnapshot.
var ModelledKeys = []string{
	"payment_mode", "target_amount", "headless", "local_proxy", "proxy_route_mode",
	"dynamic_proxies", "payment_dynamic_proxy", "followup_dynamic_proxy",
	"approve_dynamic_proxy", "reuse_payment_proxy", "reuse_followup_proxy",
	"reuse_approve_proxy", "link_proxy_region", "require_japan_extract_proxy",
	"register_with_payment_proxy", "force_legacy_paypal", "auth_concurrency",
	"k12_concurrency", "link_race_concurrency", "link_proxy_precheck_limit",
	"link_proxy_precheck_concurrency", "link_attempt_limit", "provider_proxy_configs",
	"payment_extension_dir", "paypal_phone", "paypal_card", "paypal_sms_url",
	"paypal_phone_pool", "export_name_prefix", "domain_mail_domain",
	"cloud_mail_enabled", "cloud_mail_base", "cloud_mail_token", "k12_workspace_id",
	"session_convert_format", "phone_max_receive_count", "smsbower_enabled",
	"smsbower_api_key", "smsbower_service", "smsbower_country", "smsbower_max_price",
	"manual_email_otp", "turnstile_solver_enabled", "turnstile_solver_url",
	"paypal_phone_pool_index", "success_sound_enabled", "success_audio_device",
	"pause_others_on_link_success", "account_groups", "account_group_filter",
	"account_status_filter", "workspace_page", "account_sort_column",
	"account_sort_direction", "window_geometry", "window_zoomed",
	"ui_layout_version", "main_sash_ratio", "log_sash_ratio", "body_sash_ratio",
}

// ---------------------------------------------------------------------------
// FromSnapshot
// ---------------------------------------------------------------------------

// FromSnapshot decodes the "settings" object of a full state.json snapshot
// (the shape internal/state.Store.Load returns) into a typed Settings.
//
// It reproduces GUI.load_state (app.py:14039-14213). Keys absent from the
// snapshot keep their Defaults() value, because Python guards most assignments
// with `if "key" in settings`.
//
// `accounts` is also consulted, because Python folds every account's group into
// account_groups before validating account_group_filter (app.py:14059-14064).
func FromSnapshot(snapshot map[string]any) Settings {
	s := Defaults()
	sm := subMap(snapshot, SettingsKey)

	// --- window / layout (app.py:14040-14049): read unguarded via .get() ---
	s.WindowGeometry = pyStrOr(sm["window_geometry"], "")
	s.WindowZoomed = toBool(sm["window_zoomed"])
	layoutVersion := pyIntOr(sm["ui_layout_version"], 1)
	s.UILayoutVersion = UILayoutVersion // always re-written as the current version
	if layoutVersion >= UILayoutVersion {
		s.MainSashRatio = clampFloat(pyFloatOr(sm["main_sash_ratio"], DefaultMainSashRatio), MinMainSashRatio, MaxMainSashRatio)
		s.LogSashRatio = clampFloat(pyFloatOr(sm["log_sash_ratio"], DefaultLogSashRatio), MinLogSashRatio, MaxLogSashRatio)
		s.BodySashRatio = clampFloat(pyFloatOr(sm["body_sash_ratio"], DefaultBodySashRatio), MinBodySashRatio, MaxBodySashRatio)
	}
	// else: sash ratios stay at the Defaults(), i.e. the saved ones are
	// discarded when ui_layout_version differs (app.py:14050-14053).

	// --- account groups (app.py:14052-14064) ---
	s.AccountGroups = mergeAccountGroups(sm["account_groups"], snapshot["accounts"])

	// --- filters and sort (app.py:14065-14076) ---
	groupFilter := pyStrOr(sm["account_group_filter"], AccountAllGroup)
	if !inList(groupFilter, append([]string{AccountAllGroup}, s.AccountGroups...)) {
		groupFilter = AccountAllGroup
	}
	s.AccountGroupFilter = groupFilter

	statusFilter := pyStrOr(sm["account_status_filter"], AccountStatusFilterAll)
	if !inList(statusFilter, AccountStatusFilterOptions) {
		statusFilter = AccountStatusFilterAll
	}
	s.AccountStatusFilter = statusFilter

	page := pyStrOr(sm["workspace_page"], DefaultWorkspacePage)
	if !inList(page, WorkspacePages) {
		page = DefaultWorkspacePage
	}
	s.WorkspacePage = page

	// _set_account_sort_state (app.py:19030-19032)
	sortColumn := pyStrOr(sm["account_sort_column"], "email")
	if !inList(sortColumn, AccountSortColumns) {
		sortColumn = "email"
	}
	s.AccountSortColumn = sortColumn
	sortDirection := pyStrOr(sm["account_sort_direction"], AccountSortCustom)
	if !inList(sortDirection, AccountSortDirections) {
		sortDirection = AccountSortCustom
	}
	s.AccountSortDirection = sortDirection

	// --- payment mode (app.py:14077-14081) ---
	// Read unguarded; an unrecognised value silently keeps the default.
	savedMode := pyStrOr(sm["payment_mode"], "")
	if _, ok := models.PaymentModes[savedMode]; ok {
		s.PaymentMode = savedMode
	} else if canonical, ok := PaymentModeAliases[savedMode]; ok {
		s.PaymentMode = canonical
	}

	// --- everything below is guarded by `if "key" in settings` in Python ---

	if v, ok := sm["headless"]; ok {
		s.Headless = toBool(v)
	}
	if v, ok := sm["target_amount"]; ok {
		s.TargetAmount = pyStr(v)
	}
	if v, ok := sm["local_proxy"]; ok {
		s.LocalProxy = pyStr(v)
	}
	if v, ok := sm["proxy_route_mode"]; ok {
		mode := pyStrip(pyStrOr(v, ""))
		if !inList(mode, ProxyRouteModeOptions) {
			mode = ProxyRouteModeDefault
		}
		s.ProxyRouteMode = mode
	}
	if v, ok := sm["dynamic_proxies"]; ok {
		s.DynamicProxies = pyStr(v)
	}
	if v, ok := sm["payment_dynamic_proxy"]; ok {
		s.PaymentDynamicProxy = pyStr(v)
	}
	if v, ok := sm["followup_dynamic_proxy"]; ok {
		s.FollowupDynamicProxy = pyStr(v)
	}
	if v, ok := sm["approve_dynamic_proxy"]; ok {
		s.ApproveDynamicProxy = pyStr(v)
	}
	if v, ok := sm["reuse_payment_proxy"]; ok {
		s.ReusePaymentProxy = pyStr(v)
	}
	// app.py:14108-14111: when reuse_followup_proxy is absent the value is
	// seeded from reuse_payment_proxy.
	if v, ok := sm["reuse_followup_proxy"]; ok {
		s.ReuseFollowupProxy = pyStr(v)
	} else if v, ok := sm["reuse_payment_proxy"]; ok {
		s.ReuseFollowupProxy = pyStr(v)
	}
	if v, ok := sm["reuse_approve_proxy"]; ok {
		s.ReuseApproveProxy = pyStr(v)
	}
	if v, ok := sm["link_proxy_region"]; ok {
		region := pyStrip(pyStrOr(v, ""))
		if !inList(region, LinkProxyRegionOptions) {
			region = LinkProxyRegionAny
		}
		s.LinkProxyRegion = region
	}
	if v, ok := sm["require_japan_extract_proxy"]; ok {
		s.RequireJapanExtractProxy = toBool(v)
	}
	if v, ok := sm["register_with_payment_proxy"]; ok {
		s.RegisterWithPaymentProxy = toBool(v)
	}
	if v, ok := sm["force_legacy_paypal"]; ok {
		s.ForceLegacyPaypal = toBool(v)
	}
	// app.py:14121-14123: `DEFAULT if raw in ("", None) else int(raw)`, then
	// clamp. Note this is NOT `raw or DEFAULT`: a literal 0 clamps up to 1
	// rather than falling back to 10.
	if v, ok := sm["auth_concurrency"]; ok {
		s.AuthConcurrency = clampInt(pyIntBlank(v, DefaultAuthConcurrency), 1, MaxAuthConcurrency)
	}
	if v, ok := sm["k12_concurrency"]; ok {
		// UI_SPEC only says "int, default 1", but app.py:14127 clamps against
		// MAX_AUTH_CONCURRENCY too. Python wins.
		s.K12Concurrency = clampInt(pyIntBlank(v, DefaultK12Concurrency), 1, MaxAuthConcurrency)
	}
	if v, ok := sm["link_race_concurrency"]; ok {
		// app.py:14129: `int(value or 1)` — a stored 0 becomes 1 via falsiness.
		s.LinkRaceConcurrency = clampInt(pyIntOr(v, 1), 1, MaxLinkRaceConcurrency)
	}
	if v, ok := sm["link_proxy_precheck_limit"]; ok {
		s.LinkProxyPrecheckLimit = maxInt(1, pyIntOr(v, DefaultLinkProxyPrecheckLimit))
	}
	if v, ok := sm["link_proxy_precheck_concurrency"]; ok {
		s.LinkProxyPrecheckConcurrency = clampInt(
			pyIntBlank(v, DefaultLinkProxyPrecheckConcurrency), 1, MaxLinkProxyPrecheckConcurrency)
	}
	if v, ok := sm["link_attempt_limit"]; ok {
		s.LinkAttemptLimit = clampInt(pyIntOr(v, 1), MinLinkAttemptLimit, MaxLinkAttemptLimit)
	}

	// provider_proxy_configs: read unguarded via .get(); an absent or
	// non-object value yields ProxyProviderConfig.from_state({}) for each role
	// (app.py:14143-14146).
	providerRaw := subMap(sm, "provider_proxy_configs")
	providers := make(map[string]ProviderProxyConfig, len(ProviderProxyRoles))
	for _, role := range ProviderProxyRoles {
		providers[role] = providerConfigFromState(subMap(providerRaw, role))
	}
	s.ProviderProxyConfigs = providers

	if v, ok := sm["payment_extension_dir"]; ok {
		dir := pyStrip(pyStr(v))
		if dir == "" {
			dir = DefaultPaypalExtensionDir
		}
		s.PaypalExtensionDir = dir
	}
	if v, ok := sm["paypal_phone"]; ok {
		s.PaypalPhone = pyStr(v)
	}
	if v, ok := sm["paypal_card"]; ok {
		s.PaypalCard = pyStr(v)
	}
	if v, ok := sm["paypal_sms_url"]; ok {
		s.PaypalSMSURL = pyStr(v)
	}
	if v, ok := sm["paypal_phone_pool"]; ok {
		s.PaypalPhonePool = pyStr(v)
	}
	if v, ok := sm["export_name_prefix"]; ok {
		s.ExportNamePrefix = pyStr(v)
	}

	// app.py:14168: the persisted value is discarded and the constant forced
	// back in, both on load and on save.
	s.DomainMailDomain = DefaultDomainMailDomain

	if v, ok := sm["cloud_mail_enabled"]; ok {
		s.CloudMailEnabled = toBool(v)
	}
	if v, ok := sm["cloud_mail_base"]; ok {
		base := pyStrip(pyStrOr(v, ""))
		if base == "" {
			base = DefaultCloudMailBase
		}
		s.CloudMailBase = base
	}
	if v, ok := sm["cloud_mail_token"]; ok {
		s.CloudMailToken = pyStrip(pyStrOr(v, ""))
	}
	if v, ok := sm["k12_workspace_id"]; ok {
		id := pyStrip(pyStr(v))
		if id == "" {
			id = DefaultK12WorkspaceID
		}
		s.K12WorkspaceID = id
	}
	if v, ok := sm["session_convert_format"]; ok {
		format := strings.ToLower(pyStrip(pyStr(v)))
		if !inList(format, SessionConvertFormats) {
			format = DefaultSessionConvertFormat
		}
		s.SessionConvertFormat = format
	}
	if v, ok := sm["phone_max_receive_count"]; ok {
		s.PhoneMaxReceiveCount = maxInt(0, pyIntOr(v, 0))
	}
	if v, ok := sm["smsbower_enabled"]; ok {
		s.SMSBowerEnabled = toBool(v)
	}
	if v, ok := sm["smsbower_api_key"]; ok {
		s.SMSBowerAPIKey = pyStrip(pyStrOr(v, ""))
	}
	if v, ok := sm["smsbower_service"]; ok {
		s.SMSBowerService = strOrDefault(pyStrip(pyStrOr(v, "")), SMSBowerDefaultService)
	}
	if v, ok := sm["smsbower_country"]; ok {
		s.SMSBowerCountry = strOrDefault(pyStrip(pyStrOr(v, "")), SMSBowerDefaultCountry)
	}
	if v, ok := sm["smsbower_max_price"]; ok {
		// Asymmetric on purpose: save writes "" for "no cap" (app.py:14278)
		// but load turns "" back into "0.07" (app.py:14190). Python wins.
		s.SMSBowerMaxPrice = strOrDefault(pyStrip(pyStrOr(v, "")), SMSBowerDefaultMaxPrice)
	}
	if v, ok := sm["manual_email_otp"]; ok {
		s.ManualEmailOTP = toBool(v)
	}
	if v, ok := sm["turnstile_solver_enabled"]; ok {
		s.TurnstileSolverEnabled = toBool(v)
	}
	if v, ok := sm["turnstile_solver_url"]; ok {
		s.TurnstileSolverURL = strOrDefault(pyStrip(pyStrOr(v, "")), TurnstileSolverDefaultURL)
	}
	if v, ok := sm["paypal_phone_pool_index"]; ok {
		s.PaypalPhonePoolIndex = maxInt(0, pyIntOr(v, 0))
	}
	if v, ok := sm["success_sound_enabled"]; ok {
		s.SuccessSoundEnabled = toBool(v)
	}
	if v, ok := sm["success_audio_device"]; ok {
		s.SuccessAudioDevice = strOrDefault(pyStrip(pyStr(v)), AudioDefaultDeviceLabel)
	}
	if v, ok := sm["pause_others_on_link_success"]; ok {
		s.PauseOthersOnLinkSuccess = toBool(v)
	}

	return s
}

// providerConfigFromState mirrors ProxyProviderConfig.from_state
// (app.py:1017-1030).
func providerConfigFromState(m map[string]any) ProviderProxyConfig {
	duration := pyIntOr(m["duration"], DefaultProviderProxyDuration)
	return ProviderProxyConfig{
		Enabled:  toBool(m["enabled"]),
		Username: pyStrOr(m["username"], ""),
		Password: pyStrOr(m["password"], ""),
		Endpoint: pyStrOr(m["endpoint"], ""),
		Duration: clampInt(duration, MinProviderProxyDuration, MaxProviderProxyDuration),
		Regions:  strOrDefault(pyStrOr(m["regions"], ""), DefaultProviderProxyRegions),
	}
}

// mergeAccountGroups mirrors app.py:14052-14064: start from ["未分组"], append
// each saved group that is non-empty, not a case-insensitive duplicate and not
// the "全部" pseudo-group, then fold in every group referenced by an account.
func mergeAccountGroups(saved any, accounts any) []string {
	groups := []string{AccountDefaultGroup}
	seen := map[string]bool{foldKey(AccountDefaultGroup): true}
	add := func(raw string) {
		group := pyStrip(raw)
		if group == "" || group == AccountAllGroup {
			return
		}
		key := foldKey(group)
		if seen[key] {
			return
		}
		seen[key] = true
		groups = append(groups, group)
	}
	for _, item := range asList(saved) {
		add(pyStrOr(item, ""))
	}
	// app.py:14059-14064 — account groups are folded in unconditionally, and
	// this pass does NOT skip "全部" (only the saved-groups pass does).
	for _, item := range asList(accounts) {
		account := asStringMap(item)
		if account == nil {
			continue
		}
		group := pyStrip(pyStrOr(account["group"], ""))
		if group == "" {
			group = AccountDefaultGroup
		}
		key := foldKey(group)
		if !seen[key] {
			seen[key] = true
			groups = append(groups, group)
		}
	}
	return groups
}

// ---------------------------------------------------------------------------
// ToSnapshot
// ---------------------------------------------------------------------------

// ToSnapshot renders s back into a full state.json snapshot, mirroring
// GUI._build_state_snapshot (app.py:14225-14299).
//
// Every key of `prior` that this package does not model is copied through
// verbatim — at the top level (accounts, phones, session_results, updated_at,
// schema_version, …), inside "settings", and inside each
// provider_proxy_configs role. That is a hard requirement: the same file is
// still being written by the Python app.
//
// `prior` is not mutated; the returned map is a fresh shallow copy at the two
// levels this package rewrites.
//
// Note: "updated_at" is NOT refreshed here. Python stamps it in
// _build_state_snapshot; in the Go port that belongs to the caller that owns
// the clock, so the prior value is preserved verbatim.
func ToSnapshot(s Settings, prior map[string]any) map[string]any {
	out := make(map[string]any, len(prior)+1)
	for k, v := range prior {
		out[k] = v
	}

	priorSettings := subMap(prior, SettingsKey)
	ns := make(map[string]any, len(priorSettings)+len(ModelledKeys))
	for k, v := range priorSettings {
		ns[k] = v // unmodelled settings keys survive verbatim
	}

	ns["payment_mode"] = s.PaymentMode
	ns["target_amount"] = pyStrip(s.TargetAmount)
	ns["headless"] = s.Headless
	ns["local_proxy"] = s.LocalProxy // app.py:14238 — deliberately not stripped
	routeMode := s.ProxyRouteMode
	if !inList(routeMode, ProxyRouteModeOptions) {
		routeMode = ProxyRouteModeDefault // re-validated on save too (app.py:14239)
	}
	ns["proxy_route_mode"] = routeMode
	ns["dynamic_proxies"] = pyStrip(s.DynamicProxies)
	ns["payment_dynamic_proxy"] = pyStrip(s.PaymentDynamicProxy)
	ns["followup_dynamic_proxy"] = pyStrip(s.FollowupDynamicProxy)
	ns["approve_dynamic_proxy"] = pyStrip(s.ApproveDynamicProxy)
	ns["reuse_payment_proxy"] = pyStrip(s.ReusePaymentProxy)
	ns["reuse_followup_proxy"] = pyStrip(s.ReuseFollowupProxy)
	ns["reuse_approve_proxy"] = pyStrip(s.ReuseApproveProxy)
	ns["link_proxy_region"] = strOrDefault(pyStrip(s.LinkProxyRegion), LinkProxyRegionAny)
	ns["require_japan_extract_proxy"] = s.RequireJapanExtractProxy
	ns["register_with_payment_proxy"] = s.RegisterWithPaymentProxy
	ns["force_legacy_paypal"] = s.ForceLegacyPaypal
	ns["auth_concurrency"] = clampInt(s.AuthConcurrency, 1, MaxAuthConcurrency)
	ns["k12_concurrency"] = clampInt(s.K12Concurrency, 1, MaxAuthConcurrency)
	ns["link_race_concurrency"] = clampInt(s.LinkRaceConcurrency, 1, MaxLinkRaceConcurrency)
	ns["link_proxy_precheck_limit"] = maxInt(1, s.LinkProxyPrecheckLimit)
	ns["link_proxy_precheck_concurrency"] = clampInt(s.LinkProxyPrecheckConcurrency, 1, MaxLinkProxyPrecheckConcurrency)
	ns["link_attempt_limit"] = clampInt(s.LinkAttemptLimit, MinLinkAttemptLimit, MaxLinkAttemptLimit)
	ns["provider_proxy_configs"] = providerConfigsOut(s, subMap(priorSettings, "provider_proxy_configs"))
	ns["payment_extension_dir"] = pyStrip(s.PaypalExtensionDir) // app.py:14261 — no default fallback on save
	ns["paypal_phone"] = pyStrip(s.PaypalPhone)
	ns["paypal_card"] = pyStrip(s.PaypalCard)
	ns["paypal_sms_url"] = pyStrip(s.PaypalSMSURL)
	ns["paypal_phone_pool"] = pyStrip(s.PaypalPhonePool)
	ns["export_name_prefix"] = pyStrip(s.ExportNamePrefix)
	ns["domain_mail_domain"] = DefaultDomainMailDomain // app.py:14267 — always the constant
	ns["cloud_mail_enabled"] = s.CloudMailEnabled
	ns["cloud_mail_base"] = strOrDefault(pyStrip(s.CloudMailBase), DefaultCloudMailBase)
	ns["cloud_mail_token"] = pyStrip(s.CloudMailToken)
	ns["k12_workspace_id"] = pyStrip(s.K12WorkspaceID) // app.py:14271 — no default fallback on save
	ns["session_convert_format"] = pyStrip(s.SessionConvertFormat)
	ns["phone_max_receive_count"] = maxInt(0, s.PhoneMaxReceiveCount)
	ns["smsbower_enabled"] = s.SMSBowerEnabled
	ns["smsbower_api_key"] = pyStrip(s.SMSBowerAPIKey)
	ns["smsbower_service"] = strOrDefault(pyStrip(s.SMSBowerService), SMSBowerDefaultService)
	ns["smsbower_country"] = strOrDefault(pyStrip(s.SMSBowerCountry), SMSBowerDefaultCountry)
	ns["smsbower_max_price"] = pyStrip(s.SMSBowerMaxPrice) // "" means "no cap"
	ns["manual_email_otp"] = s.ManualEmailOTP
	ns["turnstile_solver_enabled"] = s.TurnstileSolverEnabled
	ns["turnstile_solver_url"] = strOrDefault(pyStrip(s.TurnstileSolverURL), TurnstileSolverDefaultURL)
	ns["paypal_phone_pool_index"] = maxInt(0, s.PaypalPhonePoolIndex)
	ns["success_sound_enabled"] = s.SuccessSoundEnabled
	ns["success_audio_device"] = strOrDefault(pyStrip(s.SuccessAudioDevice), AudioDefaultDeviceLabel)
	ns["pause_others_on_link_success"] = s.PauseOthersOnLinkSuccess
	// make(…, 0, …) not append(nil, …): an empty AccountGroups must marshal as
	// [] like Python's list(...), not as null.
	groups := make([]string, 0, len(s.AccountGroups))
	ns["account_groups"] = append(groups, s.AccountGroups...)
	ns["account_group_filter"] = s.AccountGroupFilter
	ns["account_status_filter"] = s.AccountStatusFilter
	ns["workspace_page"] = s.WorkspacePage
	ns["account_sort_column"] = s.AccountSortColumn
	ns["account_sort_direction"] = s.AccountSortDirection
	ns["window_geometry"] = s.WindowGeometry
	ns["window_zoomed"] = s.WindowZoomed
	ns["ui_layout_version"] = UILayoutVersion // app.py:14294 — always the constant
	ns["main_sash_ratio"] = clampFloat(s.MainSashRatio, MinMainSashRatio, MaxMainSashRatio)
	ns["log_sash_ratio"] = clampFloat(s.LogSashRatio, MinLogSashRatio, MaxLogSashRatio)
	ns["body_sash_ratio"] = clampFloat(s.BodySashRatio, MinBodySashRatio, MaxBodySashRatio)

	out[SettingsKey] = ns
	return out
}

// providerConfigsOut mirrors app.py:14257-14260 plus
// _provider_proxy_config_from_vars (app.py:12479-12491): username/endpoint/
// regions are stripped, password is not, duration is written as-is.
//
// Unlike Python — which rebuilds the dict from the three known roles and so
// silently drops anything else — unknown roles and unknown fields inside a
// known role are preserved. Python's loader ignores them, so this is a strict
// safety improvement over dropping user data.
func providerConfigsOut(s Settings, prior map[string]any) map[string]any {
	out := make(map[string]any, len(prior)+len(ProviderProxyRoles))
	for k, v := range prior {
		out[k] = v
	}
	for _, role := range ProviderProxyRoles {
		cfg := s.ProviderProxyConfigs[role]
		priorRole := subMap(prior, role)
		m := make(map[string]any, len(priorRole)+6)
		for k, v := range priorRole {
			m[k] = v
		}
		m["enabled"] = cfg.Enabled
		m["username"] = pyStrip(cfg.Username)
		m["password"] = cfg.Password
		m["endpoint"] = pyStrip(cfg.Endpoint)
		m["duration"] = cfg.Duration
		m["regions"] = pyStrip(cfg.Regions)
		out[role] = m
	}
	return out
}

// ---------------------------------------------------------------------------
// Coercion helpers — Python semantics
// ---------------------------------------------------------------------------

// toBool reads a persisted boolean, accepting both real JSON booleans and the
// string spellings the Tk app sometimes wrote.
//
// DELIBERATE DIVERGENCE from Python: app.py:14082 etc. use bool(value), and
// Python's bool("false") is True because a non-empty string is truthy. Reading
// the string "false" as true would silently flip a user's checkbox, so the
// string spellings are decoded here instead. Every other value falls back to
// Python truthiness.
//
// The divergence is self-limiting: ToSnapshot writes every boolean back as a
// real JSON bool (app.py does the same), so a string spelling survives at most
// one load and can only have come from a hand edit — app.py never writes one.
func toBool(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		switch strings.ToLower(pyStrip(t)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off", "":
			return false
		}
		return true // non-empty string → Python truthiness
	}
	return pyTruthy(v)
}

// pyTruthy mirrors Python's truth testing for the value kinds json.Unmarshal
// can produce.
// PyTruthy and PyStr expose the two coercions above to the rest of the port.
// They live here rather than being re-implemented per package because every
// hand-rolled version so far has diverged: bool("false") is True, str(None) is
// "None", and a bare Go type switch quietly returns the zero value for both.
func PyTruthy(v any) bool { return pyTruthy(v) }

// PyStr mirrors Python's str(). See pyStr for the cases it deliberately does
// not reproduce.
func PyStr(v any) string { return pyStr(v) }

func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case float32:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// pyStr mirrors Python's str().
//
// The `str(None) == "None"` case matters: app.py reads several keys with a bare
// str(settings[key]) and no `or ""` guard (target_amount app.py:14085,
// local_proxy :14087, the three dynamic-proxy pools :14093-14104,
// payment_extension_dir :14150, k12_workspace_id :14174,
// session_convert_format :14176, success_audio_device :14202). A hand-edited
// JSON null therefore becomes the literal string "None" in the Tk app, and the
// port reproduces that rather than quietly healing it.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		return pyFloatStr(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	// DIVERGENCE: a JSON array or object reaching a string-valued setting.
	// Python renders repr() — str([]) is "[]", str({"a": 1}) is "{'a': 1}" —
	// and a dict's repr depends on INSERTION ORDER, which encoding/json throws
	// away, so no Go function can reproduce it. "" is returned instead, which
	// also makes the `or DEFAULT` fallbacks (payment_extension_dir,
	// k12_workspace_id, success_audio_device, smsbower_*) fire and restore a
	// usable value rather than persisting a stringified container. Only reachable
	// from a hand-corrupted state.json: app.py only ever writes strings here.
	return ""
}

// pyStrOr mirrors `str(value or fallback)`: a falsy value yields the fallback
// without going through str().
func pyStrOr(v any, fallback string) string {
	if !pyTruthy(v) {
		return fallback
	}
	return pyStr(v)
}

// pyFloatStr formats a JSON number the way Python's str() does.
//
// DIVERGENCE (unavoidable, and the reason this is not a plain FormatFloat):
// encoding/json decodes every JSON number into float64, discarding the int /
// float distinction Python's json module keeps. Python renders `20` as "20" and
// `20.0` as "20.0"; only one of the two can be reproduced from a float64.
// Integral values are therefore rendered WITHOUT the ".0", because every number
// app.py writes to these keys comes from int()/IntVar (app.py:14273, 14282,
// 12352-12357) and so is a Python int on reload. internal/accounts/pyvalue.go
// makes the same choice for the same reason.
//
// Values at or above 1e15 fall through to %g ("1e+16"), matching str(float);
// a JSON integer that large would render as full digits in Python, but no key
// here holds one.
func pyFloatStr(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ndDigitValue returns the decimal value of a Unicode Nd rune, i.e. Python's
// int(chr(cp)). Adjacent digit runs are not separated by a gap (U+1D7CE..U+1D7FF
// is one unbroken span of five ten-digit blocks), so the value is the offset
// from the start of the whole contiguous span modulo ten, not a bounded walk.
// Same helper as internal/accounts/pyvalue.go.
func ndDigitValue(r rune) (int, bool) {
	if !unicode.IsDigit(r) {
		return 0, false
	}
	if r <= '9' {
		return int(r - '0'), true
	}
	start := r
	for start > 0 && unicode.IsDigit(start-1) {
		start--
	}
	return int(r-start) % 10, true
}

// pyDecimalASCII mirrors the pre-pass CPython runs on a str before int() and
// float() parse it (_PyUnicode_TransformDecimalAndSpaceToASCII): every Unicode
// decimal digit (category Nd) is folded to its ASCII equivalent. That is why
// int("０７") == 7 and float("１.５") == 1.5 in Python while strconv rejects
// both. Non-Nd runes are left alone, so int("²") still fails — U+00B2 is
// Numeric_Type=Digit but not Nd, and Python raises ValueError on it too.
func pyDecimalASCII(s string) string {
	ascii := true
	for _, r := range s {
		if r > 0x7F {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if d, ok := ndDigitValue(r); ok && r > 0x7F {
			b.WriteByte(byte('0' + d))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pyNumStrip strips exactly the whitespace CPython's int() and float() skip
// around a numeric literal.
//
// It is NOT pyStrip. str.strip() removes 29 code points; int()/float() accept
// only 25 of them and RAISE on the four C0 information separators
// U+001C..U+001F. Verified by brute force over all 1,112,064 code points:
// int(c + "1") succeeds for exactly the set unicode.IsSpace reports, and
// str.strip() additionally removes U+001C..U+001F. Using pyStrip here made
// int("\x1c5") return 5 where Python raises ValueError (aborting load_state).
func pyNumStrip(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// pyParseIntString mirrors CPython's int(str) exactly:
//
//	[ws] [+-] digit (["_"] digit)* [ws]
//
// after _PyUnicode_TransformDecimalAndSpaceToASCII has folded Unicode decimal
// digits (pyDecimalASCII).
//
// Two things strconv.ParseInt gets wrong on its own and which a real state.json
// can carry:
//   - Underscore digit separators. int("3_0") is 30, int("+1_2_3") is 123.
//     ParseInt(base 10) rejects them; ParseInt(base 0) would accept them but
//     would also accept "0x10"/"0b1", which int() refuses. Hence the hand-rolled
//     scan: an underscore is legal only between two digits.
//   - Overflow. Python ints are arbitrary precision, so
//     int("999999999999999999999999") succeeds and every caller here then clamps
//     it into range. ParseInt returns ErrRange and the old code fell back to the
//     DEFAULT — the opposite end of the range from where Python lands. Saturating
//     to MaxInt64/MinInt64 makes the caller's clamp produce Python's answer.
func pyParseIntString(s string) (int, bool) {
	t := pyDecimalASCII(pyNumStrip(s))
	body := t
	neg := false
	if body != "" && (body[0] == '+' || body[0] == '-') {
		neg = body[0] == '-'
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	var b strings.Builder
	b.Grow(len(body))
	prevDigit := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '_' {
			// Legal only with a digit on both sides; "_3", "3_", "3__0" all raise.
			if !prevDigit || i+1 >= len(body) || body[i+1] < '0' || body[i+1] > '9' {
				return 0, false
			}
			prevDigit = false
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		prevDigit = true
		b.WriteByte(c)
	}
	n, err := strconv.ParseInt(b.String(), 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			if neg {
				return math.MinInt64, true
			}
			return math.MaxInt64, true
		}
		return 0, false
	}
	if neg {
		n = -n
	}
	return int(n), true
}

// pyParseFloatString mirrors CPython's float(str). Three fixes over a bare
// strconv.ParseFloat, all confirmed against CPython 3.12:
//   - Go accepts hex float literals ("0x1p3" -> 8); Python raises ValueError.
//   - Go's ParseFloat returns ErrRange for "1e400" and the old code fell back to
//     the default; Python returns inf. The value Go hands back alongside ErrRange
//     is already ±Inf (or 0 for an underflow like "1e-400", which Python also
//     returns), so it is simply accepted.
//   - Python accepts a signed nan ("+nan", "-NAN"); Go's ParseFloat rejects it.
func pyParseFloatString(s string) (float64, bool) {
	t := pyDecimalASCII(pyNumStrip(s))
	rest := t
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		rest = rest[1:]
	}
	if len(rest) >= 2 && rest[0] == '0' && (rest[1] == 'x' || rest[1] == 'X') {
		return 0, false
	}
	if strings.EqualFold(rest, "nan") {
		return math.NaN(), true
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// pyInt mirrors Python's int(). ok is false where Python would raise
// ValueError/TypeError.
func pyInt(v any) (int, bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return truncToInt(t), true
	case float32:
		return truncToInt(float64(t)), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		return pyParseIntString(t)
	}
	return 0, false
}

// truncToInt is int(float) with Python's toward-zero truncation, saturating
// instead of wrapping: Python's int(1e20) is exact and then clamped by the
// caller, while a bare int64 conversion of an out-of-range float64 is
// implementation-defined in Go.
func truncToInt(f float64) int {
	f = math.Trunc(f)
	if f >= math.MaxInt64 {
		return math.MaxInt64
	}
	if f <= math.MinInt64 {
		return math.MinInt64
	}
	return int(f)
}

// pyIntOr mirrors `int(value or fallback)`.
//
// Divergence, noted for the record: in Python a non-numeric string raises and
// aborts the whole load_state try-block (app.py:14212), losing every later key.
// Degrading to the default is strictly safer and keeps the rest of the file.
func pyIntOr(v any, fallback int) int {
	if !pyTruthy(v) {
		return fallback
	}
	if n, ok := pyInt(v); ok {
		return n
	}
	return fallback
}

// pyIntBlank mirrors `DEFAULT if raw in ("", None) else int(raw)`. Unlike
// pyIntOr, a stored 0 or false is NOT treated as blank.
func pyIntBlank(v any, fallback int) int {
	if v == nil {
		return fallback
	}
	if s, ok := v.(string); ok && s == "" {
		return fallback
	}
	if n, ok := pyInt(v); ok {
		return n
	}
	return fallback
}

// pyFloatOr mirrors `float(value or fallback)`.
func pyFloatOr(v any, fallback float64) float64 {
	if !pyTruthy(v) {
		return fallback
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case bool:
		return 1
	case string:
		if f, ok := pyParseFloatString(t); ok {
			return f
		}
	}
	return fallback
}

// pyStrip mirrors Python's str.strip() with no argument.
//
// Go's strings.TrimSpace uses unicode.IsSpace, which is Python's whitespace set
// minus the four ASCII separators U+001C..U+001F that str.isspace() also
// reports as whitespace; those are added back here.
func pyStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
}

// caseFoldFullReplacer covers the characters where Python's str.casefold()
// differs from a simple strings.ToLower. Go has no full case folding in the
// standard library, so the expanding folds are applied by hand. Same table as
// internal/accounts/pyvalue.go.
var caseFoldFullReplacer = strings.NewReplacer(
	"ß", "ss",
	"ς", "σ", // final sigma folds to sigma
	"ﬀ", "ff",
	"ﬁ", "fi",
	"ﬂ", "fl",
	"ﬃ", "ffi",
	"ﬄ", "ffl",
	"ﬅ", "st",
	"ﬆ", "st",
)

// foldKey mirrors Python's str.casefold() for the group-name de-duplication at
// app.py:14056.
//
// It used to be a bare strings.ToLower, which is SIMPLE folding: Python folds
// "ß" and "SS" to the same key and drops the duplicate group, Go kept both and
// the group combo box grew an extra entry on every load. Exact for ASCII, CJK
// and the expanding folds above; still an approximation for exotic scripts
// (µ→μ, ſ→s, Cherokee), none of which can name a group here.
func foldKey(s string) string { return caseFoldFullReplacer.Replace(strings.ToLower(s)) }

func strOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func inList(v string, list []string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// clampFloat mirrors app.py:14044-14046's `min(hi, max(lo, v))` — NaN included.
//
// Python's max(lo, v) returns v only when `v > lo`, which is False for NaN, so
// a NaN ratio lands on lo. Go's `if v < lo` and `if v > hi` are BOTH false for
// NaN, so the obvious comparison clamp passed NaN straight through — and
// json.Marshal then fails with "json: unsupported value: NaN", aborting the
// entire state.json save. A NaN gets here from a hand-edited "nan"/"NaN"
// string, which Python's float() and Go's ParseFloat both accept.
func clampFloat(v, lo, hi float64) float64 {
	if !(v > lo) { // max(lo, v)
		v = lo
	}
	if !(v < hi) { // min(hi, v)
		v = hi
	}
	return v
}

// subMap returns m[key] as a map[string]any, or an empty map. It never returns
// nil, so callers can index it directly.
func subMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	if sub := asStringMap(m[key]); sub != nil {
		return sub
	}
	return map[string]any{}
}

// asStringMap accepts both the map[string]any that json.Unmarshal produces and
// the typed maps a Go caller may hand back after a previous ToSnapshot.
func asStringMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, s := range t {
			out[k] = s
		}
		return out
	}
	return nil
}

// asList accepts both the []any that json.Unmarshal produces and the []string
// a Go caller may hand back after a previous ToSnapshot. Without this,
// FromSnapshot(ToSnapshot(...)) would silently ignore account_groups and the
// round trip would not be a fixed point.
func asList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	case []map[string]any:
		out := make([]any, len(t))
		for i, m := range t {
			out[i] = m
		}
		return out
	}
	return nil
}
