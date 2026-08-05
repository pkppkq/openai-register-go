// Package mail ports the Python mail-reading subsystem (app.py) to Go.
//
// It provides three reader backends behind a common Reader interface:
//   - CloudMailReader   : HTTP "Cloud Mail" API (fetch by recipient address).
//   - HotmailOtpReader  : Outlook/Hotmail via IMAP (XOAUTH2) with automatic
//     fallback to Microsoft Graph and the legacy Outlook
//     REST API. Refreshes access tokens from a refresh_token.
//   - (ProxiedIMAP)     : IMAP4-over-SSL tunnelled through an upstream proxy
//     (SOCKS via x/net/proxy, or a manual HTTP CONNECT).
//
// It preserves the OpenAI OTP parsing (subject/body regexes), the deactivation
// notice detection, the ChatGPT Team / K12 link extraction, and the Microsoft
// OAuth token endpoints + scopes from the Python source.
//
// Assumptions about the imported models package (adjust if field names differ):
//
//	models.MailAccount has string fields:
//	    Email, Password, ClientID, RefreshToken, Raw,
//	    ReceiveMailbox, MailProvider, CloudMailBase, CloudMailToken
//
// tlsclient (the Chrome-impersonating HTTP client) as this package uses it:
//
//	tlsclient.NewOrNil(proxyURL string, timeoutSec int) *tlsclient.Client
//	(*tlsclient.Client).Do(method, url string, body io.Reader,
//	    extra http.Header) (status int, body []byte, err error)
//
// This was written as an "assumption" before tlsclient existed and then went stale;
// it is now the real signature.
package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	netmail "net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	xproxy "golang.org/x/net/proxy"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/tlsclient"
)

// ---------------------------------------------------------------------------
// Constants (ported from app.py)
// ---------------------------------------------------------------------------

const (
	DefaultEmailOTPLookbackSeconds  = 300
	emailOTPFastPollSeconds         = 2
	emailOTPSlowPollSeconds         = 5
	emailOTPFastPollWindowSeconds   = 90
	emailOTPFolderRediscoverSeconds = 45
	accountEmailLockedStatus        = "邮箱锁定"
	imapScope                       = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"
	graphScope                      = "https://graph.microsoft.com/Mail.Read offline_access"
	graphDefaultScope               = "https://graph.microsoft.com/.default offline_access"
	imapServerHost                  = "outlook.office365.com"
	imapServerPort                  = 993
	imapServerAddr                  = "outlook.office365.com:993"
)

type tokenEndpoint struct {
	Name     string
	URL      string
	Scope    string
	Resource string
}

var tokenEndpoints = []tokenEndpoint{
	{Name: "LIVE+scope", URL: "https://login.live.com/oauth20_token.srf", Scope: imapScope},
	{Name: "V1-COMMON", URL: "https://login.microsoftonline.com/common/oauth2/token", Resource: "https://outlook.office.com/"},
	{Name: "V1-CONSUMERS", URL: "https://login.microsoftonline.com/consumers/oauth2/token", Resource: "https://outlook.office.com/"},
	{Name: "CONSUMERS", URL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token", Scope: imapScope},
	{Name: "COMMON", URL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", Scope: imapScope},
	{Name: "LIVE", URL: "https://login.live.com/oauth20_token.srf"},
	{Name: "CONSUMERS-noscope", URL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token"},
	{Name: "COMMON-noscope", URL: "https://login.microsoftonline.com/common/oauth2/v2.0/token"},
}

var graphTokenEndpoints = []tokenEndpoint{
	{Name: "CONSUMERS-Graph", URL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token", Scope: graphScope},
	{Name: "COMMON-Graph", URL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", Scope: graphScope},
	{Name: "CONSUMERS-Graph-default", URL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token", Scope: graphDefaultScope},
	{Name: "COMMON-Graph-default", URL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", Scope: graphDefaultScope},
	{Name: "CONSUMERS-Graph-noscope", URL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token"},
	{Name: "COMMON-Graph-noscope", URL: "https://login.microsoftonline.com/common/oauth2/v2.0/token"},
	{Name: "LIVE-Graph", URL: "https://login.live.com/oauth20_token.srf"},
}

// ---------------------------------------------------------------------------
// Logging / errors
// ---------------------------------------------------------------------------

// Log is the sink for human-facing progress messages (mirrors the Python `log`).
type Log func(msg string)

func (l Log) emit(msg string) {
	if l != nil {
		l(msg)
	}
}

// EmailSecurityInterruptError signals the mailbox is locked by Microsoft
// (compromised / needs web sign-in). Mirrors Python EmailSecurityInterruptError.
type EmailSecurityInterruptError struct {
	Message string
	Status  string
}

func (e *EmailSecurityInterruptError) Error() string { return e.Message }

func newEmailSecurityInterrupt(msg string) *EmailSecurityInterruptError {
	return &EmailSecurityInterruptError{Message: msg, Status: accountEmailLockedStatus}
}

// CloudMailError mirrors Python CloudMailError.
type CloudMailError struct{ Message string }

func (e *CloudMailError) Error() string { return e.Message }

func cloudErr(format string, args ...interface{}) *CloudMailError {
	return &CloudMailError{Message: fmt.Sprintf(format, args...)}
}

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

// MailRecord is the normalized representation of one message (mirrors the dict
// returned by the Python readers).
type MailRecord struct {
	ID          string  `json:"id"`
	Folder      string  `json:"folder"`
	Kind        string  `json:"kind"`
	Code        string  `json:"code"`
	Subject     string  `json:"subject"`
	From        string  `json:"from"`
	To          string  `json:"to"`
	Date        string  `json:"date"`
	MailTime    float64 `json:"mail_time"`
	MailTimeISO string  `json:"mail_time_iso"`
	Snippet     string  `json:"snippet"`
	Body        string  `json:"body,omitempty"`
}

// DeactivationResult mirrors the scan_openai_deactivation_notice return dict.
type DeactivationResult struct {
	Found                bool         `json:"found"`
	Count                int          `json:"count"`
	Latest               *MailRecord  `json:"latest,omitempty"`
	Matches              []MailRecord `json:"matches"`
	Days                 int          `json:"days"`
	MaxMessagesPerFolder int          `json:"max_messages_per_folder"`
	ScannedMessages      int          `json:"scanned_messages"`
	AliasMismatchCount   int          `json:"alias_mismatch_count"`
	CheckedAt            string       `json:"checked_at"`
}

// Reader is the common interface implemented by every backend.
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

// CreateMailReader mirrors Python create_mail_reader.
func CreateMailReader(account *models.MailAccount, log Log, proxyURL string) (Reader, error) {
	if strings.EqualFold(strings.TrimSpace(account.MailProvider), "cloudmail") {
		return NewCloudMailReader(account, log, account.CloudMailBase, account.CloudMailToken)
	}
	return NewHotmailOtpReader(account, log, proxyURL), nil
}

// ---------------------------------------------------------------------------
// Regexes and small text helpers (ported from app.py)
// ---------------------------------------------------------------------------

// Every pattern below that Python applies to str (not bytes) is spelled with
// the pytext.go character classes: RE2's `\s`, `\d`, `\S` and `\b` are all
// ASCII-only and all four appear in these patterns. See pytext.go for the
// defects the ASCII spellings produced.
var (
	reHTTPScheme    = regexp.MustCompile(`(?i)^https?://`)
	reOpenAIChatGPT = regexp.MustCompile(`(?i)openai|chatgpt`)
	// app.py:6283/6316/6372/6418 — `re.sub(r"\s+", " ", ...)`.
	reWhitespace = regexp.MustCompile(pyWS + `+`)
	// app.py:6318 — `[^\d]` excludes EVERY Unicode decimal digit, so the 100-char
	// window cannot skip over an Arabic-Indic digit the way `[^0-9]` would, and
	// the capture itself is Nd.
	reCodeContext = regexp.MustCompile(`(?i)(?:OpenAI|ChatGPT|verification|verify|code|验证码|登录码)` + pyNonDigit + `{0,100}(` + pyDigit + `{6})`)
	// app.py:6319 — `\b(\d{6})\b`; group 1 survives pyB (see its doc comment).
	reCodeBare = regexp.MustCompile(pyB(`(` + pyDigit + `{6})`))
	// app.py:6330. re.IGNORECASE additionally folds İ/ı into i in CPython.
	reEmailMatch     = regexp.MustCompile(`(?i)[A-Z0-9._%+` + pyReIgnoreCaseLetters + `-]+@[A-Z0-9.` + pyReIgnoreCaseLetters + `-]+\.[A-Z` + pyReIgnoreCaseLetters + `]{2,}`)
	reEmailNormalize = regexp.MustCompile("[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}")
	reScriptBlock    = regexp.MustCompile(`(?is)<script.*?</script>`)
	reStyleBlock     = regexp.MustCompile(`(?is)<style.*?</style>`)
	reHTMLTag        = regexp.MustCompile(`<[^>]+>`)
	// app.py:6379 — `\b(?:access|account)?\s*(?:deactivated|...)\b`.
	reDeactSubject = regexp.MustCompile(`(?i)` + pyB(`(?:access|account)?`+pyWS+`*(?:deactivated|disabled|suspended|terminated)`))
	reHref         = regexp.MustCompile(`(?i)href=["']([^"']+)["']`)
	// app.py:6428 — `https?://[^\s"'<>]+`. An HTML mail's `&nbsp;` right after a
	// link ends the URL in Python; with RE2's `\s` it was swallowed into the href.
	reBareURL         = regexp.MustCompile(`(?i)https?://[^` + pyWSClass + `"'<>]+`)
	reNumericToken    = regexp.MustCompile(`^` + pyDigit + `+(?:\.` + pyDigit + `+)?$`)
	reTeamInviteWords = regexp.MustCompile(`(?i)openai|chatgpt|team|workspace|business|invite|invitation`)
	reLinkScanWords   = regexp.MustCompile(`(?i)openai|chatgpt|k12`)
)

var normalizeTrimCutset = " \t\r\n\"'“”‘’<>()[]{}，,;；"

// normalizeEmailAddress mirrors normalize_email_address (app.py:1610).
func normalizeEmailAddress(value string) string {
	text := pyStrip(value)
	text = strings.Trim(text, normalizeTrimCutset)
	if m := reEmailNormalize.FindString(text); m != "" {
		return m
	}
	return text
}

// htmlToText mirrors html_to_text.
func htmlToText(value string) string {
	text := reScriptBlock.ReplaceAllString(value, " ")
	text = reStyleBlock.ReplaceAllString(text, " ")
	text = reHTMLTag.ReplaceAllString(text, " ")
	return html.UnescapeString(reWhitespace.ReplaceAllString(text, " "))
}

// extractOpenAICode mirrors extract_openai_code.
func extractOpenAICode(text string) string {
	src := text
	if src == "" {
		src = " "
	}
	normalized := reWhitespace.ReplaceAllString(src, " ")
	if m := reCodeContext.FindStringSubmatch(normalized); m != nil {
		return m[1]
	}
	if m := reCodeBare.FindStringSubmatch(normalized); m != nil {
		return m[1]
	}
	return ""
}

// ExtractOpenAICode 从邮件标题、元数据或正文中提取六位验证码。
// 该导出入口供 Wails 邮箱管理绑定复用，解析规则仍与 Python 保持一致。
func ExtractOpenAICode(text string) string {
	return extractOpenAICode(text)
}

// extractEmailAddressesForMatching mirrors extract_email_addresses_for_matching
// (app.py:6328). The fold is casefold, not lower: "ſam@b.com" and "sam@b.com"
// are the same recipient to Python.
func extractEmailAddressesForMatching(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reEmailMatch.FindAllString(text, -1) {
		addr := strings.Trim(pyStrip(m), "<>()[]{}\"'.,;:")
		addr = pyCaseFold(addr)
		if addr != "" {
			out[addr] = true
		}
	}
	return out
}

func mailboxEmailForPlusAlias(emailAddr string) string {
	text := normalizeEmailAddress(emailAddr)
	at := strings.Index(text, "@")
	if at < 0 {
		return text
	}
	local, domain := text[:at], text[at+1:]
	if !strings.Contains(local, "+") {
		return text
	}
	base := local[:strings.Index(local, "+")]
	return base + "@" + domain
}

func isPlusAliasEmail(emailAddr string) bool {
	text := normalizeEmailAddress(emailAddr)
	at := strings.Index(text, "@")
	if at < 0 {
		return false
	}
	return strings.Contains(text[:at], "+")
}

func receiveMailboxForAccount(account *models.MailAccount) string {
	explicit := normalizeEmailAddress(account.ReceiveMailbox)
	if explicit != "" {
		return explicit
	}
	return mailboxEmailForPlusAlias(account.Email)
}

func recipientTextMatchesAccount(account *models.MailAccount, recipientText string) bool {
	target := pyCaseFold(normalizeEmailAddress(account.Email))
	receive := pyCaseFold(receiveMailboxForAccount(account))
	if target == "" || target == receive {
		return true
	}
	return extractEmailAddressesForMatching(recipientText)[target]
}

// isOpenAIDeactivationNotice mirrors is_openai_deactivation_notice.
func isOpenAIDeactivationNotice(subject, fromAddr, body string) bool {
	subjectText := pyCaseFold(reWhitespace.ReplaceAllString(subject, " "))
	bodyText := pyCaseFold(reWhitespace.ReplaceAllString(body, " "))
	combined := subjectText + "\n" + pyCaseFold(fromAddr) + "\n" + bodyText
	if !reOpenAIChatGPT.MatchString(combined) {
		return false
	}
	subjectHit := reDeactSubject.MatchString(subjectText)
	bodyMarkers := []string{
		"account has been deactivated",
		"access has been deactivated",
		"account can no longer be used",
		"can no longer be used",
		"violated our terms and usage policies",
		"violated our usage policies",
		"initiate appeal",
		"start an appeal",
		"账号已停用",
		"账户已停用",
		"账号已封禁",
		"账户已封禁",
	}
	for _, marker := range bodyMarkers {
		if strings.Contains(bodyText, marker) {
			return true
		}
	}
	if subjectHit {
		for _, marker := range []string{"deactivated", "disabled", "suspended", "terminated"} {
			if strings.Contains(subjectText, marker) {
				return true
			}
		}
	}
	return false
}

func openaiDeactivationNoticeMatchesAccount(targetEmail, toAddr, body string) bool {
	target := pyCaseFold(pyStrip(targetEmail))
	if !strings.Contains(target, "@") {
		return false
	}
	addresses := extractEmailAddressesForMatching(toAddr + "\n" + body)
	if addresses[target] {
		return true
	}
	targetDomain := target[strings.LastIndex(target, "@")+1:]
	sameDomain := false
	for addr := range addresses {
		if addr[strings.LastIndex(addr, "@")+1:] == targetDomain {
			sameDomain = true
			break
		}
	}
	// + 别名共用母邮箱收信，必须明确匹配当前别名。
	if isPlusAliasEmail(target) {
		return false
	}
	if sameDomain {
		return false
	}
	return true
}

func openaiDeactivationNoticeSnippet(text string, limit int) string {
	normalized := pyStrip(reWhitespace.ReplaceAllString(text, " "))
	if len([]rune(normalized)) <= limit {
		return normalized
	}
	runes := []rune(normalized)
	cut := limit - 3
	if cut < 0 {
		cut = 0
	}
	// app.py:6420 slices by CODE POINT and then rstrip()s, which removes every
	// Unicode space, not just U+0020.
	return pyRStrip(string(runes[:cut])) + "..."
}

// extractLinksFromText mirrors extract_links_from_text.
func extractLinksFromText(text string) []string {
	raw := html.UnescapeString(text)
	var candidates []string
	appendMatch := func(value string) {
		value = pyStrip(html.UnescapeString(value))
		low := pyLower(value)
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			return
		}
		value = strings.TrimRight(value, ".,;)]}>")
		candidates = append(candidates, value)
	}
	for _, m := range reHref.FindAllStringSubmatch(raw, -1) {
		appendMatch(m[1])
	}
	for _, m := range reBareURL.FindAllString(raw, -1) {
		appendMatch(m)
	}
	seen := map[string]bool{}
	var links []string
	for _, link := range candidates {
		key := pyLower(link)
		if seen[key] {
			continue
		}
		seen[key] = true
		links = append(links, link)
	}
	return links
}

// isChatGPTTeamInviteURL mirrors is_chatgpt_team_invite_url (app.py:6446).
//
// net/url is NOT urllib here, and both differences hid real invites:
//   - url.Parse REJECTS a malformed percent-escape ("/%zz/invite/team") and the
//     whole link was discarded; urlsplit never fails, so Python still clicked it.
//   - url.Parse hands back a DECODED Path, so an escaped route was unquoted
//     twice, and url.QueryUnescape then turns "+" into a space and fails the
//     whole string on one bad escape. Python's unquote does neither.
func isChatGPTTeamInviteURL(rawURL string) bool {
	text := pyStrip(html.UnescapeString(rawURL))
	if text == "" {
		return false
	}
	parsed := pyURLSplit(text)
	host := pyCaseFold(parsed.Hostname())
	if !(host == "chatgpt.com" || strings.HasSuffix(host, ".chatgpt.com") ||
		host == "openai.com" || strings.HasSuffix(host, ".openai.com")) {
		return false
	}
	route := pyCaseFold(pyUnquote(parsed.Path + "?" + parsed.Query))
	if strings.Contains(route, "k12-invite") || strings.Contains(route, "teacher") {
		return false
	}
	hasFirst := strings.Contains(route, "invite") || strings.Contains(route, "invitation") || strings.Contains(route, "join")
	hasSecond := false
	for _, marker := range []string{"team", "workspace", "business", "admin", "invite", "join"} {
		if strings.Contains(route, marker) {
			hasSecond = true
			break
		}
	}
	return hasFirst && hasSecond
}

func extractChatGPTTeamInviteURL(text string) string {
	for _, link := range extractLinksFromText(text) {
		if isChatGPTTeamInviteURL(link) {
			return link
		}
	}
	return ""
}

// ExtractChatGPTTeamInviteURL 提取可信 OpenAI/ChatGPT 域名下的 Team 邀请链接。
// K12、教师邀请和外部域名会继续按 Python 规则被拒绝。
func ExtractChatGPTTeamInviteURL(text string) string {
	return extractChatGPTTeamInviteURL(text)
}

// ---------------------------------------------------------------------------
// RFC822 message parsing (stdlib-based replacement for the email package)
// ---------------------------------------------------------------------------

type leafPart struct {
	ContentType string
	Decoded     string
}

var wordDecoder = &mime.WordDecoder{}

func decodeHeaderText(value string) string {
	if value == "" {
		return ""
	}
	if decoded, err := wordDecoder.DecodeHeader(value); err == nil {
		return decoded
	}
	return value
}

func decodeLeafBody(header textproto.MIMEHeader, body io.Reader) string {
	raw, _ := io.ReadAll(body)
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
	case "base64":
		stripped := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(raw))
		if decoded, err := base64.StdEncoding.DecodeString(stripped); err == nil {
			return string(decoded)
		}
		if decoded, err := base64.RawStdEncoding.DecodeString(stripped); err == nil {
			return string(decoded)
		}
		return string(raw)
	case "quoted-printable":
		if decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw))); err == nil {
			return string(decoded)
		}
		return string(raw)
	default:
		return string(raw)
	}
}

func collectParts(header textproto.MIMEHeader, body io.Reader, out *[]leafPart) {
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			collectParts(textproto.MIMEHeader(part.Header), part, out)
		}
		return
	}
	if mediaType != "text/plain" && mediaType != "text/html" {
		return
	}
	text := decodeLeafBody(header, body)
	*out = append(*out, leafPart{ContentType: mediaType, Decoded: text})
}

func collectLeafParts(raw []byte) (netmail.Header, []leafPart) {
	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, nil
	}
	var parts []leafPart
	collectParts(textproto.MIMEHeader(msg.Header), msg.Body, &parts)
	return msg.Header, parts
}

// extractMessageText mirrors extract_message_text.
func extractMessageText(leaves []leafPart) string {
	var parts []string
	for _, leaf := range leaves {
		if leaf.ContentType == "text/html" {
			parts = append(parts, htmlToText(leaf.Decoded))
		} else {
			parts = append(parts, leaf.Decoded)
		}
	}
	return strings.Join(parts, "\n")
}

// extractMessageLinkText mirrors extract_message_link_text: keeps raw HTML so
// href attributes survive for link extraction.
func extractMessageLinkText(leaves []leafPart) string {
	chunks := []string{extractMessageText(leaves)}
	for _, leaf := range leaves {
		if leaf.Decoded != "" {
			chunks = append(chunks, leaf.Decoded)
		}
	}
	var nonEmpty []string
	for _, c := range chunks {
		if c != "" {
			nonEmpty = append(nonEmpty, c)
		}
	}
	return strings.Join(nonEmpty, "\n")
}

// messageRecipientHeaders mirrors message_recipient_headers.
func messageRecipientHeaders(header netmail.Header) string {
	var values []string
	for _, name := range []string{"To", "Cc", "Delivered-To", "X-Original-To", "Envelope-To", "X-Envelope-To", "X-Forwarded-To", "Resent-To"} {
		for _, v := range header[textproto.CanonicalMIMEHeaderKey(name)] {
			if v != "" {
				values = append(values, decodeHeaderText(v))
			}
		}
	}
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, "\n")
}

func headerDateTimestamp(header netmail.Header) float64 {
	if header == nil {
		return float64(time.Now().Unix())
	}
	raw := header.Get("Date")
	if raw == "" {
		return float64(time.Now().Unix())
	}
	if t, err := netmail.ParseDate(raw); err == nil {
		return float64(t.Unix())
	}
	return float64(time.Now().Unix())
}

// isoFromTimestamp mirrors datetime.fromtimestamp(t).isoformat(timespec="seconds").
func isoFromTimestamp(mailTime float64) string {
	if mailTime == 0 {
		return ""
	}
	return time.Unix(int64(mailTime), 0).Format("2006-01-02T15:04:05")
}

func nowISO() string {
	return time.Now().Format("2006-01-02T15:04:05")
}

func classifyKind(subject, fromAddr, body, haystack, code string) string {
	if isOpenAIDeactivationNotice(subject, fromAddr, body) {
		return "封禁"
	}
	if code != "" {
		return "验证码"
	}
	if reOpenAIChatGPT.MatchString(haystack) {
		return "OpenAI"
	}
	return "普通"
}

// ---------------------------------------------------------------------------
// JWT + token-kind detection (ported)
// ---------------------------------------------------------------------------

func decodeJWTPayload(token string) map[string]interface{} {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := strings.ReplaceAll(parts[1], "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// microsoftAccessTokenKind mirrors microsoft_access_token_kind (app.py:6508)
// -> (kind, aud, scope). The kind decides whether the token is used for IMAP or
// for Graph, so getting the scope chain wrong costs a mailbox connection.
func microsoftAccessTokenKind(accessToken string) (string, string, string) {
	claims := decodeJWTPayload(accessToken)
	if claims == nil {
		return "unknown", "", ""
	}
	audience := pyStrip(pyStr(pyOr(claims["aud"], "")))
	// app.py:6511 is `str(claims.get("scp") or claims.get("scope") or "").strip()`:
	// the `or` chain tests TRUTHINESS BEFORE stripping. A scp of "   " is truthy,
	// so Python keeps it and strips it to "" — it does NOT fall through to scope.
	// Choosing scope there classified an IMAP-only token as a Graph token.
	scope := pyStrip(pyStr(pyOr(claims["scp"], claims["scope"], "")))
	normAud := strings.TrimRight(pyCaseFold(audience), "/")
	normScope := pyCaseFold(scope)
	if strings.Contains(normScope, "imap.accessasuser.all") {
		return "imap", audience, scope
	}
	if normAud == "https://outlook.office.com" || normAud == "https://outlook.office365.com" ||
		normAud == "00000002-0000-0ff1-ce00-000000000000" || strings.Contains(normAud, "outlook.office") {
		return "imap", audience, scope
	}
	if normAud == "https://graph.microsoft.com" || normAud == "00000003-0000-0000-c000-000000000000" ||
		strings.Contains(normAud, "graph.microsoft.com") ||
		strings.Contains(normScope, "mail.read") || strings.Contains(normScope, "mail.readwrite") {
		return "graph", audience, scope
	}
	return "unknown", audience, scope
}

func isMicrosoftAccountSecurityInterrupt(message string) bool {
	text := pyCaseFold(message)
	for _, marker := range []string{
		"account security interrupt",
		"collecting proof",
		"found as compromised",
		"account is found as compromised",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// updateMailRefreshToken mirrors update_mail_refresh_token.
func updateMailRefreshToken(account *models.MailAccount, refreshToken string) bool {
	token := strings.TrimSpace(refreshToken)
	if token == "" || token == account.RefreshToken {
		return false
	}
	account.RefreshToken = token
	var parts []string
	if account.Raw != "" {
		parts = strings.Split(account.Raw, "----")
	}
	if len(parts) >= 4 {
		parts[3] = token
		account.Raw = strings.Join(parts, "----")
	} else if account.Email != "" && account.ClientID != "" {
		account.Raw = strings.Join([]string{account.Email, account.Password, account.ClientID, token}, "----")
	}
	return true
}

// refreshHotmailAccessToken mirrors refresh_hotmail_access_token.
func refreshHotmailAccessToken(account *models.MailAccount, log Log, proxyURL, purpose string) (string, error) {
	if strings.ToLower(strings.TrimSpace(purpose)) == "graph" {
		purpose = "graph"
	} else {
		purpose = "imap"
	}
	endpoints := tokenEndpoints
	if purpose == "graph" {
		endpoints = graphTokenEndpoints
	}
	client := tlsclient.NewOrNil(proxyURL, 10)
	var errs []string
	for _, endpoint := range endpoints {
		form := url.Values{}
		form.Set("client_id", account.ClientID)
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", account.RefreshToken)
		if endpoint.Scope != "" {
			form.Set("scope", endpoint.Scope)
		}
		if endpoint.Resource != "" {
			form.Set("resource", endpoint.Resource)
		}
		log.emit(fmt.Sprintf("尝试邮箱 Token 端点 %s", endpoint.Name))
		status, respBody, err := httpDo(client, http.MethodPost, endpoint.URL, []byte(form.Encode()), map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"Accept":       "application/json",
		})
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", endpoint.Name, err))
			log.emit(fmt.Sprintf("邮箱 Token 端点 %s 异常: %v", endpoint.Name, err))
			continue
		}
		var payload map[string]interface{}
		if len(respBody) > 0 {
			_ = json.Unmarshal(respBody, &payload)
		}
		accessToken := asString(payload["access_token"])
		if httpOK(status) && accessToken != "" {
			tokenKind, audience, scope := microsoftAccessTokenKind(accessToken)
			if purpose == "graph" && tokenKind == "unknown" && strings.Count(accessToken, ".") != 2 && strings.Count(accessToken, ".") != 4 {
				errs = append(errs, fmt.Sprintf("%s: 返回旧式 opaque token，不能用于 Microsoft Graph", endpoint.Name))
				log.emit(fmt.Sprintf("邮箱 Token 端点 %s 返回旧式 opaque token，跳过 Graph", endpoint.Name))
				if updateMailRefreshToken(account, asString(payload["refresh_token"])) {
					log.emit("微软返回了轮换后的邮箱 refresh_token，已更新本地账号")
				}
				continue
			}
			if tokenKind != purpose && tokenKind != "unknown" {
				detail := fmt.Sprintf("aud=%s scope=%s", orDash(audience), orDash(scope))
				errs = append(errs, fmt.Sprintf("%s: 返回 %s token (%s)", endpoint.Name, tokenKind, detail))
				log.emit(fmt.Sprintf("邮箱 Token 端点 %s 返回 %s token，不能用于 %s，继续尝试", endpoint.Name, tokenKind, strings.ToUpper(purpose)))
				continue
			}
			if updateMailRefreshToken(account, asString(payload["refresh_token"])) {
				log.emit("微软返回了轮换后的邮箱 refresh_token，已更新本地账号")
			}
			detail := fmt.Sprintf(" 类型=%s", tokenKind)
			if audience != "" {
				detail += " aud=" + truncate(audience, 100)
			}
			if scope != "" {
				detail += " scope=" + truncate(scope, 160)
			}
			log.emit(fmt.Sprintf("邮箱 Token 端点 %s 成功:%s", endpoint.Name, detail))
			return accessToken, nil
		}
		msg := asString(payload["error_description"])
		if msg == "" {
			msg = asString(payload["error"])
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", status)
		}
		errs = append(errs, fmt.Sprintf("%s: %s", endpoint.Name, msg))
		log.emit(fmt.Sprintf("邮箱 Token 端点 %s 失败: %s", endpoint.Name, msg))
		if isMicrosoftAccountSecurityInterrupt(msg) {
			return "", newEmailSecurityInterrupt("邮箱被微软安全锁定/疑似被盗号，需要先网页登录 Outlook 补安全证明，或直接换邮箱")
		}
	}
	return "", fmt.Errorf("所有邮箱 Token 端点均失败 -> %s", strings.Join(errs, " | "))
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func httpOK(status int) bool { return status >= 200 && status < 300 }

func httpDo(client *tlsclient.Client, method, rawURL string, body []byte, header map[string]string) (int, []byte, error) {
	return client.DoSimple(method, rawURL, body, header)
}

// ---------------------------------------------------------------------------
// Generic value helpers
// ---------------------------------------------------------------------------

func asString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asMap(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func asList(v interface{}) []interface{} {
	if l, ok := v.([]interface{}); ok {
		return l
	}
	return nil
}

func toInt(v interface{}) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}

func mapGet(m map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if v, ok := m[key]; ok && v != nil {
			return v
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
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

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Cloud Mail
// ---------------------------------------------------------------------------

// CloudMailClient mirrors the Python CloudMailClient (HTTP Cloud Mail API).
type CloudMailClient struct {
	BaseURL string
	Token   string
	client  *tlsclient.Client
}

// NewCloudMailClient mirrors CloudMailClient.__init__.
func NewCloudMailClient(baseURL, token string) (*CloudMailClient, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	tok := strings.TrimSpace(token)
	if !reHTTPScheme.MatchString(base) {
		return nil, cloudErr("Cloud Mail Base URL 格式错误")
	}
	if tok == "" {
		return nil, cloudErr("Cloud Mail 程序 Token 为空")
	}
	return &CloudMailClient{BaseURL: base, Token: tok, client: tlsclient.NewOrNil("", 30)}, nil
}

// CloudMailGenerateToken mirrors CloudMailClient.generate_token.
func CloudMailGenerateToken(baseURL, adminEmail, adminPassword string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !reHTTPScheme.MatchString(base) {
		return "", cloudErr("Cloud Mail Base URL 格式错误")
	}
	client := tlsclient.NewOrNil("", 30)
	reqBody, _ := json.Marshal(map[string]string{
		"email":    normalizeEmailAddress(adminEmail),
		"password": adminPassword,
	})
	status, respBody, err := httpDo(client, http.MethodPost, base+"/api/public/genToken", reqBody, map[string]string{
		"Content-Type": "application/json",
	})
	if err != nil {
		return "", cloudErr("Cloud Mail 生成 Token 请求失败: %v", err)
	}
	var payload map[string]interface{}
	if json.Unmarshal(respBody, &payload) != nil {
		return "", cloudErr("Cloud Mail 生成 Token 请求失败: 返回不是 JSON")
	}
	token := strings.TrimSpace(asString(mapGet(asMapOr(payload["data"]), "token")))
	if !httpOK(status) || toInt(payload["code"]) != 200 || token == "" {
		return "", cloudErr("Cloud Mail 生成 Token 失败，HTTP %d: %s", status, truncate(fmt.Sprintf("%v", payload), 500))
	}
	return token, nil
}

func asMapOr(v interface{}) map[string]interface{} {
	if m := asMap(v); m != nil {
		return m
	}
	return map[string]interface{}{}
}

func (c *CloudMailClient) request(path string, body map[string]interface{}) (map[string]interface{}, error) {
	reqBody, _ := json.Marshal(body)
	status, respBody, err := httpDo(c.client, http.MethodPost, c.BaseURL+path, reqBody, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": c.Token,
	})
	if err != nil {
		return nil, cloudErr("Cloud Mail 请求失败: %v", err)
	}
	var payload map[string]interface{}
	if json.Unmarshal(respBody, &payload) != nil {
		return nil, cloudErr("Cloud Mail 返回不是 JSON，HTTP %d: %s", status, truncate(string(respBody), 300))
	}
	if !httpOK(status) || toInt(payload["code"]) != 200 {
		return nil, cloudErr("Cloud Mail 接口错误，HTTP %d: %s", status, truncate(fmt.Sprintf("%v", payload), 500))
	}
	return payload, nil
}

// ListMails mirrors CloudMailClient.list_mails.
func (c *CloudMailClient) ListMails(toEmail, keyword string, size int) ([]map[string]interface{}, error) {
	if size <= 0 {
		size = 20
	}
	body := map[string]interface{}{
		"toEmail": normalizeEmailAddress(toEmail),
		"type":    0,
		"isDel":   0,
		"num":     1,
		"size":    clampInt(size, 1, 500),
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		body["subject"] = "%" + kw + "%"
	}
	payload, err := c.request("/api/public/emailList", body)
	if err != nil {
		return nil, err
	}
	data := payload["data"]
	if list := asList(data); list != nil {
		return dictList(list), nil
	}
	if m := asMap(data); m != nil {
		for _, key := range []string{"records", "list", "items", "rows"} {
			if list := asList(m[key]); list != nil {
				return dictList(list), nil
			}
		}
	}
	return nil, nil
}

func dictList(list []interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	for _, item := range list {
		if m := asMap(item); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// cloudMailTimestamp mirrors cloud_mail_timestamp.
func cloudMailTimestamp(value interface{}) float64 {
	switch t := value.(type) {
	case float64:
		if t > 10_000_000_000 {
			return t / 1000
		}
		return t
	case int:
		f := float64(t)
		if f > 10_000_000_000 {
			return f / 1000
		}
		return f
	}
	text := pyStrip(asString(value))
	if text == "" {
		return 0
	}
	// app.py:6712 fullmatches Python's `\d`, so "１７００００００００" is a valid epoch
	// there and float() parses it; strconv.ParseFloat would not.
	if reNumericToken.MatchString(text) {
		f := pyFloat(text)
		if f > 10_000_000_000 {
			return f / 1000
		}
		return f
	}
	// str.replace() replaces EVERY occurrence, not the first.
	isoText := strings.ReplaceAll(text, "Z", "+00:00")
	for _, layout := range []string{"2006-01-02T15:04:05-07:00", "2006-01-02T15:04:05.999999999-07:00", time.RFC3339} {
		if parsed, err := time.Parse(layout, isoText); err == nil {
			return pyDatetimeTimestamp(parsed)
		}
	}
	// datetime.fromisoformat() also accepts a naive stamp, a date on its own and
	// minute precision; the tz-naive branch is stamped UTC at app.py:6718.
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, isoText, time.UTC); err == nil {
			return pyDatetimeTimestamp(parsed)
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006/01/02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, text, time.UTC); err == nil {
			return pyDatetimeTimestamp(parsed)
		}
	}
	// DIVERGENCE: fromisoformat's full 3.11+ grammar is wider still (basic
	// "YYYYMMDD", any single separator character, "HHMMSS", week dates). Cloud
	// Mail emits epoch millis or "YYYY-MM-DD HH:MM:SS", both handled above; a
	// shape only fromisoformat accepts falls through to 0 here, which the caller
	// treats as "no timestamp" rather than misdating the mail.
	return 0
}

// CloudMailReader mirrors the Python CloudMailReader.
type CloudMailReader struct {
	account     *models.MailAccount
	log         Log
	client      *CloudMailClient
	MailMode    string
	ScanFolders []string
	connected   bool
}

// NewCloudMailReader mirrors CloudMailReader.__init__.
func NewCloudMailReader(account *models.MailAccount, log Log, baseURL, token string) (*CloudMailReader, error) {
	client, err := NewCloudMailClient(baseURL, token)
	if err != nil {
		return nil, err
	}
	return &CloudMailReader{
		account:     account,
		log:         log,
		client:      client,
		MailMode:    "cloudmail",
		ScanFolders: []string{"Cloud Mail"},
	}, nil
}

func (r *CloudMailReader) Connect() error {
	if _, err := r.client.ListMails(r.account.Email, "", 1); err != nil {
		return err
	}
	r.connected = true
	r.log.emit(fmt.Sprintf("Cloud Mail API 已连接，按收件地址取码: %s", r.account.Email))
	return nil
}

func (r *CloudMailReader) Close() error {
	r.connected = false
	return nil
}

func (r *CloudMailReader) record(mail map[string]interface{}) MailRecord {
	subject := asString(mail["subject"])
	body := asString(mail["text"])
	if body == "" {
		body = htmlToText(asString(mail["content"]))
	}
	fromAddr := asString(mapGet(mail, "sendEmail", "from"))
	toAddr := asString(mail["toEmail"])
	if toAddr == "" {
		toAddr = r.account.Email
	}
	createTime := mapGet(mail, "createTime", "createdAt")
	mailTime := cloudMailTimestamp(createTime)
	haystack := strings.Join([]string{subject, fromAddr, toAddr, body}, "\n")
	code := ""
	if reOpenAIChatGPT.MatchString(haystack) {
		code = extractOpenAICode(haystack)
	}
	id := asString(mapGet(mail, "emailId", "id"))
	return MailRecord{
		ID:          id,
		Folder:      "Cloud Mail",
		Kind:        classifyKind(subject, fromAddr, body, haystack, code),
		Code:        code,
		Subject:     subject,
		From:        fromAddr,
		To:          toAddr,
		Date:        asString(createTime),
		MailTime:    mailTime,
		MailTimeISO: isoFromTimestamp(mailTime),
		Snippet:     truncate(strings.TrimSpace(reWhitespace.ReplaceAllString(body, " ")), 220),
		Body:        body,
	}
}

func (r *CloudMailReader) records(size int, keyword string) ([]MailRecord, error) {
	mails, err := r.client.ListMails(r.account.Email, keyword, size)
	if err != nil {
		return nil, err
	}
	out := make([]MailRecord, 0, len(mails))
	for _, mail := range mails {
		out = append(out, r.record(mail))
	}
	return out, nil
}

func (r *CloudMailReader) WaitForCode(ctx context.Context, minTimestamp float64, timeout, lookbackSeconds int) (string, error) {
	if timeout < 1 {
		timeout = 1
	}
	effectiveMin := minTimestamp - float64(maxInt(0, lookbackSeconds))
	if effectiveMin < 0 {
		effectiveMin = 0
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	var lastNotice time.Time
	seenIDs := map[string]bool{}
	for time.Now().Before(deadline) {
		records, err := r.records(20, "")
		if err != nil {
			return "", err
		}
		for _, record := range records {
			if record.ID != "" {
				if seenIDs[record.ID] {
					continue
				}
				seenIDs[record.ID] = true
			}
			haystack := strings.Join([]string{record.Subject, record.From, record.Body}, "\n")
			if !reOpenAIChatGPT.MatchString(haystack) {
				continue
			}
			if effectiveMin != 0 && record.MailTime != 0 && record.MailTime+30 < effectiveMin {
				continue
			}
			if record.Code != "" {
				r.log.emit(fmt.Sprintf("Cloud Mail 收到 OpenAI 验证码: %s", record.Code))
				return record.Code, nil
			}
		}
		if time.Since(lastNotice) >= 20*time.Second {
			remain := maxInt(0, int(time.Until(deadline).Seconds()))
			r.log.emit(fmt.Sprintf("Cloud Mail 仍在等待 OpenAI 验证码，剩余约 %ds", remain))
			lastNotice = time.Now()
		}
		wait := time.Until(deadline)
		if wait > 3*time.Second {
			wait = 3 * time.Second
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("等待 Cloud Mail 验证码超时: %s", r.account.Email)
}

func (r *CloudMailReader) ListFolders() ([]string, error) {
	if !r.connected {
		if err := r.Connect(); err != nil {
			return nil, err
		}
	}
	return []string{"Cloud Mail"}, nil
}

func (r *CloudMailReader) ListRecentMessages(_ string, maxMessages int, query string) ([]MailRecord, error) {
	if !r.connected {
		if err := r.Connect(); err != nil {
			return nil, err
		}
	}
	records, err := r.records(maxMessages, "")
	if err != nil {
		return nil, err
	}
	queryText := strings.ToLower(strings.TrimSpace(query))
	if queryText == "" {
		return records, nil
	}
	var out []MailRecord
	for _, record := range records {
		haystack := strings.ToLower(strings.Join([]string{record.Kind, record.Subject, record.From, record.To, record.Snippet}, "\n"))
		if strings.Contains(haystack, queryText) {
			out = append(out, record)
		}
	}
	return out, nil
}

func (r *CloudMailReader) FetchMessage(_ string, messageID string) (MailRecord, error) {
	target := messageID
	records, err := r.records(200, "")
	if err != nil {
		return MailRecord{}, err
	}
	for _, record := range records {
		if record.ID == target {
			return record, nil
		}
	}
	return MailRecord{}, fmt.Errorf("Cloud Mail 未找到邮件 ID: %s", target)
}

func (r *CloudMailReader) waitForMatchingLink(ctx context.Context, keyword string, minTimestamp float64, timeout int, teamInvite bool) (string, error) {
	if timeout < 1 {
		timeout = 1
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	keywordText := strings.ToLower(strings.TrimSpace(keyword))
	for time.Now().Before(deadline) {
		records, err := r.records(50, "")
		if err != nil {
			return "", err
		}
		for _, record := range records {
			if minTimestamp != 0 && record.MailTime != 0 && record.MailTime+60 < minTimestamp {
				continue
			}
			haystack := strings.Join([]string{record.Subject, record.From, record.Body}, "\n")
			if keywordText != "" && !strings.Contains(strings.ToLower(haystack), keywordText) {
				continue
			}
			if teamInvite {
				if link := extractChatGPTTeamInviteURL(haystack); link != "" {
					return link, nil
				}
			} else {
				if links := extractLinksFromText(haystack); len(links) > 0 {
					return links[0], nil
				}
			}
		}
		wait := time.Until(deadline)
		if wait > 3*time.Second {
			wait = 3 * time.Second
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return "", err
		}
	}
	label := keyword
	if teamInvite {
		label = "Team 邀请"
	} else if label == "" {
		label = "目标"
	}
	return "", fmt.Errorf("等待 Cloud Mail %s邮件超时", label)
}

func (r *CloudMailReader) WaitForTeamInvite(ctx context.Context, minTimestamp float64, timeout int) (string, error) {
	return r.waitForMatchingLink(ctx, "", minTimestamp, timeout, true)
}

func (r *CloudMailReader) WaitForLink(ctx context.Context, keyword string, minTimestamp float64, timeout int) (string, error) {
	return r.waitForMatchingLink(ctx, keyword, minTimestamp, timeout, false)
}

func (r *CloudMailReader) ScanOpenAIDeactivationNotice(days, maxMessagesPerFolder int) (DeactivationResult, error) {
	if days < 1 {
		days = 90
	}
	minTimestamp := float64(time.Now().Unix()) - float64(days)*86400
	records, err := r.records(maxMessagesPerFolder, "")
	if err != nil {
		return DeactivationResult{}, err
	}
	var matches []MailRecord
	for _, record := range records {
		if record.MailTime != 0 && record.MailTime < minTimestamp {
			continue
		}
		if isOpenAIDeactivationNotice(record.Subject, record.From, record.Body) {
			record.Snippet = openaiDeactivationNoticeSnippet(record.Body, 260)
			matches = append(matches, record)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].MailTime > matches[j].MailTime })
	result := DeactivationResult{
		Found:                len(matches) > 0,
		Count:                len(matches),
		Matches:              limitRecords(matches, 5),
		Days:                 days,
		MaxMessagesPerFolder: maxMessagesPerFolder,
		ScannedMessages:      len(records),
		AliasMismatchCount:   0,
		CheckedAt:            nowISO(),
	}
	if len(matches) > 0 {
		latest := matches[0]
		result.Latest = &latest
	}
	return result, nil
}

func limitRecords(records []MailRecord, n int) []MailRecord {
	if len(records) <= n {
		return records
	}
	return records[:n]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Proxied IMAP dialing
// ---------------------------------------------------------------------------

// dialTLSViaProxy dials imapServerAddr through the given proxy and wraps it in
// TLS. proxyURL "" dials directly. SOCKS proxies use x/net/proxy; http/https
// proxies use a manual CONNECT tunnel (ProxiedIMAP4SSL in the Python source).
func dialTLSViaProxy(proxyURL string) (*tls.Conn, error) {
	tlsConfig := &tls.Config{ServerName: imapServerHost}
	if strings.TrimSpace(proxyURL) == "" {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 30 * time.Second}, "tcp", imapServerAddr, tlsConfig)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("IMAP 代理地址解析失败: %v", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "socks5", "socks5h", "socks":
		var auth *xproxy.Auth
		if parsed.User != nil {
			pass, _ := parsed.User.Password()
			auth = &xproxy.Auth{User: parsed.User.Username(), Password: pass}
		}
		dialer, err := xproxy.SOCKS5("tcp", parsed.Host, auth, &net.Dialer{Timeout: 30 * time.Second})
		if err != nil {
			return nil, err
		}
		raw, err := dialer.Dial("tcp", imapServerAddr)
		if err != nil {
			return nil, err
		}
		return tlsHandshake(raw, tlsConfig)
	case "http", "https", "":
		raw, err := httpConnectTunnel(parsed)
		if err != nil {
			return nil, err
		}
		return tlsHandshake(raw, tlsConfig)
	default:
		return nil, fmt.Errorf("IMAP 代理暂不支持: %s", proxyURL)
	}
}

func tlsHandshake(raw net.Conn, config *tls.Config) (*tls.Conn, error) {
	tlsConn := tls.Client(raw, config)
	_ = tlsConn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		raw.Close()
		return nil, err
	}
	_ = tlsConn.SetDeadline(time.Time{})
	return tlsConn, nil
}

// httpConnectTunnel mirrors HotmailOtpReader._connect_imap_via_proxy (HTTP CONNECT).
func httpConnectTunnel(parsed *url.URL) (net.Conn, error) {
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("IMAP 代理只支持 HTTP CONNECT: %s", parsed.String())
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	raw, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 30*time.Second)
	if err != nil {
		return nil, err
	}
	target := imapServerAddr
	lines := []string{
		fmt.Sprintf("CONNECT %s HTTP/1.1", target),
		fmt.Sprintf("Host: %s", target),
		"Proxy-Connection: keep-alive",
	}
	if parsed.User != nil {
		user := parsed.User.Username()
		pass, _ := parsed.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		lines = append(lines, "Proxy-Authorization: Basic "+token)
	}
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := raw.Write([]byte(strings.Join(lines, "\r\n") + "\r\n\r\n")); err != nil {
		raw.Close()
		return nil, err
	}
	var response []byte
	buf := make([]byte, 4096)
	for !bytes.Contains(response, []byte("\r\n\r\n")) && len(response) < 65536 {
		n, err := raw.Read(buf)
		if n > 0 {
			response = append(response, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	status := string(response)
	if idx := bytes.Index(response, []byte("\r\n")); idx >= 0 {
		status = string(response[:idx])
	}
	if !strings.Contains(" "+status+" ", " 200 ") {
		raw.Close()
		return nil, fmt.Errorf("IMAP 代理 CONNECT 失败: %s", status)
	}
	_ = raw.SetDeadline(time.Time{})
	return raw, nil
}

// xoauth2Client implements the SASL XOAUTH2 mechanism.
type xoauth2Client struct {
	username string
	token    string
}

func (c *xoauth2Client) Start() (string, []byte, error) {
	ir := []byte(fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", c.username, c.token))
	return "XOAUTH2", ir, nil
}

func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	return nil, errors.New("unexpected XOAUTH2 server challenge")
}

var _ sasl.Client = (*xoauth2Client)(nil)

// ---------------------------------------------------------------------------
// Hotmail OTP reader (IMAP / Graph / legacy Outlook REST)
// ---------------------------------------------------------------------------

// HotmailOtpReader mirrors the Python HotmailOtpReader.
type HotmailOtpReader struct {
	account  *models.MailAccount
	log      Log
	proxyURL string

	seen                 map[string]bool
	imapClient           *imapclient.Client
	graphClient          *tlsclient.Client
	graphConnected       bool
	graphAccessToken     string
	outlookAccessToken   string
	MailMode             string
	ScanFolders          []string
	oldCodeNoticeKeys    map[string]bool
	recentCodeCand       map[string]codeCandidate
	recentCodeNoticeAt   time.Time
	discoveringOnConnect bool
}

type codeCandidate struct {
	Folder   string
	Subject  string
	From     string
	To       string
	MailTime float64
}

// NewHotmailOtpReader mirrors HotmailOtpReader.__init__.
func NewHotmailOtpReader(account *models.MailAccount, log Log, proxyURL string) *HotmailOtpReader {
	account.Email = normalizeEmailAddress(account.Email)
	account.ReceiveMailbox = normalizeEmailAddress(account.ReceiveMailbox)
	return &HotmailOtpReader{
		account:           account,
		log:               log,
		proxyURL:          proxyURL,
		seen:              map[string]bool{},
		oldCodeNoticeKeys: map[string]bool{},
		recentCodeCand:    map[string]codeCandidate{},
	}
}

func (r *HotmailOtpReader) Connect() error {
	var lastError error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			r.log.emit(fmt.Sprintf("邮箱 IMAP 连接重试 %d/3", attempt))
		}
		err := r.connectOnce()
		if err == nil {
			return nil
		}
		var secErr *EmailSecurityInterruptError
		if errors.As(err, &secErr) {
			r.closeImapQuietly()
			return err
		}
		lastError = err
		r.closeImapQuietly()
		if attempt >= 3 {
			break
		}
		time.Sleep(time.Duration(2*attempt) * time.Second)
	}
	if lastError != nil {
		return lastError
	}
	return errors.New("邮箱 IMAP 连接失败")
}

func (r *HotmailOtpReader) closeImapQuietly() {
	if r.imapClient != nil {
		_ = r.imapClient.Close()
	}
	r.imapClient = nil
	r.graphClient = nil
	r.graphConnected = false
	r.graphAccessToken = ""
	r.outlookAccessToken = ""
	r.MailMode = ""
}

func (r *HotmailOtpReader) connectOnce() error {
	err := r.connectImapOnce()
	if err == nil {
		return nil
	}
	var secErr *EmailSecurityInterruptError
	if errors.As(err, &secErr) {
		return err
	}
	imapErr := err
	outlookAccessToken := r.outlookAccessToken
	r.closeImapQuietly()
	r.log.emit(fmt.Sprintf("Outlook IMAP 不可用，自动切换 Microsoft Graph 收信: %v", imapErr))

	graphErr := r.connectGraphOnce()
	if graphErr == nil {
		return nil
	}
	if errors.As(graphErr, &secErr) {
		return graphErr
	}
	r.closeImapQuietly()
	r.log.emit(fmt.Sprintf("Microsoft Graph 不可用，自动切换旧版 Outlook 邮件接口: %v", graphErr))

	outlookErr := r.connectOutlookRestOnce(outlookAccessToken)
	if outlookErr == nil {
		return nil
	}
	if errors.As(outlookErr, &secErr) {
		return outlookErr
	}
	return fmt.Errorf("Outlook 邮箱连接失败：IMAP=%s；Graph=%s；Outlook REST=%s",
		truncate(imapErr.Error(), 180), truncate(graphErr.Error(), 180), truncate(outlookErr.Error(), 180))
}

func (r *HotmailOtpReader) connectImapOnce() error {
	accountEmail := normalizeEmailAddress(r.account.Email)
	mailboxEmail := receiveMailboxForAccount(r.account)
	if !strings.EqualFold(mailboxEmail, accountEmail) {
		r.log.emit(fmt.Sprintf("正在连接邮箱取码: %s -> 接收主邮箱 %s", accountEmail, mailboxEmail))
	} else {
		r.log.emit(fmt.Sprintf("正在连接邮箱取码: %s", accountEmail))
	}
	accessToken, err := refreshHotmailAccessToken(r.account, r.log, r.proxyURL, "imap")
	if err != nil {
		return err
	}
	r.outlookAccessToken = accessToken

	if r.proxyURL != "" {
		conn, err := dialTLSViaProxy(r.proxyURL)
		if err != nil {
			return err
		}
		r.imapClient = imapclient.New(conn, nil)
	} else {
		r.log.emit("正在连接 Outlook IMAP: outlook.office365.com:993")
		conn, err := dialTLSViaProxy("")
		if err != nil {
			return err
		}
		r.imapClient = imapclient.New(conn, nil)
	}

	r.log.emit("正在进行邮箱 XOAUTH2 认证")
	if err := r.imapClient.Authenticate(&xoauth2Client{username: mailboxEmail, token: accessToken}); err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "authenticated but not connected") {
			return fmt.Errorf("Outlook IMAP 已拿到邮箱 token，但微软没有放行邮箱连接；通常是邮箱被风控、未完成安全验证、IMAP 不可用，或需要先网页登录 Outlook 激活邮箱: %w", err)
		}
		if strings.Contains(message, "authenticate failed") {
			return fmt.Errorf("Outlook IMAP XOAUTH2 认证失败；token 有效不代表邮箱允许 IMAP 读信，请先网页登录 Outlook 检查是否要求安全验证: %w", err)
		}
		return err
	}
	r.MailMode = "imap"
	r.log.emit("邮箱 IMAP 已连接，准备自动收 OpenAI 验证码")
	r.discoveringOnConnect = true
	r.ScanFolders = r.discoverScanFolders()
	r.discoveringOnConnect = false
	return nil
}

func (r *HotmailOtpReader) connectGraphOnce() error {
	token, err := refreshHotmailAccessToken(r.account, r.log, r.proxyURL, "graph")
	if err != nil {
		return err
	}
	r.graphAccessToken = token
	r.graphClient = tlsclient.NewOrNil(r.proxyURL, 30)
	r.graphConnected = true
	r.MailMode = "graph"
	payload, err := r.graphGetJSON("/v1.0/me/mailFolders/inbox/messages", map[string]string{"$top": "1", "$select": "id"}, false)
	if err != nil {
		return err
	}
	if asList(payload["value"]) == nil {
		return fmt.Errorf("Graph 邮箱接口返回异常: %s", truncate(fmt.Sprintf("%v", payload), 300))
	}
	r.ScanFolders = []string{"inbox", "junkemail", "deleteditems", "archive"}
	r.log.emit("Microsoft Graph 邮箱接口已连接，准备自动收 OpenAI 验证码")
	return nil
}

func (r *HotmailOtpReader) connectOutlookRestOnce(accessToken string) error {
	r.outlookAccessToken = strings.TrimSpace(accessToken)
	if r.outlookAccessToken == "" {
		token, err := refreshHotmailAccessToken(r.account, r.log, r.proxyURL, "imap")
		if err != nil {
			return err
		}
		r.outlookAccessToken = token
	}
	r.graphClient = tlsclient.NewOrNil(r.proxyURL, 30)
	r.graphConnected = true
	r.MailMode = "outlook"
	payload, err := r.graphGetJSON("/v2.0/me/mailfolders/inbox/messages", map[string]string{"$top": "1", "$select": "Id"}, false)
	if err != nil {
		return err
	}
	values := payload["value"]
	if asList(values) == nil {
		values = payload["Value"]
	}
	if asList(values) == nil {
		return fmt.Errorf("旧版 Outlook 邮件接口返回异常: %s", truncate(fmt.Sprintf("%v", payload), 300))
	}
	r.ScanFolders = []string{"inbox", "junkemail", "deleteditems", "archive"}
	r.log.emit("旧版 Outlook 邮件接口已连接，准备自动收 OpenAI 验证码")
	return nil
}

func (r *HotmailOtpReader) graphGetJSON(path string, params map[string]string, allowTokenRetry bool) (map[string]interface{}, error) {
	if !r.graphConnected || r.graphClient == nil {
		return nil, errors.New("Outlook 邮箱 API 会话未连接")
	}
	buildURL := func() string {
		var base string
		if strings.HasPrefix(path, "https://") {
			base = path
		} else if r.MailMode == "outlook" {
			base = "https://outlook.office.com/api" + path
		} else {
			base = "https://graph.microsoft.com" + path
		}
		if len(params) > 0 {
			values := url.Values{}
			for k, v := range params {
				values.Set(k, v)
			}
			base += "?" + values.Encode()
		}
		return base
	}
	authHeader := func() map[string]string {
		token := r.graphAccessToken
		if r.MailMode == "outlook" {
			token = r.outlookAccessToken
		}
		return map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + token,
			"Prefer":        `outlook.body-content-type="text"`,
		}
	}
	status, body, err := httpDo(r.graphClient, http.MethodGet, buildURL(), nil, authHeader())
	if err != nil {
		return nil, err
	}
	if status == 401 && allowTokenRetry {
		var token string
		var refErr error
		if r.MailMode == "outlook" {
			token, refErr = refreshHotmailAccessToken(r.account, r.log, r.proxyURL, "imap")
			r.outlookAccessToken = token
		} else {
			token, refErr = refreshHotmailAccessToken(r.account, r.log, r.proxyURL, "graph")
			r.graphAccessToken = token
		}
		if refErr != nil {
			return nil, refErr
		}
		status, body, err = httpDo(r.graphClient, http.MethodGet, buildURL(), nil, authHeader())
		if err != nil {
			return nil, err
		}
	}
	apiName := "Microsoft Graph"
	if r.MailMode == "outlook" {
		apiName = "旧版 Outlook API"
	}
	if !httpOK(status) {
		detail := ""
		var payload map[string]interface{}
		if json.Unmarshal(body, &payload) == nil {
			if m := asMap(payload["error"]); m != nil {
				detail = asString(m["message"])
			}
			if detail == "" {
				detail = asString(payload["error_description"])
			}
			if detail == "" {
				detail = fmt.Sprintf("%v", payload)
			}
		} else {
			detail = truncate(string(body), 300)
		}
		return nil, fmt.Errorf("%s HTTP %d: %s", apiName, status, truncate(detail, 300))
	}
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return nil, fmt.Errorf("%s 返回非 JSON: %s", apiName, truncate(string(body), 300))
	}
	return payload, nil
}

func (r *HotmailOtpReader) Close() error {
	if r.imapClient != nil {
		_ = r.imapClient.Logout().Wait()
		_ = r.imapClient.Close()
	}
	r.imapClient = nil
	r.graphClient = nil
	r.graphConnected = false
	r.graphAccessToken = ""
	r.outlookAccessToken = ""
	r.MailMode = ""
	return nil
}

func (r *HotmailOtpReader) discoverScanFolders() []string {
	defaults := []string{"INBOX", "Junk", "Junk Email", "Junk E-mail", "Spam", "Deleted Items", "Archive"}
	var folders []string
	seen := map[string]bool{}
	add := func(name string) {
		folder := strings.Trim(strings.TrimSpace(name), `"`)
		if folder == "" {
			return
		}
		key := strings.ToLower(folder)
		if seen[key] {
			return
		}
		seen[key] = true
		folders = append(folders, folder)
	}
	for _, folder := range defaults {
		add(folder)
	}

	loadFromImap := func() error {
		if r.imapClient == nil {
			return errors.New("IMAP 未连接")
		}
		listData, err := r.imapClient.List("", "*", nil).Collect()
		if err != nil {
			return err
		}
		type listed struct{ name, marker string }
		var items []listed
		for _, item := range listData {
			if item == nil || item.Mailbox == "" {
				continue
			}
			noselect := false
			for _, attr := range item.Attrs {
				if attr == imap.MailboxAttrNoSelect {
					noselect = true
					break
				}
			}
			if noselect {
				continue
			}
			var flags []string
			for _, attr := range item.Attrs {
				flags = append(flags, string(attr))
			}
			items = append(items, listed{name: item.Mailbox, marker: strings.ToLower(item.Mailbox + " " + strings.Join(flags, " "))})
		}
		keywords := []string{
			"inbox", "junk", "spam", "bulk", "deleted", "archive", "other",
			"垃圾", "垃圾邮件", "废件", "收件", "存档", "已删除",
		}
		for _, it := range items {
			hit := false
			for _, kw := range keywords {
				if strings.Contains(it.marker, strings.ToLower(kw)) {
					hit = true
					break
				}
			}
			if hit || strings.Contains(it.marker, strings.ToLower("\\Junk")) || strings.Contains(it.marker, strings.ToLower("\\Inbox")) {
				add(it.name)
			}
		}
		for _, it := range items {
			add(it.name)
		}
		return nil
	}

	if err := loadFromImap(); err != nil {
		if r.discoveringOnConnect {
			r.log.emit(fmt.Sprintf("列出邮箱文件夹失败，使用默认文件夹扫描: %v", err))
		} else {
			r.log.emit(fmt.Sprintf("列出邮箱文件夹失败，准备重连后重试: %v", err))
			if r.reconnectAfterImapError("列出邮箱文件夹", err) {
				if r.MailMode == "graph" || r.MailMode == "outlook" {
					if len(r.ScanFolders) > 0 {
						return append([]string(nil), r.ScanFolders...)
					}
					return []string{"inbox", "junkemail", "deleteditems", "archive"}
				}
				if retryErr := loadFromImap(); retryErr != nil {
					r.log.emit(fmt.Sprintf("列出邮箱文件夹重试失败，使用默认文件夹扫描: %v", retryErr))
				}
			} else {
				r.log.emit("邮箱重连失败，使用默认文件夹扫描")
			}
		}
	}

	if len(folders) > 0 {
		preview := strings.Join(folders[:minInt(8, len(folders))], ", ")
		if len(folders) > 8 {
			preview += fmt.Sprintf(" ... 共 %d 个", len(folders))
		}
		r.log.emit(fmt.Sprintf("邮箱验证码扫描文件夹: %s", preview))
		return folders
	}
	return defaults
}

func (r *HotmailOtpReader) reconnectAfterImapError(action string, cause error) bool {
	if r.MailMode != "imap" {
		return false
	}
	r.log.emit(fmt.Sprintf("%s时 IMAP 连接异常，正在自动重连: %v", action, cause))
	r.closeImapQuietly()
	if err := r.Connect(); err != nil {
		r.log.emit(fmt.Sprintf("%s后自动重连失败: %v", action, err))
		return false
	}
	return r.imapClient != nil || r.graphClient != nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- IMAP low-level fetch ----

type rawMessage struct {
	SeqNum uint32
	Raw    []byte
}

func (r *HotmailOtpReader) selectFolder(folder string) (uint32, bool) {
	if r.imapClient == nil {
		return 0, false
	}
	data, err := r.imapClient.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || data == nil {
		return 0, false
	}
	return data.NumMessages, true
}

func (r *HotmailOtpReader) fetchLastRawMessages(folder string, maxN int) ([]rawMessage, bool) {
	num, ok := r.selectFolder(folder)
	if !ok {
		return nil, false
	}
	if num == 0 {
		return nil, true
	}
	if maxN < 1 {
		maxN = 1
	}
	start := uint32(1)
	if num > uint32(maxN) {
		start = num - uint32(maxN) + 1
	}
	var seqSet imap.SeqSet
	seqSet.AddRange(start, num)
	bs := &imap.FetchItemBodySection{}
	buffers, err := r.imapClient.Fetch(seqSet, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{bs}}).Collect()
	if err != nil {
		return nil, false
	}
	out := make([]rawMessage, 0, len(buffers))
	for _, buf := range buffers {
		out = append(out, rawMessage{SeqNum: buf.SeqNum, Raw: buf.FindBodySection(bs)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SeqNum > out[j].SeqNum })
	return out, true
}

// ---- record building ----

func (r *HotmailOtpReader) mailMessageRecord(raw []byte, folder, msgID string, includeBody bool) MailRecord {
	header, leaves := collectLeafParts(raw)
	mailTime := headerDateTimestamp(header)
	var dateHeader string
	subject, fromAddr, toAddr := "", "", ""
	if header != nil {
		dateHeader = header.Get("Date")
		subject = decodeHeaderText(header.Get("Subject"))
		fromAddr = decodeHeaderText(header.Get("From"))
		toAddr = messageRecipientHeaders(header)
		if toAddr == "" {
			toAddr = decodeHeaderText(header.Get("To"))
		}
	}
	body := extractMessageText(leaves)
	haystack := subject + "\n" + fromAddr + "\n" + toAddr + "\n" + body
	code := ""
	if reOpenAIChatGPT.MatchString(haystack) {
		code = extractOpenAICode(haystack)
	}
	record := MailRecord{
		ID:          msgID,
		Folder:      folder,
		Kind:        classifyKind(subject, fromAddr, body, haystack, code),
		Code:        code,
		Subject:     subject,
		From:        fromAddr,
		To:          toAddr,
		Date:        dateHeader,
		MailTime:    mailTime,
		MailTimeISO: isoFromTimestamp(mailTime),
		Snippet:     openaiDeactivationNoticeSnippet(body, 220),
	}
	if includeBody {
		record.Body = body
	}
	return record
}

func (r *HotmailOtpReader) graphMessageRecord(item map[string]interface{}, folder string, includeBody bool) MailRecord {
	received := asString(mapGet(item, "receivedDateTime", "ReceivedDateTime"))
	mailTime := float64(time.Now().Unix())
	if received != "" {
		if ts, ok := parseISOTime(received); ok {
			mailTime = ts
		}
	}
	subject := asString(mapGet(item, "subject", "Subject"))
	senderRecord := asMapOr(mapGet(item, "from", "From"))
	sender := asMapOr(mapGet(senderRecord, "emailAddress", "EmailAddress"))
	fromName := strings.TrimSpace(asString(mapGet(sender, "name", "Name")))
	fromEmail := strings.TrimSpace(asString(mapGet(sender, "address", "Address")))
	fromAddr := fromEmail
	if fromName != "" && fromEmail != "" {
		fromAddr = fmt.Sprintf("%s <%s>", fromName, fromEmail)
	} else if fromEmail == "" {
		fromAddr = fromName
	}
	var toValues []string
	for _, rec := range asList(mapGet(item, "toRecipients", "ToRecipients")) {
		recMap := asMapOr(rec)
		emailAddr := asMapOr(mapGet(recMap, "emailAddress", "EmailAddress"))
		address := strings.TrimSpace(asString(mapGet(emailAddr, "address", "Address")))
		name := strings.TrimSpace(asString(mapGet(emailAddr, "name", "Name")))
		if address != "" {
			if name != "" {
				toValues = append(toValues, fmt.Sprintf("%s <%s>", name, address))
			} else {
				toValues = append(toValues, address)
			}
		}
	}
	toAddr := strings.Join(toValues, ", ")
	bodyRecord := asMapOr(mapGet(item, "body", "Body"))
	body := asString(mapGet(bodyRecord, "content", "Content"))
	if body == "" {
		body = asString(mapGet(item, "bodyPreview", "BodyPreview"))
	}
	if strings.EqualFold(asString(mapGet(bodyRecord, "contentType", "ContentType")), "html") {
		body = htmlToText(body)
	}
	haystack := subject + "\n" + fromAddr + "\n" + toAddr + "\n" + body
	code := ""
	if reOpenAIChatGPT.MatchString(haystack) {
		code = extractOpenAICode(haystack)
	}
	record := MailRecord{
		ID:          asString(mapGet(item, "id", "Id")),
		Folder:      folder,
		Kind:        classifyKind(subject, fromAddr, body, haystack, code),
		Code:        code,
		Subject:     subject,
		From:        fromAddr,
		To:          toAddr,
		Date:        received,
		MailTime:    mailTime,
		MailTimeISO: isoFromTimestamp(mailTime),
		Snippet:     openaiDeactivationNoticeSnippet(body, 220),
	}
	if includeBody {
		record.Body = body
	}
	return record
}

func parseISOTime(s string) (float64, bool) {
	iso := strings.Replace(s, "Z", "+00:00", 1)
	for _, layout := range []string{"2006-01-02T15:04:05.999999999-07:00", "2006-01-02T15:04:05-07:00", time.RFC3339, time.RFC3339Nano} {
		if t, err := time.Parse(layout, iso); err == nil {
			return float64(t.Unix()), true
		}
	}
	return 0, false
}

func (r *HotmailOtpReader) graphListMessageItems(folder string, maxMessages int) ([]map[string]interface{}, error) {
	if maxMessages <= 0 {
		maxMessages = 80
	}
	maxMessages = clampInt(maxMessages, 1, 500)
	outlookMode := r.MailMode == "outlook"
	var path, orderBy, selectFields string
	if outlookMode {
		path = "/v2.0/me/mailfolders/" + url.PathEscape(folder) + "/messages"
		orderBy = "ReceivedDateTime desc"
		selectFields = "Id,Subject,From,ToRecipients,ReceivedDateTime,BodyPreview,Body,IsRead"
	} else {
		path = "/v1.0/me/mailFolders/" + url.PathEscape(folder) + "/messages"
		orderBy = "receivedDateTime desc"
		selectFields = "id,subject,from,toRecipients,receivedDateTime,bodyPreview,body,isRead"
	}
	payload, err := r.graphGetJSON(path, map[string]string{
		"$top":     strconv.Itoa(maxMessages),
		"$orderby": orderBy,
		"$select":  selectFields,
	}, true)
	if err != nil {
		return nil, err
	}
	values := payload["value"]
	if asList(values) == nil {
		values = payload["Value"]
	}
	return dictList(asList(values)), nil
}

func (r *HotmailOtpReader) graphListRecentMessages(folder string, maxMessages int, query string) ([]MailRecord, error) {
	queryText := strings.ToLower(strings.TrimSpace(query))
	items, err := r.graphListMessageItems(folder, maxMessages)
	if err != nil {
		return nil, err
	}
	var messages []MailRecord
	for _, item := range items {
		record := r.graphMessageRecord(item, folder, false)
		if queryText != "" {
			haystack := strings.ToLower(strings.Join([]string{record.Kind, record.Subject, record.From, record.To, record.Snippet, record.Code}, "\n"))
			if !strings.Contains(haystack, queryText) {
				continue
			}
		}
		messages = append(messages, record)
	}
	return messages, nil
}

func (r *HotmailOtpReader) graphFetchMessage(folder, messageID string) (MailRecord, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return MailRecord{}, errors.New("邮件 ID 为空")
	}
	outlookMode := r.MailMode == "outlook"
	var path, selectFields string
	if outlookMode {
		path = "/v2.0/me/messages/" + url.PathEscape(messageID)
		selectFields = "Id,Subject,From,ToRecipients,ReceivedDateTime,BodyPreview,Body,IsRead"
	} else {
		path = "/v1.0/me/messages/" + url.PathEscape(messageID)
		selectFields = "id,subject,from,toRecipients,receivedDateTime,bodyPreview,body,isRead"
	}
	item, err := r.graphGetJSON(path, map[string]string{"$select": selectFields}, true)
	if err != nil {
		return MailRecord{}, err
	}
	return r.graphMessageRecord(item, folder, true), nil
}

// ---- public reader API ----

func (r *HotmailOtpReader) ensureConnected() error {
	if r.imapClient == nil && r.graphClient == nil {
		return r.Connect()
	}
	return nil
}

func (r *HotmailOtpReader) ListFolders() ([]string, error) {
	if err := r.ensureConnected(); err != nil {
		return nil, err
	}
	if r.MailMode == "graph" || r.MailMode == "outlook" {
		if len(r.ScanFolders) > 0 {
			return append([]string(nil), r.ScanFolders...), nil
		}
		return []string{"inbox", "junkemail", "deleteditems", "archive"}, nil
	}
	if len(r.ScanFolders) > 0 {
		return append([]string(nil), r.ScanFolders...), nil
	}
	return r.discoverScanFolders(), nil
}

func (r *HotmailOtpReader) ListRecentMessages(folder string, maxMessages int, query string) ([]MailRecord, error) {
	if err := r.ensureConnected(); err != nil {
		return nil, err
	}
	if r.MailMode == "graph" || r.MailMode == "outlook" {
		return r.graphListRecentMessages(folder, maxMessages, query)
	}
	messages, ok := r.fetchLastRawMessages(folder, clampInt(maxMessages, 1, 500))
	if !ok {
		return nil, fmt.Errorf("无法打开邮箱文件夹: %s", folder)
	}
	queryText := strings.ToLower(strings.TrimSpace(query))
	var out []MailRecord
	for _, msg := range messages {
		record := r.mailMessageRecord(msg.Raw, folder, strconv.FormatUint(uint64(msg.SeqNum), 10), false)
		if queryText != "" {
			haystack := strings.ToLower(strings.Join([]string{record.Kind, record.Subject, record.From, record.To, record.Snippet, record.Code}, "\n"))
			if !strings.Contains(haystack, queryText) {
				continue
			}
		}
		out = append(out, record)
	}
	return out, nil
}

func (r *HotmailOtpReader) FetchMessage(folder, messageID string) (MailRecord, error) {
	if err := r.ensureConnected(); err != nil {
		return MailRecord{}, err
	}
	if r.MailMode == "graph" || r.MailMode == "outlook" {
		return r.graphFetchMessage(folder, messageID)
	}
	num, ok := r.selectFolder(folder)
	if !ok {
		return MailRecord{}, fmt.Errorf("无法打开邮箱文件夹: %s", folder)
	}
	seqStr := strings.TrimSpace(messageID)
	if seqStr == "" {
		return MailRecord{}, errors.New("邮件 ID 为空")
	}
	seq, err := strconv.ParseUint(seqStr, 10, 32)
	if err != nil || seq == 0 || uint32(seq) > num {
		return MailRecord{}, fmt.Errorf("读取邮件失败: 无效邮件 ID %s", seqStr)
	}
	var seqSet imap.SeqSet
	seqSet.AddRange(uint32(seq), uint32(seq))
	bs := &imap.FetchItemBodySection{}
	buffers, err := r.imapClient.Fetch(seqSet, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{bs}}).Collect()
	if err != nil || len(buffers) == 0 {
		return MailRecord{}, fmt.Errorf("读取邮件失败")
	}
	raw := buffers[0].FindBodySection(bs)
	if len(raw) == 0 {
		return MailRecord{}, errors.New("读取邮件失败: 内容为空")
	}
	return r.mailMessageRecord(raw, folder, seqStr, true), nil
}

func (r *HotmailOtpReader) WaitForCode(ctx context.Context, minTimestamp float64, timeout, lookbackSeconds int) (string, error) {
	if r.imapClient == nil && r.graphClient == nil {
		if err := r.Connect(); err != nil {
			r.log.emit(fmt.Sprintf("邮箱取码连接失败: %v", err))
			return "", err
		}
	}
	if timeout < 1 {
		timeout = 1
	}
	effectiveMin := minTimestamp - float64(maxInt(0, lookbackSeconds))
	if effectiveMin < 0 {
		effectiveMin = 0
	}
	if effectiveMin != 0 && effectiveMin < minTimestamp {
		r.log.emit(fmt.Sprintf("邮箱验证码启用宽松回看窗口: %ds", lookbackSeconds))
	}
	started := time.Now()
	var lastNotice, lastFolderRefresh time.Time
	folders := r.ScanFolders
	if len(folders) == 0 {
		folders = r.discoverScanFolders()
	}
	deadline := started.Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		now := time.Now()
		if r.MailMode == "imap" && now.Sub(lastFolderRefresh) >= emailOTPFolderRediscoverSeconds*time.Second {
			folders = r.discoverScanFolders()
			lastFolderRefresh = now
		}
		if r.imapClient != nil {
			if err := r.imapClient.Noop().Wait(); err != nil {
				if r.reconnectAfterImapError("邮箱验证码轮询", err) {
					folders = r.ScanFolders
					if len(folders) == 0 {
						folders = r.discoverScanFolders()
					}
				}
			}
		}
		for _, folder := range folders {
			code := r.scanFolder(folder, effectiveMin)
			if code != "" {
				return code, nil
			}
		}
		elapsed := time.Since(started)
		if time.Since(lastNotice) >= 20*time.Second {
			remain := maxInt(0, timeout-int(elapsed.Seconds()))
			r.log.emit(fmt.Sprintf("仍在等待 OpenAI 新验证码邮件，剩余约 %ds", remain))
			r.logRecentCodeCandidates(effectiveMin, false)
			lastNotice = time.Now()
		}
		interval := emailOTPFastPollSeconds
		if elapsed >= emailOTPFastPollWindowSeconds*time.Second {
			interval = emailOTPSlowPollSeconds
		}
		if err := sleepCtx(ctx, time.Duration(interval)*time.Second); err != nil {
			return "", err
		}
	}
	r.logRecentCodeCandidates(effectiveMin, true)
	return "", errors.New("等待 OpenAI 邮箱验证码超时")
}

func (r *HotmailOtpReader) rememberCodeCandidate(key, folder, subject, fromAddr, toAddr string, mailTime float64) {
	r.recentCodeCand[key] = codeCandidate{Folder: folder, Subject: subject, From: fromAddr, To: toAddr, MailTime: mailTime}
	if len(r.recentCodeCand) > 12 {
		type kv struct {
			key  string
			cand codeCandidate
		}
		var items []kv
		for k, v := range r.recentCodeCand {
			items = append(items, kv{k, v})
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].cand.MailTime > items[j].cand.MailTime })
		next := map[string]codeCandidate{}
		for _, it := range items[:12] {
			next[it.key] = it.cand
		}
		r.recentCodeCand = next
	}
}

func (r *HotmailOtpReader) logRecentCodeCandidates(minTimestamp float64, force bool) {
	if len(r.recentCodeCand) == 0 {
		return
	}
	now := time.Now()
	if !force && now.Sub(r.recentCodeNoticeAt) < 60*time.Second {
		return
	}
	r.recentCodeNoticeAt = now
	var items []codeCandidate
	for _, v := range r.recentCodeCand {
		items = append(items, v)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].MailTime > items[j].MailTime })
	if len(items) > 3 {
		items = items[:3]
	}
	var parts []string
	nowUnix := float64(now.Unix())
	for _, item := range items {
		age := 0
		lag := 0
		if item.MailTime != 0 {
			age = int(maxFloat(0, nowUnix-item.MailTime))
			lag = int(maxFloat(0, minTimestamp-item.MailTime))
		}
		var ageText string
		if age >= 3600 {
			ageText = fmt.Sprintf("%dh%dm前", age/3600, (age%3600)/60)
		} else {
			ageText = fmt.Sprintf("%dm%ds前", age/60, age%60)
		}
		lagText := ""
		if lag != 0 {
			lagText = fmt.Sprintf("，早于窗口%ds", lag)
		}
		toAddr := truncate(strings.ReplaceAll(orDash(item.To), "\n", " "), 80)
		subject := truncate(strings.ReplaceAll(orDash(item.Subject), "\n", " "), 80)
		parts = append(parts, fmt.Sprintf("%s: %s%s, To=%s, Subject=%s", item.Folder, ageText, lagText, toAddr, subject))
	}
	r.log.emit("最近发现的 OpenAI 验证码邮件: " + strings.Join(parts, "；"))
	receiveMailbox := receiveMailboxForAccount(r.account)
	if !strings.EqualFold(receiveMailbox, r.account.Email) {
		r.log.emit(fmt.Sprintf("当前注册邮箱 %s 共用接收主邮箱 %s；程序只会采用原始收件人匹配当前注册邮箱的验证码。", r.account.Email, receiveMailbox))
	}
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (r *HotmailOtpReader) scanFolder(folder string, minTimestamp float64) string {
	if r.MailMode == "graph" || r.MailMode == "outlook" {
		return r.graphScanFolder(folder, minTimestamp)
	}
	messages, ok := r.fetchLastRawMessages(folder, 80)
	if !ok {
		return ""
	}
	for _, msg := range messages {
		seqStr := strconv.FormatUint(uint64(msg.SeqNum), 10)
		key := folder + ":" + seqStr
		if r.seen[key] {
			continue
		}
		header, leaves := collectLeafParts(msg.Raw)
		mailTime := headerDateTimestamp(header)
		subject, fromAddr, toAddr := "", "", ""
		if header != nil {
			subject = decodeHeaderText(header.Get("Subject"))
			fromAddr = decodeHeaderText(header.Get("From"))
			toAddr = messageRecipientHeaders(header)
			if toAddr == "" {
				toAddr = decodeHeaderText(header.Get("To"))
			}
		}
		body := extractMessageText(leaves)
		haystack := subject + "\n" + fromAddr + "\n" + body
		if !reOpenAIChatGPT.MatchString(haystack) {
			r.seen[key] = true
			continue
		}
		code := extractOpenAICode(haystack)
		if code != "" {
			r.rememberCodeCandidate(key, folder, subject, fromAddr, toAddr, mailTime)
			if !recipientTextMatchesAccount(r.account, toAddr) {
				r.seen[key] = true
				continue
			}
		}
		if mailTime+30 < minTimestamp {
			if code != "" {
				noticeKey := folder + ":" + seqStr + ":old"
				if !r.oldCodeNoticeKeys[noticeKey] {
					r.oldCodeNoticeKeys[noticeKey] = true
					lag := int(maxFloat(0, minTimestamp-mailTime))
					r.log.emit(fmt.Sprintf("发现 OpenAI 验证码邮件但时间早于等待窗口约 %ds，已跳过: %s", lag, truncate(subject, 80)))
				}
			}
			r.seen[key] = true
			continue
		}
		r.seen[key] = true
		if code != "" {
			r.log.emit(fmt.Sprintf("收到 OpenAI 验证码: %s", code))
			return code
		}
	}
	return ""
}

func (r *HotmailOtpReader) graphScanFolder(folder string, minTimestamp float64) string {
	items, err := r.graphListMessageItems(folder, 80)
	if err != nil {
		return ""
	}
	for _, item := range items {
		record := r.graphMessageRecord(item, folder, true)
		key := "graph:" + folder + ":" + record.ID
		if r.seen[key] {
			continue
		}
		haystack := strings.Join([]string{record.Subject, record.From, record.Body}, "\n")
		if !reOpenAIChatGPT.MatchString(haystack) {
			r.seen[key] = true
			continue
		}
		code := record.Code
		mailTime := record.MailTime
		if code != "" {
			r.rememberCodeCandidate(key, folder, record.Subject, record.From, record.To, mailTime)
			if !recipientTextMatchesAccount(r.account, record.To) {
				r.seen[key] = true
				continue
			}
		}
		if mailTime+30 < minTimestamp {
			if code != "" && !r.oldCodeNoticeKeys[key+":old"] {
				r.oldCodeNoticeKeys[key+":old"] = true
				lag := int(maxFloat(0, minTimestamp-mailTime))
				r.log.emit(fmt.Sprintf("发现 OpenAI 验证码邮件但时间早于等待窗口约 %ds，已跳过: %s", lag, truncate(record.Subject, 80)))
			}
			r.seen[key] = true
			continue
		}
		r.seen[key] = true
		if code != "" {
			r.log.emit(fmt.Sprintf("收到 OpenAI 验证码: %s", code))
			return code
		}
	}
	return ""
}

func (r *HotmailOtpReader) ScanOpenAIDeactivationNotice(days, maxMessagesPerFolder int) (DeactivationResult, error) {
	if r.imapClient == nil && r.graphClient == nil {
		if err := r.Connect(); err != nil {
			r.log.emit(fmt.Sprintf("邮箱封禁邮件检查连接失败: %v", err))
			return DeactivationResult{}, err
		}
	}
	if days < 1 {
		days = 90
	}
	if maxMessagesPerFolder < 10 {
		maxMessagesPerFolder = 120
	}
	minTimestamp := float64(time.Now().Unix()) - float64(days)*86400
	folders := r.ScanFolders
	if len(folders) == 0 {
		folders = r.discoverScanFolders()
	}
	var matches []MailRecord
	aliasMismatch := 0
	scanned := 0
	for _, folder := range folders {
		fMatches, fAlias, fScanned := r.scanFolderDeactivation(folder, minTimestamp, maxMessagesPerFolder)
		matches = append(matches, fMatches...)
		aliasMismatch += fAlias
		scanned += fScanned
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].MailTime > matches[j].MailTime })
	result := DeactivationResult{
		Found:                len(matches) > 0,
		Count:                len(matches),
		Matches:              limitRecords(matches, 5),
		Days:                 days,
		MaxMessagesPerFolder: maxMessagesPerFolder,
		ScannedMessages:      scanned,
		AliasMismatchCount:   aliasMismatch,
		CheckedAt:            nowISO(),
	}
	if len(matches) > 0 {
		latest := matches[0]
		result.Latest = &latest
	}
	return result, nil
}

func (r *HotmailOtpReader) scanFolderDeactivation(folder string, minTimestamp float64, maxMessagesPerFolder int) ([]MailRecord, int, int) {
	if r.MailMode == "graph" || r.MailMode == "outlook" {
		return r.graphScanFolderDeactivation(folder, minTimestamp, maxMessagesPerFolder)
	}
	messages, ok := r.fetchLastRawMessages(folder, maxMessagesPerFolder)
	if !ok {
		return nil, 0, 0
	}
	var matches []MailRecord
	aliasMismatch := 0
	scanned := 0
	for _, msg := range messages {
		scanned++
		header, leaves := collectLeafParts(msg.Raw)
		mailTime := headerDateTimestamp(header)
		if minTimestamp != 0 && mailTime+60 < minTimestamp {
			continue
		}
		subject, fromAddr, toAddr := "", "", ""
		var dateHeader string
		if header != nil {
			dateHeader = header.Get("Date")
			subject = decodeHeaderText(header.Get("Subject"))
			fromAddr = decodeHeaderText(header.Get("From"))
			toAddr = messageRecipientHeaders(header)
			if toAddr == "" {
				toAddr = decodeHeaderText(header.Get("To"))
			}
		}
		body := extractMessageText(leaves)
		if !isOpenAIDeactivationNotice(subject, fromAddr, body) {
			continue
		}
		if !openaiDeactivationNoticeMatchesAccount(r.account.Email, toAddr, body) {
			aliasMismatch++
			continue
		}
		matches = append(matches, MailRecord{
			Folder:      folder,
			Subject:     subject,
			From:        fromAddr,
			To:          toAddr,
			Date:        dateHeader,
			MailTime:    mailTime,
			MailTimeISO: isoFromTimestamp(mailTime),
			Snippet:     openaiDeactivationNoticeSnippet(body, 260),
		})
	}
	return matches, aliasMismatch, scanned
}

func (r *HotmailOtpReader) graphScanFolderDeactivation(folder string, minTimestamp float64, maxMessagesPerFolder int) ([]MailRecord, int, int) {
	items, err := r.graphListMessageItems(folder, maxMessagesPerFolder)
	if err != nil {
		return nil, 0, 0
	}
	var matches []MailRecord
	aliasMismatch := 0
	scanned := 0
	for _, item := range items {
		scanned++
		record := r.graphMessageRecord(item, folder, true)
		if minTimestamp != 0 && record.MailTime+60 < minTimestamp {
			continue
		}
		if !isOpenAIDeactivationNotice(record.Subject, record.From, record.Body) {
			continue
		}
		if !openaiDeactivationNoticeMatchesAccount(r.account.Email, record.To, record.Body) {
			aliasMismatch++
			continue
		}
		matches = append(matches, MailRecord{
			Folder:      folder,
			Subject:     record.Subject,
			From:        record.From,
			To:          record.To,
			Date:        record.Date,
			MailTime:    record.MailTime,
			MailTimeISO: record.MailTimeISO,
			Snippet:     openaiDeactivationNoticeSnippet(record.Body, 260),
		})
	}
	return matches, aliasMismatch, scanned
}

func (r *HotmailOtpReader) WaitForTeamInvite(ctx context.Context, minTimestamp float64, timeout int) (string, error) {
	if r.imapClient == nil && r.graphClient == nil {
		if err := r.Connect(); err != nil {
			r.log.emit(fmt.Sprintf("Team 邀请邮件扫描连接失败: %v", err))
			return "", err
		}
	}
	if timeout < 1 {
		timeout = 1
	}
	started := time.Now()
	var lastNotice, lastFolderRefresh time.Time
	folders := r.ScanFolders
	if len(folders) == 0 {
		folders = r.discoverScanFolders()
	}
	deadline := started.Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		now := time.Now()
		if r.MailMode == "imap" && now.Sub(lastFolderRefresh) >= emailOTPFolderRediscoverSeconds*time.Second {
			folders = r.discoverScanFolders()
			lastFolderRefresh = now
		}
		for _, folder := range folders {
			inviteURL := r.scanFolderTeamInvite(folder, minTimestamp)
			if inviteURL != "" {
				r.log.emit(fmt.Sprintf("收到 ChatGPT Team 邀请链接: %s", truncate(inviteURL, 140)))
				return inviteURL, nil
			}
		}
		if time.Since(lastNotice) >= 20*time.Second {
			remain := maxInt(0, timeout-int(time.Since(started).Seconds()))
			r.log.emit(fmt.Sprintf("仍在扫描 ChatGPT Team 邀请邮件，剩余约 %ds", remain))
			lastNotice = time.Now()
		}
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return "", err
		}
	}
	return "", errors.New("等待 ChatGPT Team 邀请邮件超时")
}

func (r *HotmailOtpReader) scanFolderTeamInvite(folder string, minTimestamp float64) string {
	if r.MailMode == "graph" || r.MailMode == "outlook" {
		return r.graphScanFolderTeamInvite(folder, minTimestamp)
	}
	messages, ok := r.fetchLastRawMessages(folder, 100)
	if !ok {
		return ""
	}
	for _, msg := range messages {
		header, leaves := collectLeafParts(msg.Raw)
		mailTime := headerDateTimestamp(header)
		if minTimestamp != 0 && mailTime+60 < minTimestamp {
			continue
		}
		subject, fromAddr := "", ""
		if header != nil {
			subject = decodeHeaderText(header.Get("Subject"))
			fromAddr = decodeHeaderText(header.Get("From"))
		}
		body := extractMessageLinkText(leaves)
		haystack := strings.Join([]string{subject, fromAddr, body}, "\n")
		if !reTeamInviteWords.MatchString(haystack) {
			continue
		}
		if inviteURL := extractChatGPTTeamInviteURL(haystack); inviteURL != "" {
			return inviteURL
		}
	}
	return ""
}

func (r *HotmailOtpReader) graphScanFolderTeamInvite(folder string, minTimestamp float64) string {
	items, err := r.graphListMessageItems(folder, 100)
	if err != nil {
		return ""
	}
	for _, item := range items {
		record := r.graphMessageRecord(item, folder, true)
		if minTimestamp != 0 && record.MailTime+60 < minTimestamp {
			continue
		}
		haystack := strings.Join([]string{record.Subject, record.From, record.Body}, "\n")
		if !reTeamInviteWords.MatchString(haystack) {
			continue
		}
		if inviteURL := extractChatGPTTeamInviteURL(haystack); inviteURL != "" {
			return inviteURL
		}
	}
	return ""
}

func (r *HotmailOtpReader) WaitForLink(ctx context.Context, keyword string, minTimestamp float64, timeout int) (string, error) {
	if r.imapClient == nil && r.graphClient == nil {
		if err := r.Connect(); err != nil {
			r.log.emit(fmt.Sprintf("邮箱取链接连接失败: %v", err))
			return "", err
		}
	}
	if timeout < 1 {
		timeout = 1
	}
	started := time.Now()
	var lastNotice time.Time
	folders := r.ScanFolders
	if len(folders) == 0 {
		folders = r.discoverScanFolders()
	}
	deadline := started.Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		for _, folder := range folders {
			link := r.scanFolderLink(folder, keyword, minTimestamp)
			if link != "" {
				return link, nil
			}
		}
		if time.Since(lastNotice) >= 20*time.Second {
			remain := maxInt(0, timeout-int(time.Since(started).Seconds()))
			r.log.emit(fmt.Sprintf("仍在等待 K12 邀请邮件，剩余约 %ds", remain))
			lastNotice = time.Now()
		}
		if err := sleepCtx(ctx, 5*time.Second); err != nil {
			return "", err
		}
	}
	return "", errors.New("等待 K12 邀请邮件超时")
}

func (r *HotmailOtpReader) scanFolderLink(folder, keyword string, minTimestamp float64) string {
	if r.MailMode == "graph" || r.MailMode == "outlook" {
		return r.graphScanFolderLink(folder, keyword, minTimestamp)
	}
	messages, ok := r.fetchLastRawMessages(folder, 50)
	if !ok {
		return ""
	}
	keywordText := strings.TrimSpace(keyword)
	for _, msg := range messages {
		header, leaves := collectLeafParts(msg.Raw)
		mailTime := headerDateTimestamp(header)
		if minTimestamp != 0 && mailTime+30 < minTimestamp {
			continue
		}
		subject, fromAddr := "", ""
		if header != nil {
			subject = decodeHeaderText(header.Get("Subject"))
			fromAddr = decodeHeaderText(header.Get("From"))
		}
		body := extractMessageLinkText(leaves)
		haystack := strings.Join([]string{subject, fromAddr, body}, "\n")
		if keywordText != "" && !strings.Contains(strings.ToLower(haystack), strings.ToLower(keywordText)) {
			continue
		}
		if !reLinkScanWords.MatchString(haystack) {
			continue
		}
		for _, link := range extractLinksFromText(haystack) {
			if keywordText == "" || strings.Contains(strings.ToLower(link), strings.ToLower(keywordText)) || strings.Contains(strings.ToLower(haystack), strings.ToLower(keywordText)) {
				r.log.emit(fmt.Sprintf("收到 K12 邀请链接: %s", truncate(link, 120)))
				return link
			}
		}
	}
	return ""
}

func (r *HotmailOtpReader) graphScanFolderLink(folder, keyword string, minTimestamp float64) string {
	keywordText := strings.TrimSpace(keyword)
	items, err := r.graphListMessageItems(folder, 50)
	if err != nil {
		return ""
	}
	for _, item := range items {
		record := r.graphMessageRecord(item, folder, true)
		if minTimestamp != 0 && record.MailTime+30 < minTimestamp {
			continue
		}
		haystack := strings.Join([]string{record.Subject, record.From, record.Body}, "\n")
		if keywordText != "" && !strings.Contains(strings.ToLower(haystack), strings.ToLower(keywordText)) {
			continue
		}
		if !reLinkScanWords.MatchString(haystack) {
			continue
		}
		for _, link := range extractLinksFromText(haystack) {
			if keywordText == "" || strings.Contains(strings.ToLower(link), strings.ToLower(keywordText)) || strings.Contains(strings.ToLower(haystack), strings.ToLower(keywordText)) {
				r.log.emit(fmt.Sprintf("收到 K12 邀请链接: %s", truncate(link, 120)))
				return link
			}
		}
	}
	return ""
}

var (
	_ Reader = (*CloudMailReader)(nil)
	_ Reader = (*HotmailOtpReader)(nil)
)
