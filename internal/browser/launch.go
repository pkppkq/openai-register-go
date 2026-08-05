package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/models"
)

// Background window geometry (app.py BROWSER_BACKGROUND_*).
const (
	bgWidth  = 820
	bgHeight = 620
	bgX      = 32
	bgY      = 32
)

// chromeBinCandidates 按优先级返回当前机器可推导的 Chrome/Chromium 路径，
// 不在源码中记录任何开发机用户名或 Playwright 版本号。
func chromeBinCandidates() []string {
	candidates := make([]string, 0, 8)
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		playwright, _ := filepath.Glob(filepath.Join(
			localAppData, "ms-playwright", "chromium-*", "chrome-win64", "chrome.exe",
		))
		sort.Sort(sort.Reverse(sort.StringSlice(playwright)))
		candidates = append(candidates, playwright...)
		candidates = append(candidates,
			filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	return append(candidates,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	)
}

// ResolveChromeBin returns the first existing Chrome/Chromium binary (env
// CHROME_BIN wins), or "" to let go-rod fall back to its own lookup/download.
func ResolveChromeBin() string {
	if v := strings.TrimSpace(os.Getenv("CHROME_BIN")); v != "" {
		return v
	}
	for _, p := range chromeBinCandidates() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// LaunchOptions configures a worker browser. Fingerprint is required (drives both
// launch args and per-page emulation).
type LaunchOptions struct {
	Fingerprint  models.DeviceFingerprint
	Headless     bool
	ProxyServer  string // ProxyConfig.chain_url; "" for direct
	ChromeBin    string // "" -> ResolveChromeBin()
	ExtensionDir string // "" -> no extension (register path); set -> load MV3 unpacked
	UserDataDir  string // "" -> ephemeral; set -> persistent context
	// ExtraArgs 仅供需要额外隔离开关的专用浏览器使用，例如支付窗口禁用
	// Chromium 自带的密码和信用卡自动填充。每项格式为
	// []string{"flag", "value1", ...}；无值开关只传 flag。
	ExtraArgs [][]string
}

// Browser wraps a launched rod.Browser plus the launcher (for Cleanup) and the
// fingerprint every page is emulated with.
type Browser struct {
	Rod      *rod.Browser
	launcher *launcher.Launcher
	fp       models.DeviceFingerprint
}

// backgroundArgs mirrors browser_background_args + the worker's extra
// site-isolation-disable flag. Each entry is (flag, values...).
func backgroundArgs(locale string) [][]string {
	args := [][]string{
		{"disable-blink-features", "AutomationControlled"},
		{"disable-background-networking"},
		{"disable-component-update"},
		{"disable-default-apps"},
		{"disable-sync"},
		{"metrics-recording-only"},
		{"no-first-run"},
		{"deny-permission-prompts"},
		{"window-size", fmt.Sprintf("%d,%d", bgWidth, bgHeight)},
		{"window-position", fmt.Sprintf("%d,%d", bgX, bgY)},
		{"disable-features", "IsolateOrigins,site-per-process"},
	}
	if locale != "" {
		args = append(args, []string{"lang", locale})
	}
	return args
}

// Launch starts Chromium with the fingerprint-derived args (+ optional proxy and
// unpacked extension) and connects rod.
func Launch(opts LaunchOptions) (*Browser, error) {
	bin := opts.ChromeBin
	if bin == "" {
		bin = ResolveChromeBin()
	}

	l := launcher.New().
		Leakless(false). // Windows Defender false-flags rod's leakless.exe helper
		Headless(opts.Headless)
	if bin != "" {
		l = l.Bin(bin)
	}
	for _, a := range backgroundArgs(opts.Fingerprint.Locale) {
		l = l.Set(flags.Flag(a[0]), a[1:]...)
	}
	for _, a := range opts.ExtraArgs {
		if len(a) == 0 || strings.TrimSpace(a[0]) == "" {
			continue
		}
		l = l.Set(flags.Flag(a[0]), a[1:]...)
	}
	l = l.Set("no-default-browser-check")
	if opts.ProxyServer != "" {
		l = l.Set("proxy-server", opts.ProxyServer)
	}
	if opts.ExtensionDir != "" {
		l = l.Set("disable-extensions-except", opts.ExtensionDir).
			Set("load-extension", opts.ExtensionDir)
	}
	if opts.UserDataDir != "" {
		l = l.UserDataDir(opts.UserDataDir)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch chromium: %w", err)
	}
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		l.Cleanup()
		return nil, fmt.Errorf("connect rod: %w", err)
	}
	return &Browser{Rod: b, launcher: l, fp: opts.Fingerprint}, nil
}

// Fingerprint returns the browser's device fingerprint.
func (b *Browser) Fingerprint() models.DeviceFingerprint { return b.fp }

// ClearCookies mirrors Playwright's context.clear_cookies(), which every worker
// entry point calls right after creating the context. The profile dir is fresh
// per launch so this is normally a no-op, but the extension we preload can seed
// cookies before the flow starts.
func (b *Browser) ClearCookies() error {
	if b == nil || b.Rod == nil {
		return nil
	}
	return proto.NetworkClearBrowserCookies{}.Call(b.Rod)
}

// Close closes the rod browser and cleans up the launcher profile/temp dirs.
func (b *Browser) Close() {
	if b.Rod != nil {
		_ = b.Rod.Close()
	}
	if b.launcher != nil {
		b.launcher.Cleanup()
	}
}

// NewPage opens a blank page with the full fingerprint emulation applied BEFORE
// any navigation: UA/platform, timezone, locale, device metrics, client-hint
// headers, and the init-script spoof (re-registered per target). Navigate after.
func (b *Browser) NewPage() (*Page, error) {
	page, err := b.Rod.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}
	prepared, err := b.PreparePage(page)
	if err != nil {
		_ = page.Close()
		return nil, err
	}
	return prepared, nil
}

// PreparePage 把同一浏览器中新弹出的标签页纳入与主页面一致的指纹环境。
// 支付流程会由 PayPal/OpenAI 打开新页；CDP 的 UA、时区和设备参数是按
// target 生效的，不能假设新页会自动继承。
func (b *Browser) PreparePage(page *rod.Page) (*Page, error) {
	if b == nil || page == nil {
		return nil, fmt.Errorf("prepare page: page is nil")
	}
	if err := applyEmulation(page, b.fp); err != nil {
		return nil, err
	}
	return &Page{Rod: page, fp: b.fp}, nil
}

// applyEmulation installs the per-page fingerprint (order matches the Playwright
// new_context options + add_init_script).
func applyEmulation(page *rod.Page, fp models.DeviceFingerprint) error {
	if err := (proto.EmulationSetTimezoneOverride{TimezoneID: fp.Timezone}).Call(page); err != nil {
		return fmt.Errorf("set timezone: %w", err)
	}
	if fp.Locale != "" {
		if err := (proto.EmulationSetLocaleOverride{Locale: fp.Locale}).Call(page); err != nil {
			return fmt.Errorf("set locale: %w", err)
		}
	}
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent:         fp.UserAgent,
		AcceptLanguage:    fp.AcceptLanguage(),
		Platform:          fp.Platform,
		UserAgentMetadata: userAgentMetadata(fp),
	}); err != nil {
		return fmt.Errorf("set user agent: %w", err)
	}
	sw, sh := fp.ScreenWidth, fp.ScreenHeight
	if err := (proto.EmulationSetDeviceMetricsOverride{
		Width:             fp.ViewportWidth,
		Height:            fp.ViewportHeight,
		DeviceScaleFactor: fp.DeviceScaleFactor,
		Mobile:            false,
		ScreenWidth:       &sw,
		ScreenHeight:      &sh,
	}).Call(page); err != nil {
		return fmt.Errorf("set device metrics: %w", err)
	}
	if _, err := page.SetExtraHeaders(flattenHeaders(FingerprintHeaders(fp))); err != nil {
		return fmt.Errorf("set extra headers: %w", err)
	}
	if _, err := page.EvalOnNewDocument(FingerprintInitScript(fp)); err != nil {
		return fmt.Errorf("install fingerprint init script: %w", err)
	}
	return nil
}

// userAgentMetadata builds the UA-Client-Hints metadata that must accompany a
// UA override. CDP's setUserAgentOverride BLANKS all UA-CH when this is omitted,
// leaving navigator.userAgentData.platform empty — itself a strong bot signal on
// a Windows Chrome. Values are kept identical to FingerprintHeaders and to the
// init script's getHighEntropyValues table. Note the UA-CH platform is "Windows"
// (navigator.platform stays "Win32").
func userAgentMetadata(fp models.DeviceFingerprint) *proto.EmulationUserAgentMetadata {
	major := fp.ChromeMajor()
	full := fp.ChromeFull()
	return &proto.EmulationUserAgentMetadata{
		Brands: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Google Chrome", Version: major},
			{Brand: "Chromium", Version: major},
			{Brand: "Not.A/Brand", Version: "24"},
		},
		FullVersionList: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Google Chrome", Version: full},
			{Brand: "Chromium", Version: full},
			{Brand: "Not.A/Brand", Version: "24.0.0.0"},
		},
		// FullVersion is deprecated but still backs getHighEntropyValues
		// (['uaFullVersion']). Leaving it empty makes Chrome report its REAL
		// build there while brands report the spoofed major — a self-inconsistent
		// fingerprint that is trivially detectable.
		FullVersion:     full,
		Platform:        "Windows",
		PlatformVersion: "15.0.0",
		Architecture:    "x86",
		Model:           "",
		Mobile:          false,
		Bitness:         "64",
		Wow64:           false,
	}
}

// flattenHeaders turns a header map into rod's [k1,v1,k2,v2,...] form.
func flattenHeaders(h map[string]string) []string {
	out := make([]string, 0, len(h)*2)
	for k, v := range h {
		out = append(out, k, v)
	}
	return out
}

// ExtensionManifestExists reports whether dir looks like an unpacked extension.
func ExtensionManifestExists(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "manifest.json"))
	return err == nil
}
