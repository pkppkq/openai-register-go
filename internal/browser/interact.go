package browser

import (
	"errors"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/openai"
)

// clickButtonByTextJS mirrors _click_button_by_text (app.py:11166): locate the
// first visible button/[role=button] whose text contains any of `texts`, scroll
// it into view, and return its viewport-center coordinates for a real mouse click.
const clickButtonByTextJS = `(texts) => {
    const visible = (el) => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    // Playwright's :has-text() lowercases AND whitespace-normalizes both sides,
    // and reads rendered text (innerText skips script/style and hidden nodes).
    // A raw textContent.includes() is strictly stricter and silently misses
    // labels uppercased by CSS or wrapped across lines.
    const norm = (s) => (s || '').replace(/\s+/g, ' ').trim().toLowerCase();
    const rendered = (el) => norm(el.innerText || el.textContent);
    const candidates = Array.from(document.querySelectorAll('button, [role="button"]'))
        .filter(visible)
        .filter(el => texts.some(text => rendered(el).includes(norm(text))));
    const el = candidates[0];
    if (!el) return null;
    el.scrollIntoView({ block: 'center', inline: 'center' });
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2, text: el.textContent || '' };
}`

// ClickButtonByText mirrors _click_button_by_text: a REAL mouse click at the
// button's center (not el.Click()) — the coordinate path is anti-detection and
// also dodges overlays. Returns (clicked, buttonText).
func (p *Page) ClickButtonByText(texts []string) (bool, string) {
	v, err := p.Rod.Eval(clickButtonByTextJS, texts)
	if err != nil || v == nil || v.Value.Nil() {
		return false, ""
	}
	x := v.Value.Get("x").Num()
	y := v.Value.Get("y").Num()
	label := strings.TrimSpace(v.Value.Get("text").Str())
	if err := p.Rod.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
		return false, ""
	}
	if err := p.Rod.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return false, ""
	}
	return true, label
}

// ForceClick dispatches the click in JS, bypassing go-rod's
// actionability/visibility/hit-testing. Use this ONLY inside cross-origin
// Turnstile frames, where a page-level mouse click cannot be aimed: the
// element's coordinates are frame-relative and the frame's own offset is not
// readable from here.
//
// This is NOT equivalent to Playwright's click(force=True), which still
// delivers a real trusted mouse click — see ForcePointerClick. Note that
// HTMLElement.click() essentially cannot fail, so a caller's fallback ladder
// below a ForceClick rung is unreachable.
func ForceClick(el *rod.Element) bool {
	if el == nil {
		return false
	}
	_, err := el.Eval(`function(){ this.click(); return true; }`)
	return err == nil
}

// ForcePointerClick emulates Playwright's click(force=True) on a MAIN-FRAME
// element: skip the actionability wait, but still scroll the element into view
// and deliver a real, trusted mouse click at its centre.
//
// Two reasons this matters over ForceClick: handlers bound to
// pointerdown/mousedown (or gated on isTrusted) never see a JS .click(), and a
// JS .click() always "succeeds" — so a genuinely un-clickable control reports
// success and the caller never reaches its fallback rungs.
func (p *Page) ForcePointerClick(el *rod.Element) error {
	if el == nil {
		return errors.New("element is nil")
	}
	if err := el.ScrollIntoView(); err != nil {
		return err
	}
	shape, err := el.Shape()
	if err != nil {
		return err
	}
	box := shape.Box()
	if box == nil || box.Width <= 0 || box.Height <= 0 {
		return errors.New("element has no visible box")
	}
	if err := p.Rod.Mouse.MoveTo(proto.NewPoint(box.X+box.Width/2, box.Y+box.Height/2)); err != nil {
		return err
	}
	return p.Rod.Mouse.Click(proto.InputMouseButtonLeft, 1)
}

// turnstilePageSelectors are tried on the top-level document (app.py:10751).
var turnstilePageSelectors = []string{
	"input[type='checkbox']",
	".ctp-checkbox-label",
	"label.ctp-checkbox-label",
	"#challenge-stage",
	"#challenge-form",
	".cf-turnstile",
	"[name='cf-turnstile-response']",
	"iframe[src*='challenges.cloudflare.com']",
	"iframe[src*='turnstile']",
}

// turnstileFrameSelectors are tried inside a challenge iframe (app.py:10790).
var turnstileFrameSelectors = []string{
	"input[type='checkbox']",
	".ctp-checkbox-label",
	"label.ctp-checkbox-label",
	"#challenge-stage",
	"#challenge-form",
	"body",
}

// isChallengeFrameURL mirrors the frame filter in _click_turnstile_checkbox:
// only challenge-ish frames (or same-document/blank ones) are considered.
func isChallengeFrameURL(url string) bool {
	if url == "" || url == "about:blank" {
		return true
	}
	return strings.Contains(url, "challenges.cloudflare.com") ||
		strings.Contains(url, "turnstile") ||
		strings.Contains(url, "cf-chl")
}

// ClickTurnstileCheckbox mirrors _click_turnstile_checkbox: best-effort force
// clicks on the top-level challenge widgets, then INSIDE each challenge iframe.
// go-rod has no flat frame list (Playwright's page.frames), so challenge frames
// are reached via the <iframe> element's Frame() page. Returns whether anything
// was clicked, plus a short description for logging.
func (p *Page) ClickTurnstileCheckbox() (bool, string) {
	clicked := false
	detail := ""

	for _, sel := range turnstilePageSelectors {
		el := findNow(p.Rod, sel)
		if el == nil {
			continue
		}
		if ForceClick(el) {
			clicked = true
			detail = "挑战控件: " + sel
			break
		}
	}

	if ok, d := clickInChallengeFrames(p.Rod, 0); ok {
		return true, d
	}
	return clicked, detail
}

// findNow looks an element up WITHOUT go-rod's retry loop. Page.Element polls
// until the element appears or the context deadline expires, so a 1200ms budget
// costs the full 1200ms on every MISS — nine absent selectors would burn ~11s
// per sweep and starve the caller's 45s auto-solve loop of re-checks. Playwright's
// locator.count() returned instantly, which is the cadence being reproduced.
func findNow(p *rod.Page, sel string) *rod.Element {
	el, err := p.Sleeper(rod.NotFoundSleeper).Element(sel)
	if err != nil {
		return nil
	}
	return el
}

// maxChallengeFrameDepth bounds the recursive frame walk. Turnstile nests its
// real widget one level inside the outer challenge iframe.
const maxChallengeFrameDepth = 3

// clickInChallengeFrames reproduces Playwright's page.frames, which is a FLAT,
// RECURSIVE list filtered on each frame's LIVE url. Walking only top-level
// <iframe> elements and filtering on the src ATTRIBUTE misses both the nested
// inner Turnstile frame and any frame navigated by JS after insertion (no src).
func clickInChallengeFrames(p *rod.Page, depth int) (bool, string) {
	if p == nil || depth >= maxChallengeFrameDepth {
		return false, ""
	}
	iframes, err := p.Elements("iframe")
	if err != nil {
		return false, ""
	}
	for _, ifr := range iframes {
		fp, err := ifr.Frame()
		if err != nil || fp == nil {
			continue
		}
		// Prefer the frame's live URL; fall back to the src attribute when the
		// frame has not committed a navigation yet.
		url := ""
		if info, err := fp.Info(); err == nil && info != nil {
			url = info.URL
		}
		if url == "" {
			if src, _ := ifr.Attribute("src"); src != nil {
				url = *src
			}
		}
		if !isChallengeFrameURL(url) {
			continue
		}
		for _, sel := range turnstileFrameSelectors {
			el := findNow(fp, sel)
			if el == nil {
				continue
			}
			if ForceClick(el) {
				return true, "iframe 点击: " + sel + " (" + truncate(url, 80) + ")"
			}
		}
		if ok, d := clickInChallengeFrames(fp, depth+1); ok {
			return true, d
		}
	}
	return false, ""
}

// HasCloudflareClearance mirrors _has_cloudflare_clearance: a non-empty
// cf_clearance cookie that is not past its expiry. IMPORTANT: only expires>0 AND
// expires<now counts as expired — session cookies (rod reports Expires -1 or 0)
// are VALID. Inverting this rejects a good clearance.
func (b *Browser) HasCloudflareClearance() bool {
	cookies, err := b.Rod.GetCookies()
	if err != nil {
		return false
	}
	now := float64(time.Now().Unix())
	for _, c := range cookies {
		if c == nil || c.Name != "cf_clearance" || strings.TrimSpace(c.Value) == "" {
			continue
		}
		exp := float64(c.Expires)
		if exp > 0 && exp < now {
			continue
		}
		return true
	}
	return false
}

// IsCloudflareChallengePage mirrors _is_cloudflare_challenge_page: try the
// injected detector JS first, then title+url, then body text. Stage order
// matters — on a live challenge the top-level body is often near-empty because
// the content lives in the iframe, so the JS eval carries the detection.
func (p *Page) IsCloudflareChallengePage() bool {
	if v, err := p.Rod.Eval(openai.CFInterstitialDetectJS); err == nil && v != nil && v.Value.Bool() {
		return true
	}
	title := ""
	if v, err := p.Rod.Eval(`() => document.title || ''`); err == nil && v != nil {
		title = v.Value.Str()
	}
	if openai.IsCloudflareChallengeText(title + "\n" + p.URL()) {
		return true
	}
	if v, err := p.Rod.Timeout(1200 * time.Millisecond).Eval(`() => (document.body && document.body.innerText || '').slice(0, 2000)`); err == nil && v != nil {
		if openai.IsCloudflareChallengeText(v.Value.Str()) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
