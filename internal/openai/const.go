// Package openai holds OpenAI/ChatGPT auth constants and the small pure helpers
// (PKCE, JWT decode, browser headers, auth-record normalization) shared by the
// browser worker and the protocol registration path. Ported from app.py:64-140
// and the auth helpers at app.py:2639-2674 / 4943 / 5588.
package openai

// Base hosts.
const (
	ChatGPTBaseURL = "https://chatgpt.com"
	AuthBaseURL    = "https://auth.openai.com"
)

// auth.openai.com REST endpoints used across the registration flow.
const (
	AuthAuthorizeContinueURL = AuthBaseURL + "/api/accounts/authorize/continue"
	AuthEmailOTPSendURL      = AuthBaseURL + "/api/accounts/email-otp/send"
	AuthEmailOTPValidateURL  = AuthBaseURL + "/api/accounts/email-otp/validate"
	AuthWorkspaceSelectURL   = AuthBaseURL + "/api/accounts/workspace/select"
	AuthPhoneSendURL         = AuthBaseURL + "/api/accounts/add-phone/send"
	AuthPhoneOTPValidateURL  = AuthBaseURL + "/api/accounts/phone-otp/validate"
	AuthUserRegisterURL      = AuthBaseURL + "/api/accounts/user/register"
	AuthCreateAccountURL     = AuthBaseURL + "/api/accounts/create_account"
)

// OAuth (PKCE authorize + token exchange).
const (
	DefaultRedirectURI = "http://localhost:1455/auth/callback"
	DefaultClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	AuthOAuthAudience  = "https://api.openai.com/v1"
	// Auth0-Client header value (base64 of {"name":"auth0-spa-js","version":"1.21.0"}).
	Auth0ClientHeader = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9" // gitleaks:allow，公开 Auth0 客户端元数据
)

// AuthOAuthTokenURLs is tried in order during code->token exchange.
var AuthOAuthTokenURLs = []string{
	AuthBaseURL + "/api/oauth/oauth2/token",
	AuthBaseURL + "/oauth/token",
}

// User-agent + impersonation profile (must match the tlsclient JA3 profile UA).
const (
	CurlCffiImpersonate = "chrome136"
	DefaultUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
)

// Cloudflare / Turnstile handling.
const (
	CFAutoSolveTimeout              = 45 // seconds
	CFManualWaitTimeout             = 90 // seconds
	TurnstileSolverDefaultURL       = "http://127.0.0.1:8888"
	AuthProxyFailureRemoveThreshold = 2
)

// CFClearanceURLs are the hosts a cf_clearance cookie is checked against.
var CFClearanceURLs = []string{
	AuthBaseURL,
	ChatGPTBaseURL,
	"https://openai.com",
	"https://auth.openai.com",
	"https://chatgpt.com",
}

// CFInterstitialDetectJS is injected verbatim to detect a Cloudflare
// interstitial / managed-challenge / Turnstile page. Ported byte-for-byte from
// app.py CF_INTERSTITIAL_DETECT_JS — the pass/fail heuristic depends on it.
const CFInterstitialDetectJS = `() => {
  try {
    const title = String(document.title || '');
    const href = String(location.href || '');
    const bodyText = String(document.body && document.body.innerText || '').slice(0, 2000);
    const html = String(document.documentElement && document.documentElement.innerHTML || '').slice(0, 8000);
    const hasChallengeIframe = !!document.querySelector(
      'iframe[src*="challenges.cloudflare.com"], iframe[src*="turnstile"], iframe[src*="cf-chl"]'
    );
    const hasTurnstileWidget = !!document.querySelector(
      '.cf-turnstile, [data-sitekey], #cf-challenge-running, #challenge-form, #challenge-stage, #challenge-error-title'
    );
    const titleHit = /just a moment|attention required|checking your browser|cloudflare/i.test(title);
    const bodyHit = /verify you are human|checking your browser|enable javascript and cookies|cloudflare ray id|performance & security by cloudflare|needs to review the security/i.test(bodyText);
    const htmlHit = /cf-browser-verification|challenge-platform|__cf_chl|challenges\.cloudflare\.com|turnstile/i.test(html);
    const urlHit = /challenges\.cloudflare\.com|cdn-cgi\/challenge|__cf_chl/i.test(href);
    return !!(hasChallengeIframe || hasTurnstileWidget || titleHit || bodyHit || htmlHit || urlHit);
  } catch (e) {
    return false;
  }
}`

// SMSBower country-id map (from app.py SMSBOWER_COUNTRY_IDS).
var SMSBowerCountryIDs = map[string]string{
	"BR": "73",
	"CO": "33",
	"JP": "182",
	"US": "187",
}

// Extension / workspace defaults.
const (
	// 发布版默认不绑定开发机目录；支付窗口允许用户在设置中显式选择扩展。
	DefaultPayPalExtensionDir = ""
	DefaultK12WorkspaceID     = "workspace-example"
)
