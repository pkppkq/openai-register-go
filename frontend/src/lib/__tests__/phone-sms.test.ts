/*
  S15 手机与接码 — the SMSBower block.

  Validation here is not a nicety. `SaveSettings` does NOT validate
  (bindings.go:371 only calls `ToSnapshot`), so this function is the only thing
  between the user and a value written into the real state.json that the Tk app
  itself refuses to write — and the settings file is shared with a running Tk
  process. The expectations are therefore app.py's: `oracle.smsbower` is the
  output of `_smsbower_settings` (14366) plus `save_smsbower_settings`' API-key
  check (14391), run under CPython over the corpus.

  Two things worth knowing before editing anything here:

    - An empty 最高单价 is MEANINGFUL. It is "no cap", not "unset", so unlike
      服务代码 and 国家 ID it is never replaced by a default. Defaulting it to
      0.07 would silently start capping a user who deliberately uncapped.
    - Python's `float()` is looser than the box: `inf`, `nan` and `1_0` all
      parse, and `float("nan") <= 0` is False, so app.py accepts NaN as a price
      cap. The port refuses all three. That is a deliberate divergence in the
      safe direction and it is asserted as such at the bottom.
*/
import { describe, expect, it } from 'vitest'
import {
  SMSBOWER_DEFAULTS,
  normalizeSmsBower,
  validateSmsBower,
  type SmsBowerSettings,
} from '../pages/PhoneSms.svelte'
import { oracle, type OracleSmsCase } from './oracle'

/** The oracle's five StringVars as the component's settings object. */
function settingsOf(input: OracleSmsCase['input']): SmsBowerSettings {
  const [enabled, apiKey, service, country, maxPrice] = input
  return { enabled, apiKey, service, country, maxPrice }
}

describe('SMSBOWER_DEFAULTS', () => {
  it('are the StringVar seeds of app.py 82-84 and 12383-12387', () => {
    expect(SMSBOWER_DEFAULTS).toEqual({
      enabled: false,
      apiKey: '',
      service: oracle.constants.SMSBOWER_DEFAULT_SERVICE,
      country: oracle.constants.SMSBOWER_DEFAULT_COUNTRY,
      maxPrice: oracle.constants.SMSBOWER_DEFAULT_MAX_PRICE,
    })
  })

  it('start disabled with no key, so a fresh state file rents nothing', () => {
    // Enabling SMSBower is what makes a run able to rent a billable number.
    expect(SMSBOWER_DEFAULTS.enabled).toBe(false)
    expect(SMSBOWER_DEFAULTS.apiKey).toBe('')
  })

  it('ship a default price cap rather than an uncapped pool', () => {
    expect(SMSBOWER_DEFAULTS.maxPrice).toBe('0.07')
    expect(SMSBOWER_DEFAULTS.maxPrice).not.toBe('')
  })
})

describe('normalizeSmsBower', () => {
  it('reproduces app.py 14367-14369 for every case the Python accepted', () => {
    for (const c of oracle.smsbower) {
      if (c.normalized === null) continue
      expect(normalizeSmsBower(settingsOf(c.input)), JSON.stringify(c.input)).toEqual(c.normalized)
    }
  })

  it('substitutes the default for a blank service or country', () => {
    // 14396-14397 pushes the substituted values back into the entries, so the
    // user sees an empty box become dr/33 rather than guessing.
    const out = normalizeSmsBower({ enabled: false, apiKey: '', service: '  ', country: '', maxPrice: '' })
    expect(out.service).toBe(SMSBOWER_DEFAULTS.service)
    expect(out.country).toBe(SMSBOWER_DEFAULTS.country)
  })

  it('never substitutes a default for a blank price, because blank means no cap', () => {
    const out = normalizeSmsBower({ enabled: false, apiKey: '', service: 'dr', country: '33', maxPrice: '   ' })
    expect(out.maxPrice).toBe('')
  })

  it('strips every field and leaves enabled alone', () => {
    const out = normalizeSmsBower({
      enabled: true,
      apiKey: '  key  ',
      service: ' dr ',
      country: ' 33 ',
      maxPrice: ' 0.07 ',
    })
    expect(out).toEqual({ enabled: true, apiKey: 'key', service: 'dr', country: '33', maxPrice: '0.07' })
  })

  it('returns a new object rather than editing the bound state', () => {
    const input: SmsBowerSettings = { enabled: false, apiKey: ' k ', service: '', country: '', maxPrice: '' }
    const out = normalizeSmsBower(input)
    expect(out).not.toBe(input)
    expect(input.apiKey).toBe(' k ')
  })
})

describe('validateSmsBower', () => {
  it('returns app.py own message, or empty, for every case in the corpus', () => {
    for (const c of oracle.smsbower) {
      expect(validateSmsBower(settingsOf(c.input)), JSON.stringify(c.input)).toBe(c.error)
    }
  })

  it('exercises every outcome app.py has, valid included', () => {
    // Guards the loop above: three ValueErrors out of _smsbower_settings, the
    // API-key check save_smsbower_settings adds on top, and success.
    expect([...new Set(oracle.smsbower.map((c) => c.error))].sort()).toEqual(
      [
        '',
        'SMSBower 服务代码格式不正确',
        'SMSBower 国家 ID 必须是数字',
        'SMSBower 最高单价必须是大于 0 的数字，或留空',
        '启用 SMSBower 前请填写 API Key',
      ].sort(),
    )
  })

  it('validates the NORMALISED values, so a blank service is valid', () => {
    // The substitution at 14367 happens before the format test at 14370, which
    // is why an empty 服务代码 is accepted and an empty-looking `  ` one is too.
    expect(validateSmsBower({ enabled: false, apiKey: '', service: '', country: '', maxPrice: '' })).toBe('')
  })

  it('rejects a service code outside [A-Za-z0-9_] (app.py:14370)', () => {
    for (const service of ['dr!', 'dr dr', '汉', 'dr-1']) {
      expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, service }), service).toBe('SMSBower 服务代码格式不正确')
    }
    expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, service: 'ab_09' })).toBe('')
  })

  it('rejects a non-digit country id (app.py:14372)', () => {
    for (const country of ['33a', '-1', '+33', '3.3']) {
      expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, country }), country).toBe('SMSBower 国家 ID 必须是数字')
    }
  })

  it('accepts Unicode decimal digits in the country id, as str.isdigit() does', () => {
    // Not a curiosity: `settings.pyIsDigit` on the Go side makes the same call,
    // so refusing them here would reject a value the backend would store.
    expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, country: '٣٣' })).toBe('')
  })

  it('gives unparseable and non-positive prices the SAME message (app.py 14375-14379)', () => {
    // Python raises a bare ValueError inside the try and rewrites both arms to
    // one string; splitting them here would invent a message the Tk app has not
    // got.
    for (const maxPrice of ['abc', '0', '-0.5', '0.0']) {
      expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, maxPrice }), maxPrice).toBe(
        'SMSBower 最高单价必须是大于 0 的数字，或留空',
      )
    }
  })

  it('accepts the float spellings Python accepts', () => {
    for (const maxPrice of ['1', '.5', '5.', '1e-3', '+1', '0.07']) {
      expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, maxPrice }), maxPrice).toBe('')
    }
  })

  it('folds Unicode decimal digits before parsing the price, as float() does', () => {
    // app.py 14376: `float("１.５")` is 1.5. pyDecimalASCII on the Go side.
    expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, maxPrice: '１.５' })).toBe('')
    expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, maxPrice: '０' })).toBe(
      'SMSBower 最高单价必须是大于 0 的数字，或留空',
    )
  })

  it('accepts an empty price as "no cap"', () => {
    expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, maxPrice: '' })).toBe('')
    expect(validateSmsBower({ ...SMSBOWER_DEFAULTS, maxPrice: '   ' })).toBe('')
  })

  it('checks the API key LAST, after the field formats (app.py:14390)', () => {
    // save_smsbower_settings calls _smsbower_settings first, so a bad service
    // code is reported before the missing key even when both are wrong.
    expect(validateSmsBower({ enabled: true, apiKey: '', service: 'dr!', country: '33', maxPrice: '' })).toBe(
      'SMSBower 服务代码格式不正确',
    )
    expect(validateSmsBower({ enabled: true, apiKey: '', service: 'dr', country: '33', maxPrice: '' })).toBe(
      '启用 SMSBower 前请填写 API Key',
    )
  })

  it('requires a key only when enabled, and treats a whitespace key as missing', () => {
    expect(validateSmsBower({ enabled: false, apiKey: '', service: 'dr', country: '33', maxPrice: '' })).toBe('')
    expect(validateSmsBower({ enabled: true, apiKey: '   ', service: 'dr', country: '33', maxPrice: '' })).toBe(
      '启用 SMSBower 前请填写 API Key',
    )
    expect(validateSmsBower({ enabled: true, apiKey: ' k ', service: 'dr', country: '33', maxPrice: '' })).toBe('')
  })
})

describe('deliberate divergence: the editor is stricter than float()/isdigit()', () => {
  it('refuses what app.py would have saved, and refuses it consistently', () => {
    // ² is Numeric_Type=Digit but not category Nd, so str.isdigit() takes it and
    // \p{Nd} does not. inf/nan/1_0 are float() grammar the box rejects — and
    // `float("nan") <= 0` being False means app.py really does accept NaN as a
    // price cap. Refusing in the editor cannot desynchronise anything: the value
    // simply never reaches disk. It is asserted rather than ignored so that
    // nobody "fixes" the port towards app.py here by accident.
    for (const c of oracle.smsbowerDivergent) {
      expect(c.error, `app.py accepts ${JSON.stringify(c.input)}`).toBe('')
      expect(validateSmsBower(settingsOf(c.input)), `the port refuses it`).not.toBe('')
    }
  })

  it('names the divergent inputs, so the list cannot quietly shrink to nothing', () => {
    expect(oracle.smsbowerDivergent.map((c) => `${c.input[3]}|${c.input[4]}`)).toEqual([
      '²|',
      '33|1_0',
      '33|inf',
      '33|nan',
    ])
  })
})
