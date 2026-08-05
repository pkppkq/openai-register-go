package browser

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/models"
	"github.com/pkppkq/openai-register-go/internal/openai"
)

// Page wraps a rod.Page created by Browser.NewPage (fingerprint already applied).
type Page struct {
	Rod *rod.Page
	fp  models.DeviceFingerprint
}

// URL returns the page's current URL ("" if it can't be read).
func (p *Page) URL() string {
	info, err := p.Rod.Info()
	if err != nil || info == nil {
		return ""
	}
	return info.URL
}

// IsClosed reports whether the page/target is gone (go-rod signals this via
// errors rather than a bool — mirrors the Python page.is_closed() guards).
func (p *Page) IsClosed() bool {
	_, err := p.Rod.Info()
	return err != nil
}

// Close closes the page, ignoring errors (mirrors _close_browser semantics).
func (p *Page) Close() { _ = p.Rod.Close() }

// Navigate goes to url and waits for DOMContentLoaded (readyState
// interactive/complete) — Playwright used wait_until="domcontentloaded", which
// go-rod's full-load WaitLoad would overshoot on challenge/SPA pages.
func (p *Page) Navigate(url string, timeout time.Duration) error {
	if err := p.Rod.Timeout(timeout).Navigate(url); err != nil {
		return fmt.Errorf("navigate %s: %w", url, err)
	}
	return p.WaitDOMContentLoaded(timeout)
}

// WaitDOMContentLoaded polls document.readyState until interactive/complete.
func (p *Page) WaitDOMContentLoaded(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v, err := p.Rod.Eval(`() => document.readyState`)
		if err == nil && v != nil {
			s := v.Value.String()
			if s == "interactive" || s == "complete" {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for DOMContentLoaded")
}

// reactFillJS is _force_fill_locator's native-setter recipe (app.py:11891). A
// regular function so callFunctionOn binds `this` to the element (arrow would
// capture lexical this).
const reactFillJS = `function(value){
    const el = this;
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const desc = Object.getOwnPropertyDescriptor(proto, 'value');
    if (desc && desc.set) desc.set.call(el, value); else el.value = value;
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    el.dispatchEvent(new Event('blur', { bubbles: true }));
}`

// ForceFill mirrors _force_fill_locator: click, type, then run the React-safe
// native value-setter + input/change/blur dispatch so controlled inputs commit.
// Returns false on any failure (never errors, matching the Python try/except).
func (p *Page) ForceFill(el *rod.Element, value string) bool {
	if el == nil {
		return false
	}
	if err := el.Timeout(3*time.Second).Click(proto.InputMouseButtonLeft, 1); err != nil {
		return false
	}
	if err := el.Timeout(5 * time.Second).Input(value); err != nil {
		return false
	}
	if _, err := el.Eval(reactFillJS, value); err != nil {
		return false
	}
	return true
}

// clickSubmitByDOMJS is _click_submit_button_by_dom (app.py:10159) verbatim.
const clickSubmitByDOMJS = `() => {
    const visible = (el) => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const buttons = Array.from(document.querySelectorAll('button, [role="button"]')).filter(visible);
    const button = buttons.find(el =>
        (el.textContent || '').includes('Finish creating account')
        || (el.textContent || '').includes('アカウントの作成を完了する')
        || (el.textContent || '').includes('作成を完了')
        || (el.textContent || '').includes('Continue')
        || (el.textContent || '').includes('继续')
        || (el.textContent || '').includes('完成帐户创建')
        || (el.textContent || '').includes('完成账户创建')
        || (el.textContent || '').includes('続行')
        || (el.getAttribute('data-dd-action-name') || '') === 'Continue'
        || (el.type || '').toLowerCase() === 'submit'
    );
    if (!button || button.getAttribute('aria-disabled') === 'true' || button.disabled) return false;
    button.scrollIntoView({ block: 'center', inline: 'center' });
    button.focus();
    const form = button.closest('form');
    if (form && typeof form.requestSubmit === 'function') {
        form.requestSubmit(button);
        return true;
    }
    button.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerType: 'mouse', isPrimary: true }));
    button.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }));
    button.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerType: 'mouse', isPrimary: true }));
    button.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }));
    button.click();
    return true;
}`

// ClickSubmitButtonByDOM mirrors _click_submit_button_by_dom: find the localized
// submit/continue button and activate it via requestSubmit or a synthetic
// pointer/mouse event sequence (anti-detection; not a plain .Click()).
func (p *Page) ClickSubmitButtonByDOM() bool {
	v, err := p.Rod.Eval(clickSubmitByDOMJS)
	if err != nil || v == nil {
		return false
	}
	return v.Value.Bool()
}

// sessionProbeJS mirrors the _has_chatgpt_session fetch (app.py:10075).
const sessionProbeJS = `async () => {
    const resp = await fetch('/api/auth/session', { credentials: 'include' });
    if (!resp.ok) return null;
    return await resp.json();
}`

// HasChatGPTSession mirrors _has_chatgpt_session: across ALL browser pages whose
// URL is on chatgpt.com, fetch /api/auth/session and report whether any returns a
// truthy accessToken. Tolerates per-page errors.
func (b *Browser) HasChatGPTSession() bool {
	pages, err := b.Rod.Pages()
	if err != nil {
		return false
	}
	for _, pg := range pages {
		info, err := pg.Info()
		if err != nil || info == nil || !strings.HasPrefix(info.URL, openai.ChatGPTBaseURL) {
			continue
		}
		v, err := pg.Eval(sessionProbeJS)
		if err != nil || v == nil {
			continue
		}
		if token := v.Value.Get("accessToken").Str(); token != "" {
			return true
		}
	}
	return false
}

// ContextHasChatGPTPage mirrors _context_has_chatgpt_page: any open page is on
// chatgpt.com.
func (b *Browser) ContextHasChatGPTPage() bool {
	pages, err := b.Rod.Pages()
	if err != nil {
		return false
	}
	for _, pg := range pages {
		if info, err := pg.Info(); err == nil && info != nil && strings.HasPrefix(info.URL, openai.ChatGPTBaseURL) {
			return true
		}
	}
	return false
}

// VisibleInputs mirrors _visible_inputs: for each selector, up to 12 matching
// elements that are currently visible.
func (p *Page) VisibleInputs(selectors []string) []*rod.Element {
	var visible []*rod.Element
	for _, sel := range selectors {
		els, err := p.Rod.Elements(sel)
		if err != nil {
			continue
		}
		for i, el := range els {
			if i >= 12 {
				break
			}
			if ok, _ := el.Visible(); ok {
				visible = append(visible, el)
			}
		}
	}
	return visible
}
