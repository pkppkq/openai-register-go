package browser

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// StorageState is the go-rod substitute for Playwright's storage_state():
// go-rod can carry cookies across contexts but has NO built-in way to move
// localStorage/sessionStorage, and the OpenAI session lives in BOTH. Copying
// only cookies silently loses the login (relink's proxy-switch hand-off).
type StorageState struct {
	Cookies []*proto.NetworkCookieParam `json:"cookies"`
	Origins []OriginStorage             `json:"origins"`
}

// OriginStorage holds one origin's localStorage (and sessionStorage) entries.
//
// SessionStorage is captured for completeness but is NOT replayed by
// ApplyStorageState: sessionStorage is scoped to a browsing context (tab), so it
// cannot cross browsers or even tabs — the seed tab's copy dies with the tab.
// Playwright's storage_state omits it for the same reason. Use
// Page.SeedSessionStorage to inject it into a specific live tab.
type OriginStorage struct {
	Origin         string            `json:"origin"`
	LocalStorage   map[string]string `json:"localStorage,omitempty"`
	SessionStorage map[string]string `json:"sessionStorage,omitempty"`
}

const dumpStorageJS = `() => {
    const dump = (store) => {
        const out = {};
        try {
            for (let i = 0; i < store.length; i++) {
                const k = store.key(i);
                if (k !== null) out[k] = store.getItem(k);
            }
        } catch (e) {}
        return out;
    };
    return { origin: location.origin, local: dump(window.localStorage), session: dump(window.sessionStorage) };
}`

// ExportStorageState captures all browser cookies plus the localStorage /
// sessionStorage of every currently-open page's origin.
func (b *Browser) ExportStorageState() (*StorageState, error) {
	cookies, err := b.Rod.GetCookies()
	if err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}
	state := &StorageState{Cookies: cookiesToParams(cookies)}

	pages, err := b.Rod.Pages()
	if err != nil {
		return state, nil // cookies alone are still useful
	}
	seen := map[string]bool{}
	for _, pg := range pages {
		info, err := pg.Info()
		if err != nil || info == nil || !strings.HasPrefix(info.URL, "http") {
			continue
		}
		v, err := pg.Eval(dumpStorageJS)
		if err != nil || v == nil {
			continue
		}
		origin := v.Value.Get("origin").Str()
		if origin == "" || seen[origin] {
			continue
		}
		seen[origin] = true
		entry := OriginStorage{Origin: origin, LocalStorage: map[string]string{}, SessionStorage: map[string]string{}}
		for k, val := range v.Value.Get("local").Map() {
			entry.LocalStorage[k] = val.Str()
		}
		for k, val := range v.Value.Get("session").Map() {
			entry.SessionStorage[k] = val.Str()
		}
		state.Origins = append(state.Origins, entry)
	}
	return state, nil
}

// cookiesToParams converts read cookies into settable params, preserving
// domain/path/expiry/secure/httpOnly/sameSite (all load-bearing for session reuse).
func cookiesToParams(cookies []*proto.NetworkCookie) []*proto.NetworkCookieParam {
	out := make([]*proto.NetworkCookieParam, 0, len(cookies))
	for _, c := range cookies {
		if c == nil {
			continue
		}
		p := &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
		}
		// Only carry a real expiry; session cookies (<=0) must stay session cookies
		// (leaving Expires zero-valued keeps them session-scoped).
		if c.Expires > 0 {
			p.Expires = c.Expires
		}
		if c.PartitionKey != nil {
			// 分区 Cookie 的键决定它在哪个顶层站点可见，刷新后必须保留。
			partitionKey := *c.PartitionKey
			p.PartitionKey = &partitionKey
		}
		out = append(out, p)
	}
	return out
}

// ApplyStorageState replays a captured state into this browser: cookies first,
// then per-origin localStorage/sessionStorage. Seeding web storage requires being
// ON that origin, so each origin is visited on a scratch page before writing.
func (b *Browser) ApplyStorageState(state *StorageState) error {
	if state == nil {
		return nil
	}
	if len(state.Cookies) > 0 {
		if err := b.Rod.SetCookies(state.Cookies); err != nil {
			return fmt.Errorf("set cookies: %w", err)
		}
	}
	for _, origin := range state.Origins {
		// Only localStorage is transferable; see OriginStorage docs.
		if len(origin.LocalStorage) == 0 {
			continue
		}
		if _, err := url.Parse(origin.Origin); err != nil {
			continue
		}
		if err := b.seedOrigin(origin); err != nil {
			return err
		}
	}
	return nil
}

const seedStorageJS = `(local) => {
    try { for (const k in local) window.localStorage.setItem(k, local[k]); } catch (e) {}
    return true;
}`

// seedSessionStorageJS writes sessionStorage into the CURRENT tab.
const seedSessionStorageJS = `(session) => {
    try { for (const k in session) window.sessionStorage.setItem(k, session[k]); } catch (e) {}
    return true;
}`

// SeedSessionStorage writes sessionStorage entries into this page's tab. Unlike
// localStorage, sessionStorage cannot be transferred via ApplyStorageState — it
// must be injected into the exact tab that will use it, while that tab is on the
// matching origin.
func (p *Page) SeedSessionStorage(entries map[string]string) error {
	if len(entries) == 0 {
		return nil
	}
	if _, err := p.Rod.Eval(seedSessionStorageJS, entries); err != nil {
		return fmt.Errorf("seed sessionStorage: %w", err)
	}
	return nil
}

// seedOrigin writes an origin's web-storage entries. Web storage is
// origin-partitioned, so the page must actually BE on that origin first — but we
// must not really load it: a redirect (chatgpt.com -> auth.openai.com) would land
// us on a different origin and silently seed the wrong partition, and a 204/304
// would not navigate at all. So the request is hijacked and served a local stub,
// which puts the document on the exact origin with zero network.
func (b *Browser) seedOrigin(origin OriginStorage) error {
	page, err := b.Rod.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return fmt.Errorf("open seed page: %w", err)
	}
	defer func() { _ = page.Close() }()

	if err := applyEmulation(page, b.fp); err != nil {
		return err
	}

	router := page.HijackRequests()
	if err := router.Add(origin.Origin+"/*", "", func(ctx *rod.Hijack) {
		ctx.Response.SetHeader("Content-Type", "text/html; charset=utf-8")
		ctx.Response.SetBody("<!doctype html><html><head><title>seed</title></head><body></body></html>")
	}); err != nil {
		return fmt.Errorf("hijack %s: %w", origin.Origin, err)
	}
	go router.Run()
	defer func() { _ = router.Stop() }()

	if err := page.Navigate(origin.Origin + "/"); err != nil {
		return fmt.Errorf("navigate seed origin %s: %w", origin.Origin, err)
	}
	if err := page.WaitLoad(); err != nil {
		return fmt.Errorf("load seed origin %s: %w", origin.Origin, err)
	}
	if _, err := page.Eval(seedStorageJS, origin.LocalStorage); err != nil {
		return fmt.Errorf("seed storage for %s: %w", origin.Origin, err)
	}
	return nil
}
