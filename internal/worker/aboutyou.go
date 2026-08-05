// aboutyou.go — port of the "About you" profile-form cluster of
// OpenAIRegisterPayLinkWorker (app.py:10991-11902).
//
// Not ported (verified dead code in Python):
//   - _fill_about_you_inputs_by_dom (app.py:11683-11748): no caller.
//   - _about_you_birth_date_from_values (app.py:11750-11763): no caller, and it
//     calls self._parse_about_you_birth_date which does not exist anywhere in
//     app.py — invoking it would raise AttributeError.

package worker

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"

	"github.com/pkppkq/openai-register-go/internal/browser"
	"github.com/pkppkq/openai-register-go/internal/models"
)

// aboutYouEvalTimeout bounds evals that Python left unbounded (Playwright's
// page.evaluate has a 30s default; go-rod would block forever without a
// deadline). Probes that Python bounded explicitly keep the Python value.
const aboutYouEvalTimeout = 10 * time.Second

// AboutYouFiller ports the "About you" (基础资料) profile-form cluster:
// app.py:10991-11902 — detection, field classification, the 4-tier fill
// fallback and submission of the post-registration profile form.
//
// The cluster's Python original also calls four predicates that live in OTHER
// worker clusters (_has_chatgpt_session app.py:10062, _click_continue
// app.py:10117, _has_register_phone_number_form app.py:10278,
// _has_visible_password app.py:10543). They are injected as nil-safe hooks so
// this file stays self-contained; a nil hook behaves as "false" / "not
// clicked", which only makes _about_you_submit_done fall back to its URL and
// form checks.
type AboutYouFiller struct {
	page *browser.Page
	log  func(string)

	// HasChatGPTSession mirrors _has_chatgpt_session (app.py:10062).
	HasChatGPTSession func() bool
	// HasRegisterPhoneNumberForm mirrors _has_register_phone_number_form (app.py:10278).
	HasRegisterPhoneNumberForm func() bool
	// HasVisiblePassword mirrors _has_visible_password (app.py:10543).
	HasVisiblePassword func() bool
	// ClickContinue mirrors _click_continue (app.py:10117).
	ClickContinue func() bool
}

// NewAboutYouFiller builds the filler for one page. log may be nil.
func NewAboutYouFiller(page *browser.Page, log func(string)) *AboutYouFiller {
	return &AboutYouFiller{page: page, log: log}
}

// AboutYouFieldMeta mirrors one entry of _about_you_field_meta's JS payload
// (app.py:11273-11322).
type AboutYouFieldMeta struct {
	Index        int    `json:"index"`
	Tag          string `json:"tag"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	ID           string `json:"id"`
	Placeholder  string `json:"placeholder"`
	Autocomplete string `json:"autocomplete"`
	Inputmode    string `json:"inputmode"`
	AriaLabel    string `json:"ariaLabel"`
	TestID       string `json:"testId"`
	Label        string `json:"label"`
	Value        string `json:"value"`
}

// AboutYouFill mirrors one (index, kind, value) tuple of _about_you_plan_fills
// (app.py:11509).
type AboutYouFill struct {
	Index int
	Kind  string
	Value string
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func (f *AboutYouFiller) logf(format string, args ...interface{}) {
	if f == nil || f.log == nil {
		return
	}
	if len(args) == 0 {
		f.log(format)
		return
	}
	f.log(fmt.Sprintf(format, args...))
}

func (f *AboutYouFiller) rodPage() *rod.Page {
	if f == nil || f.page == nil {
		return nil
	}
	return f.page.Rod
}

func (f *AboutYouFiller) eval(timeout time.Duration, js string, args ...interface{}) (*proto.RuntimeRemoteObject, error) {
	p := f.rodPage()
	if p == nil {
		return nil, fmt.Errorf("page unavailable")
	}
	return p.Timeout(timeout).Eval(js, args...)
}

// aboutYouTruncate slices by RUNE like Python's str[:n].
func aboutYouTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// aboutYouPyList reproduces Python's f-string rendering of a list[str]
// (["a", "b"] -> ['a', 'b']) so the log lines stay byte-comparable with the
// Python build.
func aboutYouPyList(values []string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `'`, `\'`)
		parts = append(parts, "'"+v+"'")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func aboutYouOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (f *AboutYouFiller) hasChatGPTSession() bool {
	if f.HasChatGPTSession == nil {
		return false
	}
	return f.HasChatGPTSession()
}

func (f *AboutYouFiller) hasRegisterPhoneNumberForm() bool {
	if f.HasRegisterPhoneNumberForm == nil {
		return false
	}
	return f.HasRegisterPhoneNumberForm()
}

func (f *AboutYouFiller) hasVisiblePassword() bool {
	if f.HasVisiblePassword == nil {
		return false
	}
	return f.HasVisiblePassword()
}

func (f *AboutYouFiller) clickContinue() bool {
	if f.ClickContinue == nil {
		return false
	}
	return f.ClickContinue()
}

// aboutYouBodyTextJS mirrors page.locator("body").inner_text().
const aboutYouBodyTextJS = `() => {
    if (!document.body) throw new Error('no body');
    return document.body.innerText || '';
}`

func (f *AboutYouFiller) bodyInnerText(timeout time.Duration) (string, bool) {
	v, err := f.eval(timeout, aboutYouBodyTextJS)
	if err != nil || v == nil {
		return "", false
	}
	return v.Value.Str(), true
}

// ---------------------------------------------------------------------------
// detection / entry point
// ---------------------------------------------------------------------------

// aboutYouFormMarkers is the multilingual body-text ladder of
// _has_about_you_form (app.py:10994-11012), verbatim and in order.
var aboutYouFormMarkers = []string{
	"tell us about you",
	"about you",
	"birth",
	"how old are you",
	"full name",
	"finish creating account",
	"confirmemos tu edad",
	"fecha de nacimiento",
	"nombre y apellidos",
	"finalizar la creación de la cuenta",
	"finalizar la creacion de la cuenta",
	"生まれた年",
	"生年",
	"年齢",
	"アカウントの作成を完了する",
	"出生年",
	"年龄",
}

// HasAboutYouForm mirrors _has_about_you_form (app.py:10991-11020): localized
// body text hints AND at least two visible inputs.
func (f *AboutYouFiller) HasAboutYouForm() bool {
	text, ok := f.bodyInnerText(1 * time.Second)
	if !ok {
		return false
	}
	lower := strings.ToLower(text)
	hasAboutText := false
	for _, marker := range aboutYouFormMarkers {
		if strings.Contains(lower, marker) {
			hasAboutText = true
			break
		}
	}
	if !hasAboutText {
		return false
	}
	// Python wrapped this in try/except -> True; browser.VisibleInputs cannot
	// raise (it swallows per-selector errors), so that branch is unreachable here.
	return len(f.page.VisibleInputs([]string{"input", "textarea", `[contenteditable="true"]`})) >= 2
}

// FillAboutYou mirrors _fill_about_you (app.py:11022-11032): generate a random
// profile, wait for the inputs, fill them, wait 1.5s (anti-detection settle)
// and submit.
func (f *AboutYouFiller) FillAboutYou() error {
	name, birthdate := models.RandomProfile()
	birthYear := strings.Split(birthdate, "-")[0]
	yearNum, err := strconv.Atoi(birthYear)
	if err != nil {
		// Python would raise ValueError out of int(birth_year); random_profile
		// always yields YYYY-MM-DD so this is defensive only.
		return fmt.Errorf("基础资料生日解析失败: %s", birthdate)
	}
	ageNum := time.Now().UTC().Year() - yearNum
	if ageNum < 18 {
		ageNum = 18
	}
	age := strconv.Itoa(ageNum)
	f.logf("填写基础资料: %s / birthdate=%s / birth_year=%s / age=%s", name, birthdate, birthYear, age)
	if err := f.WaitForAboutYouInputs(30 * time.Second); err != nil {
		return err
	}
	f.FillAboutYouInputs(name, birthdate, birthYear, age)
	f.logf("基础资料已填写，等待 1.5 秒后提交")
	time.Sleep(1500 * time.Millisecond)
	done, err := f.SubmitAboutYou()
	if err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("基础资料已填写，但未找到“完成帐户创建”按钮")
	}
	return nil
}

// aboutYouVisibleControlCountJS is _wait_for_about_you_inputs' counter
// (app.py:11192) verbatim.
const aboutYouVisibleControlCountJS = `() => Array.from(document.querySelectorAll('input, textarea, [contenteditable="true"]')).filter(el => {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
}).length`

// WaitForAboutYouInputs mirrors _wait_for_about_you_inputs (app.py:11189-11200):
// poll every 0.5s until >=2 visible controls. The error text hardcodes "30 秒"
// exactly as Python does, even when a different timeout is passed.
func (f *AboutYouFiller) WaitForAboutYouInputs(timeout time.Duration) error {
	started := time.Now()
	for time.Since(started) < timeout {
		v, err := f.eval(aboutYouEvalTimeout, aboutYouVisibleControlCountJS)
		if err == nil && v != nil && v.Value.Int() >= 2 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("about-you 页面 30 秒内未出现姓名/年龄输入框")
}

// ---------------------------------------------------------------------------
// submission
// ---------------------------------------------------------------------------

// AboutYouSubmitDone mirrors _about_you_submit_done (app.py:11034-11048).
// Returns an error only for the closed-page case, which Python raises.
func (f *AboutYouFiller) AboutYouSubmitDone(beforeURL string) (bool, error) {
	if f.page == nil || f.page.IsClosed() {
		return false, fmt.Errorf("浏览器页面已关闭，无法等待基础资料提交结果")
	}
	currentURL := f.page.URL()
	if f.hasChatGPTSession() {
		return true, nil
	}
	if currentURL != beforeURL {
		return true, nil
	}
	if strings.Contains(currentURL, "add-phone") || strings.Contains(currentURL, "phone-verification") {
		return true, nil
	}
	if f.hasRegisterPhoneNumberForm() {
		return true, nil
	}
	if f.hasVisiblePassword() {
		return true, nil
	}
	return !f.HasAboutYouForm(), nil
}

// aboutYouSubmitTexts is the _click_button_by_text ladder of _submit_about_you
// (app.py:11053), verbatim and in order.
var aboutYouSubmitTexts = []string{
	"Finish creating account",
	"Finalizar la creación de la cuenta",
	"Finalizar la creacion de la cuenta",
	"アカウントの作成を完了する",
	"作成を完了",
	"完成帐户创建",
	"完成账户创建",
	"Create account",
	"Continue",
	"完成",
}

// SubmitAboutYou mirrors _submit_about_you (app.py:11050-11062): click ladder,
// then poll _about_you_submit_done for 30s at 0.25s cadence.
func (f *AboutYouFiller) SubmitAboutYou() (bool, error) {
	if f.page == nil {
		return false, nil
	}
	beforeURL := f.page.URL()
	if !f.ClickFinishCreatingAccount() && !f.clickContinue() {
		if !f.ClickButtonByText(aboutYouSubmitTexts) {
			return false, nil
		}
	}

	started := time.Now()
	for time.Since(started) < 30*time.Second {
		done, err := f.AboutYouSubmitDone(beforeURL)
		if err != nil {
			return false, err
		}
		if done {
			return true, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	f.logf("基础资料提交后页面未跳转，继续检测当前页面状态")
	return true, nil
}

// aboutYouFinishSelector is one rung of the _click_finish_creating_account
// priority ladder. label keeps the original Playwright selector string because
// it is echoed verbatim into the Chinese log lines; xpath is the go-rod
// rewrite of :has-text() (no such pseudo-selector outside Playwright).
type aboutYouFinishSelector struct {
	label string
	xpath string
}

// aboutYouHasText renders Playwright's :has-text(needle) as an XPath predicate.
// :has-text folds case AND normalizes whitespace on both sides; a plain
// contains(., "X") is case-sensitive and whitespace-exact, so a label uppercased
// by CSS or wrapped across two text nodes silently fails to match and the rung
// is skipped. XPath 1.0 has no lower-case(), hence translate().
func aboutYouHasText(needle string) string {
	const upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const lower = "abcdefghijklmnopqrstuvwxyz"
	return `contains(translate(normalize-space(.), "` + upper + `", "` + lower + `"), "` +
		strings.ToLower(needle) + `")`
}

// aboutYouFinishSelectors mirrors app.py:11065-11074 — ORDER IS PRIORITY.
var aboutYouFinishSelectors = []aboutYouFinishSelector{
	{`button:has-text("Finish creating account")`, `//button[` + aboutYouHasText("Finish creating account") + `]`},
	{`button:has-text("Finalizar la creación de la cuenta")`, `//button[` + aboutYouHasText("Finalizar la creación de la cuenta") + `]`},
	{`button:has-text("Finalizar la creacion de la cuenta")`, `//button[` + aboutYouHasText("Finalizar la creacion de la cuenta") + `]`},
	{`button:has-text("アカウントの作成を完了する")`, `//button[` + aboutYouHasText("アカウントの作成を完了する") + `]`},
	{`button:has-text("作成を完了")`, `//button[` + aboutYouHasText("作成を完了") + `]`},
	{`button[type="submit"]:has-text("Finish")`, `//button[@type="submit"][` + aboutYouHasText("Finish") + `]`},
	{`button[type="submit"]:has-text("作成")`, `//button[@type="submit"][` + aboutYouHasText("作成") + `]`},
	{`button[data-dd-action-name="Continue"][type="submit"]:has-text("Finish")`, `//button[@data-dd-action-name="Continue"][@type="submit"][` + aboutYouHasText("Finish") + `]`},
}

// aboutYouElementRectJS returns the element's viewport rect (Playwright's
// bounding_box equivalent for a main-frame element).
const aboutYouElementRectJS = `function(){
    const r = this.getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
}`

// aboutYouFinishFormSubmitJS is the DOM fallback of
// _click_finish_creating_account (app.py:11116-11153) verbatim.
const aboutYouFinishFormSubmitJS = `() => {
    const visible = (el) => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const enabled = (el) => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
    const buttons = Array.from(document.querySelectorAll('button')).filter(el => visible(el) && enabled(el));
    const finish = buttons.find(el => {
        const text = (el.textContent || '').trim();
        return text.includes('Finish creating account')
            || text.includes('Finalizar la creación de la cuenta')
            || text.includes('Finalizar la creacion de la cuenta')
            || text.includes('アカウントの作成を完了する')
            || text.includes('作成を完了')
            || text.includes('完成帐户创建')
            || text.includes('完成账户创建');
    });
    const submit = finish || buttons.find(el =>
        (el.type || '').toLowerCase() === 'submit'
        && (el.getAttribute('data-dd-action-name') || '') === 'Continue'
        && /Finish|作成|完成/.test((el.textContent || '').trim())
    );
    if (!submit) return false;
    submit.scrollIntoView({ block: 'center', inline: 'center' });
    submit.focus();
    const form = submit.closest('form');
    if (form && typeof form.requestSubmit === 'function') {
        form.requestSubmit(submit);
        return true;
    }
    submit.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerType: 'mouse', isPrimary: true }));
    submit.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }));
    submit.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerType: 'mouse', isPrimary: true }));
    submit.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }));
    submit.click();
    return true;
}`

// ClickFinishCreatingAccount mirrors _click_finish_creating_account
// (app.py:11064-11162): for every selector in priority order try force click ->
// coordinate click (down / 0.1s / up) -> focus+Enter; then Enter on the age
// input; then the DOM requestSubmit fallback.
func (f *AboutYouFiller) ClickFinishCreatingAccount() bool {
	p := f.rodPage()
	if p == nil {
		return false
	}

	for _, sel := range aboutYouFinishSelectors {
		// Playwright's is_visible(timeout=700) actually returns immediately;
		// 700ms is kept as the go-rod lookup budget for the same rung.
		el, err := p.Timeout(700 * time.Millisecond).ElementX(sel.xpath)
		if err != nil || el == nil {
			continue
		}
		// CRITICAL: go-rod's Element inherits the page context it was found on,
		// and Element.Timeout(d) derives from THAT context (rod context.go:110).
		// The 700ms lookup budget would therefore cap this whole rung — Python
		// gives each step its own budget (700/3000/5000/3000/3000). Dropping back
		// to the page's base context restores that. Without it, ScrollIntoView's
		// WaitStableRAF alone can consume the remainder on an animating React
		// page and every later step fails instantly with "context deadline
		// exceeded", so all 8 selectors fall through and the form never submits.
		el = el.CancelTimeout()
		if visible, err := el.Visible(); err == nil && !visible {
			continue
		}
		forceErr := ""
		if err := el.Timeout(3 * time.Second).ScrollIntoView(); err != nil {
			forceErr = err.Error()
		}
		if forceErr == "" {
			// Playwright's click(force=True) still delivers a REAL mouse click and
			// can fail; a JS .click() cannot, which would make the coordinate and
			// focus+Enter rungs below dead code.
			if err := f.page.ForcePointerClick(el); err == nil {
				f.logf("已 force click: %s", sel.label)
				return true
			} else {
				forceErr = err.Error()
			}
		}
		f.logf("force click 失败 %s: %s", sel.label, aboutYouTruncate(forceErr, 120))

		if box, err := el.Timeout(3 * time.Second).Eval(aboutYouElementRectJS); err == nil && box != nil {
			x := box.Value.Get("x").Num() + box.Value.Get("width").Num()/2
			y := box.Value.Get("y").Num() + box.Value.Get("height").Num()/2
			clickErr := ""
			if err := p.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
				clickErr = err.Error()
			} else if err := p.Mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
				clickErr = err.Error()
			} else {
				time.Sleep(100 * time.Millisecond)
				if err := p.Mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
					clickErr = err.Error()
				}
			}
			if clickErr == "" {
				f.logf("已坐标点击: %s", sel.label)
				return true
			}
			f.logf("坐标点击失败 %s: %s", sel.label, aboutYouTruncate(clickErr, 120))
		} else if err != nil {
			f.logf("坐标点击失败 %s: %s", sel.label, aboutYouTruncate(err.Error(), 120))
		}

		enterErr := ""
		if err := el.Timeout(3 * time.Second).Focus(); err != nil {
			enterErr = err.Error()
		} else if err := p.Keyboard.Type(input.Enter); err != nil {
			enterErr = err.Error()
		}
		if enterErr == "" {
			f.logf("已聚焦按钮并回车: %s", sel.label)
			return true
		}
		f.logf("按钮回车失败 %s: %s", sel.label, aboutYouTruncate(enterErr, 120))
	}

	inputs := f.page.VisibleInputs([]string{"input"})
	if len(inputs) >= 2 {
		enterErr := ""
		if err := inputs[1].Timeout(3 * time.Second).Focus(); err != nil {
			enterErr = err.Error()
		} else if err := p.Keyboard.Type(input.Enter); err != nil {
			enterErr = err.Error()
		}
		if enterErr == "" {
			f.logf("已在年龄输入框按 Enter 提交")
			return true
		}
		f.logf("年龄输入框 Enter 提交失败: %s", aboutYouTruncate(enterErr, 120))
	}

	v, err := f.eval(aboutYouEvalTimeout, aboutYouFinishFormSubmitJS)
	if err != nil || v == nil || !v.Value.Bool() {
		return false
	}
	f.logf("已提交 Finish creating account 表单")
	_ = f.page.WaitDOMContentLoaded(10 * time.Second)
	return true
}

// ClickButtonByText mirrors _click_button_by_text (app.py:11164-11187): a real
// mouse click at the first visible button whose textContent contains any of
// texts. browser.Page.ClickButtonByText carries the identical JS.
func (f *AboutYouFiller) ClickButtonByText(texts []string) bool {
	if f.page == nil {
		return false
	}
	clicked, label := f.page.ClickButtonByText(texts)
	if !clicked {
		return false
	}
	f.logf("已点击按钮: %s", aboutYouTruncate(strings.TrimSpace(label), 40))
	return true
}

// ---------------------------------------------------------------------------
// field semantics (pure logic)
// ---------------------------------------------------------------------------

// Every cue regex below is applied to text that ultimately comes from the
// RENDERED page (attribute values plus up to three ancestors' innerText), so
// both of RE2's dialect gaps are live here and both are spelled around:
//
//	`\s` -> pyWS   RE2's \s is [\t\n\f\r ]. A localized label rendered as
//	               "Date&nbsp;of&nbsp;birth" then failed `date\s*of\s*birth`,
//	               and the field was classified birth_year instead of
//	               birth_date — i.e. a 4-digit year typed into a full-date
//	               control, which _about_you_values_ok then rejects forever.
//	`\b` -> pyB()  RE2's \b is ASCII-only, so it sees a boundary between a CJK
//	               character and an ASCII letter where Python's (Unicode) \w
//	               sees none: "コードage" matched `\bage\b` in Go but not in
//	               Python, classifying an unrelated control as the age field.
//	`\d` -> pyDigit
//	               RE2's \d is [0-9]. These classes inspect values read BACK out
//	               of the rendered form, so a localized page hands them
//	               ٢٠٠٠-٠١-٠١ where Python's \d matches and Go's does not.
//	               Python then reads the parts with int(), which accepts any Nd
//	               digit — int("١٩٩٩") == 1999 while strconv.Atoi errors — so
//	               pyIntDigits goes with pyDigit everywhere below. Widening one
//	               without the other just moves the mismatch a line later.
//
// Getting this wrong is not cosmetic: _about_you_values_ok is the gate that
// decides whether the about-you form was accepted, and it re-reads the values
// the BROWSER is showing. An ASCII-only \d makes Go declare a correctly filled
// Arabic-locale form a failure and retry it forever.
var (
	aboutYouReTypeDateSpaced = regexp.MustCompile(pyB(`type` + pyWS + `*=` + pyWS + `*date`))
	aboutYouReTypeDate       = regexp.MustCompile(pyB(`type=date`))
	// [/\-.] transliterated to [/.-] (trailing dash = literal) — same class.
	aboutYouReMMDDYYYY = regexp.MustCompile(pyB(`mm` + pyWS + `*[/.-]` + pyWS + `*dd` + pyWS + `*[/.-]` + pyWS + `*yyyy`))
	aboutYouReDDMMYYYY = regexp.MustCompile(pyB(`(?:dd|tt)` + pyWS + `*[/.-]` + pyWS + `*mm` + pyWS + `*[/.-]` + pyWS + `*(?:yyyy|aaaa|jjjj)`))
	aboutYouReYYYYMMDD = regexp.MustCompile(pyB(`yyyy` + pyWS + `*[/.-]` + pyWS + `*mm` + pyWS + `*[/.-]` + pyWS + `*dd`))

	// birth_date_patterns (app.py:11216-11227) as one alternation — identical
	// to Python's any(re.search(p, text, re.I) for p in patterns).
	aboutYouReBirthDateCues = regexp.MustCompile(`(?i)date` + pyWS + `*of` + pyWS + `*birth|birth` + pyWS + `*date|birthday|born` + pyWS + `*on|fecha` + pyWS + `*de` + pyWS + `*nacimiento|geburtstag|出生日期|生年月日|誕生日|` + pyB(`dob`))
	// birth_patterns (app.py:11228-11236) — year-only cues.
	aboutYouReBirthYearCues = regexp.MustCompile(`(?i)birth` + pyWS + `*year|year` + pyWS + `*of` + pyWS + `*birth|born` + pyWS + `*year|生まれた年|生年|出生年|年份`)
	// age_patterns (app.py:11237-11243).
	aboutYouReAgeCues = regexp.MustCompile(`(?i)` + pyB(`age`) + `|how` + pyWS + `*old|年齢|年龄|年纪`)
	// The 生年 vs 生年月日 disambiguation guard (app.py:11247).
	aboutYouReFullDateStems = regexp.MustCompile(`(?i)生年月日|date` + pyWS + `*of` + pyWS + `*birth|birth` + pyWS + `*date|birthday|fecha` + pyWS + `*de` + pyWS + `*nacimiento|geburtstag`)

	aboutYouReISODate  = regexp.MustCompile(`^(` + pyDigit + `{4})-(` + pyDigit + `{2})-(` + pyDigit + `{2})$`)
	aboutYouReSlashY   = regexp.MustCompile(`^(` + pyDigit + `{4})/(` + pyDigit + `{1,2})/(` + pyDigit + `{1,2})$`)
	aboutYouReSlashUS  = regexp.MustCompile(`^(` + pyDigit + `{1,2})/(` + pyDigit + `{1,2})/(` + pyDigit + `{4})$`)
	aboutYouReDotEU    = regexp.MustCompile(`^(` + pyDigit + `{1,2})\.(` + pyDigit + `{1,2})\.(` + pyDigit + `{4})$`)
	aboutYouReYearHead = regexp.MustCompile(`^` + pyDigit + `{4}`)

	aboutYouReDDMMSep = regexp.MustCompile(pyB(`dd` + pyWS + `*[/-]` + pyWS + `*mm` + pyWS + `*[/-]` + pyWS + `*(?:yyyy|aaaa)`))
	aboutYouReUSToken = regexp.MustCompile(pyB(`us|en-us`))
	aboutYouReJJJJ    = regexp.MustCompile(pyB(`jjjj`))
	aboutYouReISOHint = regexp.MustCompile(pyB(`yyyy-mm-dd`))

	aboutYouReMonthToken = regexp.MustCompile(pyB(`bday-month|birthmonth|birth-month|month`))
	aboutYouReMonthOnly  = regexp.MustCompile(pyB(`month|mm`))
	aboutYouReYearOrDay  = regexp.MustCompile(pyB(`year|yyyy|day|dd`))
	aboutYouReDayToken   = regexp.MustCompile(pyB(`bday-day|birthday-day|birth-day|day`))
	aboutYouReDayOnly    = regexp.MustCompile(pyB(`day|dd`))
	aboutYouReYearToken  = regexp.MustCompile(pyB(`bday-year|birthyear|birth-year|year`))
	aboutYouReAgeWord    = regexp.MustCompile(pyB(`age`))

	aboutYouReDigits    = regexp.MustCompile(`^` + pyDigit + `+$`)
	aboutYouReYear4     = regexp.MustCompile(`^` + pyDigit + `{4}$`)
	aboutYouReAge1to3   = regexp.MustCompile(`^` + pyDigit + `{1,3}$`)
	aboutYouReNum1to2   = regexp.MustCompile(`^` + pyDigit + `{1,2}$`)
	aboutYouReNum1to4   = regexp.MustCompile(`^` + pyDigit + `{1,4}$`)
	aboutYouReISOValue  = regexp.MustCompile(`^` + pyDigit + `{4}-` + pyDigit + `{2}-` + pyDigit + `{2}$`)
	aboutYouReSlashVal  = regexp.MustCompile(`^` + pyDigit + `{1,2}/` + pyDigit + `{1,2}/` + pyDigit + `{4}$`)
	aboutYouReDottedVal = regexp.MustCompile(`^` + pyDigit + `{1,2}\.` + pyDigit + `{1,2}\.` + pyDigit + `{4}$`)
)

// AboutYouSecondFieldKindFromContext mirrors
// _about_you_second_field_kind_from_context (app.py:11202-11254): decide whether
// the second control wants a full date, a birth year, or an age. Order matters —
// year-only cues are tested BEFORE full-date cues so the shared CJK stem
// 生年 / 生年月日 resolves correctly.
func AboutYouSecondFieldKindFromContext(context string) string {
	text := context
	lower := strings.ToLower(text)
	// Prefer full date when type=date / bday / birthdate / birthday present
	if aboutYouReTypeDateSpaced.MatchString(lower) || aboutYouReTypeDate.MatchString(lower) {
		return "birth_date"
	}
	for _, token := range []string{"bday", "birthdate", "birthday", "date of birth", "dateofbirth", "dob"} {
		if strings.Contains(lower, token) {
			return "birth_date"
		}
	}
	if aboutYouReMMDDYYYY.MatchString(lower) || aboutYouReDDMMYYYY.MatchString(lower) {
		return "birth_date"
	}
	if aboutYouReYYYYMMDD.MatchString(lower) {
		return "birth_date"
	}
	// Year-only cues before full-date cues that contain the same CJK stem
	// (e.g. 生年 vs 生年月日)
	if aboutYouReBirthYearCues.MatchString(text) {
		// Avoid treating full 生年月日 as year-only
		if !aboutYouReFullDateStems.MatchString(text) {
			return "birth_year"
		}
	}
	if aboutYouReBirthDateCues.MatchString(text) {
		return "birth_date"
	}
	if aboutYouReAgeCues.MatchString(text) {
		return "age"
	}
	// Empty/unknown context: keep legacy default (year-only field)
	return "birth_year"
}

// ParseAboutYouBirthdate mirrors _about_you_parse_birthdate (app.py:11256-11271):
// accepts yyyy-mm-dd, yyyy/m/d, m/d/yyyy and d.m.yyyy, returning zero-padded
// (year, month, day); falls back to a leading 4-digit year with empty parts.
func ParseAboutYouBirthdate(birthdate string) (string, string, string) {
	text := pyStrip(birthdate)
	if m := aboutYouReISODate.FindStringSubmatch(text); m != nil {
		return m[1], m[2], m[3]
	}
	if m := aboutYouReSlashY.FindStringSubmatch(text); m != nil {
		return m[1], aboutYouPad2(m[2]), aboutYouPad2(m[3])
	}
	if m := aboutYouReSlashUS.FindStringSubmatch(text); m != nil {
		return m[3], aboutYouPad2(m[1]), aboutYouPad2(m[2])
	}
	if m := aboutYouReDotEU.FindStringSubmatch(text); m != nil {
		return m[3], aboutYouPad2(m[2]), aboutYouPad2(m[1])
	}
	year := ""
	if aboutYouReYearHead.MatchString(text) {
		// text[:4] on a str is four CODE POINTS; a byte slice would cut a
		// three-byte ٢ into pieces and hand back mojibake.
		year = pyRuneSlice(text, 4)
	}
	return year, "", ""
}

// aboutYouPad2 is f"{int(value):02d}". The int() is Unicode-aware — the caller
// matched `pyDigit{1,2}`, so "٠١" reaches here and Python turns it into 1 — but
// the FORMAT is ASCII, so the padded result is "01" either way.
func aboutYouPad2(value string) string {
	n, ok := pyIntDigits(value)
	if !ok {
		return value
	}
	return fmt.Sprintf("%02d", n)
}

// aboutYouFieldMetaJS is _about_you_field_meta's evaluator (app.py:11277-11319)
// verbatim.
//
// KNOWN PYTHON BUG (preserved bug-for-bug): this enumerator includes `select`
// while _fill_visible_input_by_keyboard's enumerator does NOT (app.py:11781).
// TODO(port): when a <select> precedes a text field, the plan indices produced
// from THIS list are off-by-one for the keyboard filler and target the wrong
// control. Kept identical to Python on purpose — fixing it changes which field
// gets typed into.
const aboutYouFieldMetaJS = `() => {
    const visible = (el) => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const controls = Array.from(document.querySelectorAll('input, textarea, select, [contenteditable="true"]')).filter(visible);
    const labelText = (el) => {
        const parts = [];
        const labelledBy = el.getAttribute('aria-labelledby');
        if (labelledBy) {
            for (const id of labelledBy.split(/\s+/)) {
                const node = document.getElementById(id);
                if (node) parts.push(node.textContent || '');
            }
        }
        for (const label of Array.from(document.querySelectorAll('label'))) {
            if (label.htmlFor && label.htmlFor === el.id) parts.push(label.textContent || '');
            if (label.contains(el)) parts.push(label.textContent || '');
        }
        let p = el.parentElement;
        for (let i = 0; i < 3 && p; i++, p = p.parentElement) {
            const t = (p.innerText || '').trim();
            if (t && t.length < 120) parts.push(t);
        }
        return parts.join(' | ');
    };
    return controls.map((el, index) => ({
        index,
        tag: (el.tagName || '').toLowerCase(),
        type: String(el.getAttribute('type') || el.type || '').toLowerCase(),
        name: String(el.getAttribute('name') || ''),
        id: String(el.id || ''),
        placeholder: String(el.getAttribute('placeholder') || ''),
        autocomplete: String(el.getAttribute('autocomplete') || ''),
        inputmode: String(el.getAttribute('inputmode') || ''),
        ariaLabel: String(el.getAttribute('aria-label') || ''),
        testId: String(el.getAttribute('data-testid') || ''),
        label: labelText(el),
        value: String(el.value || el.textContent || ''),
    }));
}`

// AboutYouFieldMeta mirrors _about_you_field_meta (app.py:11273-11322): visible
// controls plus their semantic metadata; empty slice on any failure.
func (f *AboutYouFiller) AboutYouFieldMeta() []AboutYouFieldMeta {
	v, err := f.eval(aboutYouEvalTimeout, aboutYouFieldMetaJS)
	if err != nil || v == nil || v.Value.Nil() {
		return nil
	}
	var metas []AboutYouFieldMeta
	if err := v.Value.Unmarshal(&metas); err != nil {
		return nil
	}
	return metas
}

func (m AboutYouFieldMeta) field(key string) string {
	switch key {
	case "type":
		return m.Type
	case "name":
		return m.Name
	case "id":
		return m.ID
	case "placeholder":
		return m.Placeholder
	case "autocomplete":
		return m.Autocomplete
	case "inputmode":
		return m.Inputmode
	case "ariaLabel":
		return m.AriaLabel
	case "testId":
		return m.TestID
	case "label":
		return m.Label
	case "tag":
		return m.Tag
	case "value":
		return m.Value
	}
	return ""
}

// joinFields reproduces Python's " ".join(str(meta.get(k) or "") ...), keeping
// empty entries (and therefore the double spaces) so \b behaviour matches.
func (m AboutYouFieldMeta) joinFields(keys ...string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, m.field(key))
	}
	return strings.Join(parts, " ")
}

// AboutYouClassifyField mirrors _about_you_classify_field (app.py:11324-11355):
// name / birth_date / birth_year / birth_month / birth_day / age / ignore / unknown.
func AboutYouClassifyField(meta AboutYouFieldMeta) string {
	blob := strings.ToLower(meta.joinFields("type", "name", "id", "placeholder", "autocomplete", "inputmode", "ariaLabel", "testId", "label"))
	if meta.Type == "hidden" {
		return "ignore"
	}
	for _, k := range []string{"csrf", "token", "password", "email", "otp", "code"} {
		if strings.Contains(blob, k) {
			return "ignore"
		}
	}
	// Month / day / year split fields
	if aboutYouReMonthToken.MatchString(blob) && !strings.Contains(blob, "year") {
		if !strings.Contains(blob, "day") || strings.Contains(blob, "month") {
			if aboutYouReMonthOnly.MatchString(blob) && !aboutYouReYearOrDay.MatchString(blob) {
				return "birth_month"
			}
		}
	}
	if aboutYouReDayToken.MatchString(blob) && !strings.Contains(blob, "year") && !strings.Contains(blob, "month") {
		if aboutYouReDayOnly.MatchString(blob) {
			return "birth_day"
		}
	}
	if aboutYouReYearToken.MatchString(blob) && !strings.Contains(blob, "month") {
		return "birth_year"
	}
	// Full date
	if meta.Type == "date" {
		return "birth_date"
	}
	for _, k := range []string{"bday", "birthdate", "birthday", "date of birth", "dateofbirth", "dob", "出生日期", "生年月日"} {
		if strings.Contains(blob, k) {
			return "birth_date"
		}
	}
	// Name
	for _, k := range []string{"fullname", "full name", "full-name", "your name", "全名", "姓名", "お名前"} {
		if strings.Contains(blob, k) {
			if !strings.Contains(blob, "user") || strings.Contains(blob, "name") {
				return "name"
			}
			break
		}
	}
	// Age
	if aboutYouReAgeWord.MatchString(blob) || strings.Contains(blob, "年龄") || strings.Contains(blob, "年齢") {
		return "age"
	}
	return "unknown"
}

// AboutYouSecondFieldContext mirrors _about_you_second_field_context
// (app.py:11357-11376): the descriptive blob of the first date/year/age-ish
// control, else the second control, else the first 2000 chars of the body.
func (f *AboutYouFiller) AboutYouSecondFieldContext() string {
	metas := f.AboutYouFieldMeta()
	for _, meta := range metas {
		switch AboutYouClassifyField(meta) {
		case "birth_date", "birth_year", "birth_month", "birth_day", "age":
			return meta.joinFields("type", "name", "id", "placeholder", "autocomplete", "ariaLabel", "label")
		}
	}
	if len(metas) >= 2 {
		return metas[1].joinFields("type", "name", "id", "placeholder", "autocomplete", "ariaLabel", "label")
	}
	text, ok := f.bodyInnerText(1 * time.Second)
	if !ok {
		return ""
	}
	return aboutYouTruncate(text, 2000)
}

// AboutYouSecondFieldValue mirrors _about_you_second_field_value
// (app.py:11378-11411): the locale/format branching for the second control —
// German dd.mm.yyyy, Spanish/EU dd/mm/yyyy, US mm/dd/yyyy, ISO yyyy-mm-dd.
func AboutYouSecondFieldValue(kind, birthYear, age, birthdate, context string) string {
	if kind == "age" {
		return age
	}
	if kind == "birth_date" {
		text := strings.ToLower(context)
		year, month, day := ParseAboutYouBirthdate(birthdate)
		if !(year != "" && month != "" && day != "") {
			return birthdate
		}
		// German TT.MM.JJJJ
		if strings.Contains(text, "tt.mm") || strings.Contains(text, "tt.mm.jjjj") || aboutYouReJJJJ.MatchString(text) || strings.Contains(text, "geburtstag") {
			return day + "." + month + "." + year
		}
		// Spanish / EU dd/mm
		if strings.Contains(text, "dd/mm") ||
			strings.Contains(text, "dd-mm") ||
			strings.Contains(text, "aaaa") ||
			strings.Contains(text, "fecha de nacimiento") ||
			aboutYouReDDMMSep.MatchString(text) {
			return day + "/" + month + "/" + year
		}
		// US style mm/dd/yyyy
		if strings.Contains(text, "mm/dd") || strings.Contains(text, "mm-dd") || aboutYouReUSToken.MatchString(text) {
			return month + "/" + day + "/" + year
		}
		// native date input and ISO placeholders
		if strings.Contains(text, "type=date") || aboutYouReTypeDateSpaced.MatchString(text) || aboutYouReISOHint.MatchString(text) {
			return year + "-" + month + "-" + day
		}
		return year + "-" + month + "-" + day
	}
	if kind == "birth_month" {
		_, month, _ := ParseAboutYouBirthdate(birthdate)
		return aboutYouOr(month, "01")
	}
	if kind == "birth_day" {
		_, _, day := ParseAboutYouBirthdate(birthdate)
		return aboutYouOr(day, "01")
	}
	return birthYear
}

// AboutYouSecondFieldSelectors mirrors _about_you_second_field_selectors
// (app.py:11413-11456). ORDER IS PRIORITY.
func AboutYouSecondFieldSelectors(kind string) []string {
	switch kind {
	case "age":
		return []string{
			`input[name*="age" i]`,
			`input[id*="age" i]`,
			`input[placeholder*="age" i]`,
			`input[aria-label*="age" i]`,
			`input[inputmode="numeric"]`,
		}
	case "birth_date":
		return []string{
			`input[type="date"]`,
			`input[name*="birthdate" i]`,
			`input[name*="birthday" i]`,
			`input[name*="birth" i]`,
			`input[autocomplete="bday"]`,
			`input[placeholder*="birth" i]`,
			`input[placeholder*="mm" i]`,
			`input[aria-label*="birth" i]`,
			`input[aria-label*="date" i]`,
		}
	case "birth_month":
		return []string{
			`input[name*="month" i]`,
			`input[autocomplete="bday-month"]`,
			`input[aria-label*="month" i]`,
			`select[name*="month" i]`,
		}
	case "birth_day":
		return []string{
			`input[name*="day" i]`,
			`input[autocomplete="bday-day"]`,
			`input[aria-label*="day" i]`,
			`select[name*="day" i]`,
		}
	}
	return []string{
		`input[name*="year" i]`,
		`input[name*="birth" i]`,
		`input[autocomplete="bday-year"]`,
		`input[placeholder*="year" i]`,
		`input[aria-label*="year" i]`,
		`input[aria-label*="birth" i]`,
		`input[inputmode="numeric"]`,
	}
}

// aboutYouYearOK is the hardcoded 1950..2007 validation window of
// _about_you_values_ok (app.py:11472-11476).
func aboutYouYearOK(yearText string) bool {
	if !aboutYouReYear4.MatchString(yearText) {
		return false
	}
	year, ok := pyIntDigits(yearText)
	if !ok {
		return false
	}
	return year >= 1950 && year <= 2007
}

// AboutYouValuesOK mirrors _about_you_values_ok (app.py:11458-11507): confirm
// the visible values actually took. Birth years must fall in 1950..2007 and an
// age in 18..100; a bare year never confirms a full birth_date field.
func AboutYouValuesOK(values []string, secondKind string) bool {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := pyStrip(value); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) == 0 {
		return false
	}
	// Name is first non-empty; remaining may be one date string or month/day/year parts
	name := cleaned[0]
	if utf8.RuneCountInString(name) < 2 {
		return false
	}
	if aboutYouReDigits.MatchString(name) {
		return false
	}
	rest := cleaned[1:]
	if len(rest) == 0 {
		return false
	}

	switch secondKind {
	case "age":
		second := rest[0]
		if !aboutYouReAge1to3.MatchString(second) {
			return false
		}
		n, ok := pyIntDigits(second)
		if !ok {
			return false
		}
		return n >= 18 && n <= 100
	case "birth_year":
		return aboutYouYearOK(rest[0])
	case "birth_month", "birth_day":
		return aboutYouReNum1to2.MatchString(rest[0])
	case "birth_date":
		// Multi-part month/day/year visible values
		if len(rest) >= 3 {
			allNumeric := true
			for _, part := range rest[:3] {
				if !aboutYouReNum1to4.MatchString(part) {
					allNumeric = false
					break
				}
			}
			if allNumeric {
				parts := rest[:3]
				// patterns: MM DD YYYY | DD MM YYYY | YYYY MM DD
				if aboutYouYearOK(parts[2]) {
					return true
				}
				if aboutYouYearOK(parts[0]) {
					return true
				}
			}
		}
		second := rest[0]
		if aboutYouReISOValue.MatchString(second) {
			// second[0:4] counts code points in Python; the separators are
			// ASCII but the digits need not be.
			return aboutYouYearOK(pyRuneSlice(second, 4))
		}
		if aboutYouReSlashVal.MatchString(second) {
			idx := strings.LastIndex(second, "/")
			return aboutYouYearOK(second[idx+1:])
		}
		if aboutYouReDottedVal.MatchString(second) {
			idx := strings.LastIndex(second, ".")
			return aboutYouYearOK(second[idx+1:])
		}
		// Year-only is NOT a valid full birth_date confirmation
		return false
	}

	return aboutYouYearOK(rest[0])
}

// AboutYouPlanFills mirrors _about_you_plan_fills (app.py:11509-11578): map the
// classified controls to (index, kind, value) fills, preferring split
// month/day/year controls, then a full-date control, then an age control, then
// the legacy "second visible control" fallback. De-duplicated by index, order kept.
func (f *AboutYouFiller) AboutYouPlanFills(name, birthdate, birthYear, age string) []AboutYouFill {
	metas := f.AboutYouFieldMeta()
	if len(metas) == 0 {
		secondContext := f.AboutYouSecondFieldContext()
		secondKind := AboutYouSecondFieldKindFromContext(secondContext)
		secondValue := AboutYouSecondFieldValue(secondKind, birthYear, age, birthdate, secondContext)
		return []AboutYouFill{{Index: 0, Kind: "name", Value: name}, {Index: 1, Kind: secondKind, Value: secondValue}}
	}

	kinds := make([]string, len(metas))
	for i := range metas {
		kinds[i] = AboutYouClassifyField(metas[i])
	}
	year, month, day := ParseAboutYouBirthdate(birthdate)
	var fills []AboutYouFill

	pick := func(kind string) *AboutYouFieldMeta {
		for i := range metas {
			if kinds[i] == kind {
				return &metas[i]
			}
		}
		return nil
	}

	// Name
	nameMeta := pick("name")
	if nameMeta == nil {
		// first unknown/non-date field
		for i := range metas {
			if (kinds[i] == "unknown" || kinds[i] == "name") && metas[i].Type != "date" && metas[i].Type != "number" {
				nameMeta = &metas[i]
				break
			}
		}
	}
	if nameMeta == nil {
		nameMeta = &metas[0]
	}
	fills = append(fills, AboutYouFill{Index: nameMeta.Index, Kind: "name", Value: name})

	// Prefer split month/day/year if present
	monthMeta := pick("birth_month")
	dayMeta := pick("birth_day")
	yearMeta := pick("birth_year")
	dateMeta := pick("birth_date")
	ageMeta := pick("age")

	switch {
	case monthMeta != nil || dayMeta != nil || (yearMeta != nil && dateMeta == nil):
		if monthMeta != nil {
			fills = append(fills, AboutYouFill{Index: monthMeta.Index, Kind: "birth_month", Value: aboutYouOr(month, "01")})
		}
		if dayMeta != nil {
			fills = append(fills, AboutYouFill{Index: dayMeta.Index, Kind: "birth_day", Value: aboutYouOr(day, "01")})
		}
		if yearMeta != nil {
			fills = append(fills, AboutYouFill{Index: yearMeta.Index, Kind: "birth_year", Value: aboutYouOr(year, birthYear)})
		}
	case dateMeta != nil:
		ctx := dateMeta.joinFields("type", "name", "placeholder", "autocomplete", "ariaLabel", "label")
		kind := "birth_date"
		value := AboutYouSecondFieldValue(kind, birthYear, age, birthdate, ctx)
		// native date input must be yyyy-mm-dd
		if strings.ToLower(dateMeta.Type) == "date" {
			if year != "" && month != "" && day != "" {
				value = year + "-" + month + "-" + day
			} else {
				value = birthdate
			}
		}
		fills = append(fills, AboutYouFill{Index: dateMeta.Index, Kind: kind, Value: value})
	case ageMeta != nil:
		fills = append(fills, AboutYouFill{Index: ageMeta.Index, Kind: "age", Value: age})
	default:
		// Fallback: second visible control
		secondIndex := 0
		if len(metas) > 1 {
			secondIndex = 1
		}
		secondContext := f.AboutYouSecondFieldContext()
		secondKind := AboutYouSecondFieldKindFromContext(secondContext)
		secondValue := AboutYouSecondFieldValue(secondKind, birthYear, age, birthdate, secondContext)
		fills = append(fills, AboutYouFill{Index: secondIndex, Kind: secondKind, Value: secondValue})
	}

	// de-dup by index, keep order
	seen := map[int]bool{}
	ordered := make([]AboutYouFill, 0, len(fills))
	for _, item := range fills {
		if seen[item.Index] {
			continue
		}
		seen[item.Index] = true
		ordered = append(ordered, item)
	}
	return ordered
}

// ---------------------------------------------------------------------------
// the 4-tier fill
// ---------------------------------------------------------------------------

// aboutYouDOMFillJS is tier 2 of _fill_about_you_inputs (app.py:11606-11634)
// verbatim; the Python dict argument becomes positional (index, value).
//
// Enumerates WITH `select` — see the bug note on aboutYouFieldMetaJS.
const aboutYouDOMFillJS = `(index, value) => {
    const visible = (el) => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const controls = Array.from(document.querySelectorAll('input, textarea, select, [contenteditable="true"]')).filter(visible);
    const el = controls[index];
    if (!el) return false;
    el.focus();
    if (el.tagName === 'SELECT') {
        el.value = value;
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
        return true;
    }
    if (el.isContentEditable) {
        el.textContent = value;
    } else {
        const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
        const desc = Object.getOwnPropertyDescriptor(proto, 'value');
        if (desc && desc.set) desc.set.call(el, value); else el.value = value;
    }
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    el.dispatchEvent(new Event('blur', { bubbles: true }));
    return true;
}`

// aboutYouNameSelectors is the tier-3 name ladder (app.py:11647-11653).
var aboutYouNameSelectors = []string{
	`input[name="name"]`,
	`input[name*="name" i]`,
	`input[autocomplete="name"]`,
	`input[placeholder*="name" i]`,
	`input[placeholder*="全名" i]`,
	`input[aria-label*="name" i]`,
	`input[aria-label*="全名" i]`,
}

// FillAboutYouInputs mirrors _fill_about_you_inputs (app.py:11580-11681): the
// 4-tier fallback — (1) mouse+keyboard by planned index, (2) DOM native-setter
// fill by planned index, (3) selector-driven force fill (+ positional force
// fill), (4) keyboard retry — each tier verified with _about_you_values_ok.
// It NEVER fails the caller: every failure degrades to a log line, matching the
// Python contract that the operator can finish the form by hand.
func (f *AboutYouFiller) FillAboutYouInputs(name, birthdate, birthYear, age string) {
	if f.page == nil {
		return
	}
	plan := f.AboutYouPlanFills(name, birthdate, birthYear, age)
	primarySecondKind := "birth_date"
	for _, item := range plan {
		if item.Kind != "name" {
			primarySecondKind = item.Kind
			break
		}
	}
	parts := make([]string, 0, len(plan))
	for _, item := range plan {
		parts = append(parts, fmt.Sprintf("#%d:%s=%s", item.Index, item.Kind, item.Value))
	}
	f.logf("基础资料字段识别: %s", strings.Join(parts, ", "))

	// 1) Keyboard fill by planned indices
	keyboardErr := ""
	for _, item := range plan {
		if err := f.FillVisibleInputByKeyboard(item.Index, item.Value); err != nil {
			keyboardErr = err.Error()
			break
		}
	}
	if keyboardErr == "" {
		f.FocusAboutYouSubmitOrBody()
		values := f.VisibleInputValues()
		if AboutYouValuesOK(values, primarySecondKind) {
			f.logf("基础资料已通过键盘输入")
			return
		}
		f.logf("基础资料键盘输入后校验未通过，当前值=%s", aboutYouPyList(values))
	} else {
		f.logf("基础资料键盘输入失败，改用 DOM 填写: %s", aboutYouTruncate(keyboardErr, 120))
	}

	// 2) DOM fill by planned indices
	domErr := ""
	for _, item := range plan {
		if _, err := f.eval(aboutYouEvalTimeout, aboutYouDOMFillJS, item.Index, item.Value); err != nil {
			domErr = err.Error()
			break
		}
	}
	if domErr == "" {
		f.FocusAboutYouSubmitOrBody()
		values := f.VisibleInputValues()
		if AboutYouValuesOK(values, primarySecondKind) {
			f.logf("基础资料已通过 DOM 填写")
			return
		}
	} else {
		f.logf("基础资料 DOM 填写异常: %s", aboutYouTruncate(domErr, 120))
	}

	// 3) Selector-based fill
	filledName := f.FillFirstVisible(aboutYouNameSelectors, name)
	// Fill each planned non-name field via selectors
	filledSecond := false
	for _, item := range plan {
		if item.Kind == "name" {
			continue
		}
		if f.FillFirstVisible(AboutYouSecondFieldSelectors(item.Kind), item.Value) {
			filledSecond = true
		}
	}

	if !filledName || !filledSecond {
		visibleInputs := f.page.VisibleInputs([]string{"input", "textarea", "select"})
		for _, item := range plan {
			if item.Index < len(visibleInputs) {
				f.ForceFillLocator(visibleInputs[item.Index], item.Value)
			}
		}
	}

	values := f.VisibleInputValues()
	if AboutYouValuesOK(values, primarySecondKind) {
		return
	}

	// 4) Mouse click + keyboard retry
	f.logf("基础资料 DOM 填写未生效，改用鼠标点击 + 键盘输入")
	for _, item := range plan {
		if err := f.FillVisibleInputByKeyboard(item.Index, item.Value); err != nil {
			// Python lets this RuntimeError escape _fill_about_you_inputs; the Go
			// port degrades gracefully instead (this method must never fail the
			// flow) while still aborting the remaining fills like Python does.
			f.logf("基础资料键盘补填失败: %s", aboutYouTruncate(err.Error(), 120))
			return
		}
	}
	f.FocusAboutYouSubmitOrBody()
	values = f.VisibleInputValues()
	if !AboutYouValuesOK(values, primarySecondKind) {
		f.logf("基础资料自动填写未确认成功，当前可见输入值=%s。请在浏览器中手动填写/提交；程序将继续等待登录完成", aboutYouPyList(values))
	}
}

// AboutYouCurrentValuesOK mirrors _about_you_current_values_ok (app.py:11765-11770).
func (f *AboutYouFiller) AboutYouCurrentValuesOK() bool {
	return AboutYouValuesOK(f.VisibleInputValues(), f.AboutYouSecondFieldKind())
}

// AboutYouSecondFieldKind mirrors _about_you_second_field_kind (app.py:11772-11776).
func (f *AboutYouFiller) AboutYouSecondFieldKind() string {
	kind := AboutYouSecondFieldKindFromContext(f.AboutYouSecondFieldContext())
	if kind == "" {
		return "birth_year"
	}
	return kind
}

// aboutYouKeyboardBoxJS is _fill_visible_input_by_keyboard's locator
// (app.py:11780-11791) verbatim.
//
// KNOWN PYTHON BUG (preserved bug-for-bug): this enumerator does NOT include
// `select`, while _about_you_field_meta / _visible_input_values DO.
// TODO(port): plan indices computed from the select-inclusive list can therefore
// address the wrong control here whenever a visible <select> precedes the target
// field. Behaviour kept identical to Python on purpose.
const aboutYouKeyboardBoxJS = `(index) => {
    const controls = Array.from(document.querySelectorAll('input, textarea, [contenteditable="true"]')).filter(el => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    });
    const el = controls[index];
    if (!el) return null;
    el.scrollIntoView({ block: 'center', inline: 'center' });
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
}`

// aboutYouKeyboardCommitJS is the post-typing event dispatch
// (app.py:11801-11814) verbatim — same select-less enumerator.
const aboutYouKeyboardCommitJS = `(index) => {
    const controls = Array.from(document.querySelectorAll('input, textarea, [contenteditable="true"]')).filter(el => {
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    });
    const el = controls[index];
    if (!el) return false;
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('change', { bubbles: true }));
    el.dispatchEvent(new Event('blur', { bubbles: true }));
    return true;
}`

// FillVisibleInputByKeyboard mirrors _fill_visible_input_by_keyboard
// (app.py:11778-11816): real mouse click on the control's centre, Ctrl+A +
// Backspace, per-char typing at 30ms, explicit input/change/blur, 0.5s settle.
func (f *AboutYouFiller) FillVisibleInputByKeyboard(index int, value string) error {
	p := f.rodPage()
	if p == nil {
		return fmt.Errorf("未找到第 %d 个可见输入框", index+1)
	}
	v, err := f.eval(aboutYouEvalTimeout, aboutYouKeyboardBoxJS, index)
	if err != nil || v == nil || v.Value.Nil() {
		return fmt.Errorf("未找到第 %d 个可见输入框", index+1)
	}
	x := v.Value.Get("x").Num()
	y := v.Value.Get("y").Num()
	if err := p.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
		return fmt.Errorf("输入框鼠标定位失败: %w", err)
	}
	if err := p.Mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("输入框鼠标点击失败: %w", err)
	}
	// page.keyboard.press("Control+A") then "Backspace"
	if err := p.KeyActions().Press(input.ControlLeft).Type(input.KeyA).Do(); err != nil {
		return fmt.Errorf("清空输入框失败: %w", err)
	}
	if err := p.Keyboard.Type(input.Backspace); err != nil {
		return fmt.Errorf("清空输入框失败: %w", err)
	}
	f.typeWithDelay(value, 30*time.Millisecond)
	// Python ignores the result of this evaluate; so do we.
	_, _ = f.eval(aboutYouEvalTimeout, aboutYouKeyboardCommitJS, index)
	time.Sleep(500 * time.Millisecond)
	return nil
}

// typeWithDelay reproduces Playwright's keyboard.type(value, delay=30):
// one keydown/keyup pair per rune with a 30ms gap (anti-detection cadence).
func (f *AboutYouFiller) typeWithDelay(value string, delay time.Duration) {
	p := f.rodPage()
	if p == nil {
		return
	}
	for _, r := range value {
		if !aboutYouTypeRune(p, r) {
			// Runes outside go-rod's US keymap (CJK etc.) cannot be sent as key
			// events; Input.insertText is the documented substitute.
			_ = p.InsertText(string(r))
		}
		time.Sleep(delay)
	}
}

// aboutYouTypeRune sends one rune as a key event; input.Key.Info() PANICS for
// runes that have no keymap entry, so the panic is contained here.
func aboutYouTypeRune(p *rod.Page, r rune) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if err := p.Keyboard.Type(input.Key(r)); err != nil {
		return false
	}
	return true
}

// aboutYouVisibleInputValuesJS is _visible_input_values (app.py:11821-11832)
// verbatim — enumerates WITH `select` (see the bug note on aboutYouFieldMetaJS).
const aboutYouVisibleInputValuesJS = `() => Array.from(document.querySelectorAll('input, textarea, select, [contenteditable="true"]')).filter(el => {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    if (!(r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none')) return false;
    const type = String(el.type || '').toLowerCase();
    if (type === 'hidden' || type === 'password') return false;
    return true;
}).map(el => {
    if (el.isContentEditable) return (el.textContent || '').trim();
    if (el.tagName === 'SELECT') return String(el.value || '').trim();
    return String(el.value || '').trim();
})`

// VisibleInputValues mirrors _visible_input_values (app.py:11818-11836): the
// trimmed values of every visible non-hidden/non-password control; empty on error.
func (f *AboutYouFiller) VisibleInputValues() []string {
	v, err := f.eval(aboutYouEvalTimeout, aboutYouVisibleInputValuesJS)
	if err != nil || v == nil || v.Value.Nil() {
		return nil
	}
	arr := v.Value.Arr()
	values := make([]string, 0, len(arr))
	for _, item := range arr {
		values = append(values, item.Str())
	}
	return values
}

// aboutYouFocusSubmitOrBodyJS is _focus_about_you_submit_or_body
// (app.py:11842-11867) verbatim.
const aboutYouFocusSubmitOrBodyJS = `() => {
    const visible = (el) => {
        if (!el) return false;
        const r = el.getBoundingClientRect();
        const s = getComputedStyle(el);
        return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
    };
    const enabled = (el) => el && !el.disabled && el.getAttribute('aria-disabled') !== 'true';
    const active = document.activeElement;
    if (active && typeof active.blur === 'function') {
        try { active.blur(); } catch (e) {}
    }
    const buttons = Array.from(document.querySelectorAll('button, [role="button"], input[type="submit"]')).filter(visible);
    const texts = ['continue', 'next', 'submit', 'done', 'finish', 'create', '继续', '下一步', '完成', '创建'];
    for (const btn of buttons) {
        if (!enabled(btn)) continue;
        const t = ((btn.innerText || btn.textContent || btn.value || '') + ' ' + (btn.getAttribute('aria-label') || '')).toLowerCase();
        if (texts.some(x => t.includes(x))) {
            try { btn.focus(); return 'button'; } catch (e) {}
        }
    }
    if (document.body && typeof document.body.focus === 'function') {
        try { document.body.tabIndex = -1; document.body.focus(); } catch (e) {}
    }
    return 'body';
}`

const aboutYouBlurActiveJS = `() => { try { document.activeElement && document.activeElement.blur(); } catch (e) {} }`

// FocusAboutYouSubmitOrBody mirrors _focus_about_you_submit_or_body
// (app.py:11838-11879): blur the active input and focus the submit button (then
// Tab off it) or the body, so date widgets COMMIT their value. The 0.2s tail is
// part of the anti-detection cadence.
func (f *AboutYouFiller) FocusAboutYouSubmitOrBody() {
	v, err := f.eval(aboutYouEvalTimeout, aboutYouFocusSubmitOrBodyJS)
	if err != nil {
		_, _ = f.eval(aboutYouEvalTimeout, aboutYouBlurActiveJS)
	} else if v != nil && v.Value.Str() == "button" {
		if p := f.rodPage(); p != nil {
			_ = p.Keyboard.Type(input.Tab)
		}
	}
	time.Sleep(200 * time.Millisecond)
}

// FillFirstVisible mirrors _fill_first_visible (app.py:11881-11885): force fill
// the first visible element matched by the selector ladder (order = priority).
func (f *AboutYouFiller) FillFirstVisible(selectors []string, value string) bool {
	if f.page == nil {
		return false
	}
	for _, el := range f.page.VisibleInputs(selectors) {
		if f.ForceFillLocator(el, value) {
			return true
		}
	}
	return false
}

// ForceFillLocator mirrors _force_fill_locator (app.py:11887-11901): click,
// fill, then the React-safe native prototype value-setter plus
// input/change/blur. browser.Page.ForceFill carries the identical recipe.
func (f *AboutYouFiller) ForceFillLocator(el *rod.Element, value string) bool {
	if f.page == nil {
		return false
	}
	return f.page.ForceFill(el, value)
}
