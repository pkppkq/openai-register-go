/*
  S14 导入邮箱 — the preview parser.

  This is the highest-stakes pure function in the frontend, because it is a
  SECOND implementation of something the Go side also implements. The user reads
  the preview and then presses 导入账号, which calls `api.ImportAccounts` and lets
  Go parse the same text again; if the two disagree, the preview is a lie about
  what is going into the account list.

  So every expectation below comes from app.py. `oracle.parseLine` is the output
  of `parse_account_line` (1617) and `extract_account_extras` (1645) — sliced out
  of app.py by line number and exec'd under CPython, not retyped — over a corpus
  covering every prefix alias, every positional sniff, the cloudmail exemption,
  the account-type whitelist and the three-way status derivation.
  `oracle.parseText` is `import_accounts`' own line handling (14687/14695).

  ONE DIVERGENCE, and it is in the port's disfavour: Python's `\d` inside a str
  pattern is Unicode-aware, JS's is ASCII. A positional field of fullwidth or
  Arabic-Indic digits is a phone number to app.py and is not one here. It is
  asserted explicitly at the bottom rather than hidden, because the fix belongs
  in internal/importer (which decides what actually gets imported) and a preview
  that "fixed" it alone would be the drift this file exists to prevent.
*/
import { describe, expect, it } from 'vitest'
import {
  SEPARATOR,
  extractExtras,
  importGroupFor,
  normalizeEmailAddress,
  parseLine,
  parseText,
} from '../pages/ImportAccounts.svelte'
import { GROUP_ALL, GROUP_DEFAULT } from '../pages/Workbench.svelte'
import { oracle, type OracleAccount } from './oracle'

/** parseLine's account, shaped like the oracle's, for a whole-object compare. */
function parsed(line: string): { error: string; account: OracleAccount | null } {
  const { account, error } = parseLine(line)
  return { error, account }
}

describe('SEPARATOR', () => {
  it('is the four hyphens app.py 1618 splits on', () => {
    expect(SEPARATOR).toBe('----')
  })
})

describe('normalizeEmailAddress', () => {
  it('agrees with normalize_email_address (app.py:1610) over the corpus', () => {
    for (const c of oracle.normalizeEmail) {
      expect(normalizeEmailAddress(c.input), JSON.stringify(c.input)).toBe(c.output)
    }
  })

  it('strips the Python cutset from BOTH ends, not just a leading run', () => {
    // Go's strings.Trim takes a set of runes; the JS equivalent has to be a
    // character class anchored at each end, which is easy to get wrong as a
    // single leading-only regex.
    expect(normalizeEmailAddress('<a@b.com>')).toBe('a@b.com')
    expect(normalizeEmailAddress('，a@b.com；')).toBe('a@b.com')
  })

  it('returns the trimmed text unchanged when nothing looks like an address', () => {
    // 1614: `match.group(0) if match else text`. An empty return here would
    // turn "email 不能为空" into a wrong diagnosis of a typo'd address.
    expect(normalizeEmailAddress('junk')).toBe('junk')
    expect(normalizeEmailAddress('')).toBe('')
  })

  it('extracts the first address out of surrounding words', () => {
    expect(normalizeEmailAddress('name a@b.com tail')).toBe('a@b.com')
    expect(normalizeEmailAddress('first@x.com second@y.com')).toBe('first@x.com')
  })
})

describe('parseLine', () => {
  it('reproduces parse_account_line for every line in the corpus', () => {
    for (const c of oracle.parseLine) {
      expect(parsed(c.line), JSON.stringify(c.line)).toEqual({ error: c.error, account: c.account })
    }
  })

  it('checks a corpus wide enough to cover the whole function', () => {
    expect(oracle.parseLine.length).toBeGreaterThanOrEqual(60)
    // Both outcomes are represented; a corpus of only-valid lines would never
    // exercise the three error strings.
    expect(oracle.parseLine.some((c) => c.error !== '')).toBe(true)
    expect(oracle.parseLine.some((c) => c.error === '')).toBe(true)
  })

  it('produces app.py error text verbatim, because the Go import will produce it too', () => {
    // importer.go returns the same three strings; the preview must not invent a
    // friendlier wording that then differs from the 导入 log line (14721).
    const errors = new Set(oracle.parseLine.map((c) => c.error).filter((text) => text !== ''))
    expect([...errors].sort()).toEqual(
      [
        '格式错误，应为 email----password----client_id----refresh_token',
        'email 不能为空',
        '非 Cloud Mail 邮箱的 client_id / refresh_token 不能为空',
      ].sort(),
    )
    for (const text of errors) {
      const sample = oracle.parseLine.find((c) => c.error === text) as { line: string }
      expect(parseLine(sample.line).error).toBe(text)
    }
  })

  it('exempts only a cloudmail account from the OAuth pair', () => {
    // app.py 1626. outlook does NOT get the exemption, and neither does an
    // unrecognised provider, which is silently dropped to '' at 1673.
    expect(parseLine('a@b.com----pw--------  ----mail_provider=cloudmail').error).toBe('')
    expect(parseLine('a@b.com----pw--------  ----mail_provider=outlook').error).not.toBe('')
    expect(parseLine('a@b.com----pw--------  ----mail_provider=gmail').error).not.toBe('')
  })

  it('assumes plus for a line that already carries an OpenAI RT (app.py:1635)', () => {
    expect(parseLine('a@b.com----pw----cid----rt----rt_token=RT1').account?.accountType).toBe('plus')
    expect(parseLine('a@b.com----pw----cid----rt').account?.accountType).toBe('free')
    // An explicit type wins over the inference.
    expect(parseLine('a@b.com----pw----cid----rt----type=team----rt_token=RT').account?.accountType).toBe('team')
  })

  it('accepts only free/plus/team for type=, dropping k12 and pro (app.py:1678)', () => {
    // Both are valid account types elsewhere in app.py; the import whitelist is
    // narrower, and widening it here would import accounts the Tk app cannot.
    expect(parseLine('a@b.com----pw----cid----rt----type=k12').account?.accountType).toBe('free')
    expect(parseLine('a@b.com----pw----cid----rt----type=pro').account?.accountType).toBe('free')
  })

  it('derives status three ways, in app.py 1636 precedence', () => {
    // RT wins over phone+url, and phone alone gets nothing.
    expect(parseLine('a@b.com----pw----cid----rt----rt_token=RT').account?.status).toBe('已绑定手机号')
    expect(parseLine('a@b.com----pw----cid----rt----phone=+331----sms_url=https://s/1').account?.status).toBe(
      '待获取RT',
    )
    expect(parseLine('a@b.com----pw----cid----rt----phone=+331').account?.status).toBe('')
    expect(
      parseLine('a@b.com----pw----cid----rt----rt_token=RT----phone=+331----sms_url=https://s/1').account?.status,
    ).toBe('已绑定手机号')
  })
})

describe('extractExtras', () => {
  it('lets an explicit key beat a later positional of the same shape', () => {
    // app.py 1683/1686/1689 all guard on the field still being empty.
    expect(extractExtras(['phone=+1', '+339999999']).authPhoneNumber).toBe('+1')
    expect(extractExtras(['sms_url=https://a/1', 'https://b/2']).authPhoneSMSURL).toBe('https://a/1')
  })

  it('lets the FIRST positional of a shape win over the second', () => {
    expect(extractExtras(['+331234567', '+339999999']).authPhoneNumber).toBe('+331234567')
  })

  it('fills both fields from one glued phone+url token (app.py:1681)', () => {
    const extras = extractExtras(['+33 1 23 45 67https://sms.example/x'])
    expect(extras.authPhoneNumber).toBe('+33 1 23 45 67')
    expect(extras.authPhoneSMSURL).toBe('https://sms.example/x')
  })

  it('requires six or more characters for a bare positional phone (app.py:1686)', () => {
    expect(extractExtras(['123456']).authPhoneNumber).toBe('123456')
    expect(extractExtras(['12345']).authPhoneNumber).toBe('')
  })

  it('accepts only http and https for a bare positional URL', () => {
    expect(extractExtras(['https://s/1']).authPhoneSMSURL).toBe('https://s/1')
    expect(extractExtras(['http://s/1']).authPhoneSMSURL).toBe('http://s/1')
    expect(extractExtras(['ftp://s/1']).authPhoneSMSURL).toBe('')
  })

  it('keeps everything after the first = as the value (app.py `split("=", 1)`)', () => {
    expect(extractExtras(['rt_token=a=b']).openaiRT).toBe('a=b')
  })

  it('matches the prefix case-insensitively but keeps the value case', () => {
    expect(extractExtras(['RT_TOKEN=RT3']).openaiRT).toBe('RT3')
    expect(extractExtras(['type=FREE']).accountType).toBe('free')
  })

  it('normalises a receive_mailbox value like an address', () => {
    expect(extractExtras(['receive_mailbox=<c@d.com>']).receiveMailbox).toBe('c@d.com')
  })

  it('skips blank parts instead of letting them clear a field', () => {
    expect(extractExtras(['   ', 'rt_token=RT']).openaiRT).toBe('RT')
  })

  it('does not confuse phone= with phone_sms_url=, which shares a stem', () => {
    // The prefix chain is first-match-wins; `phone=` is tested before the URL
    // aliases and `phone_sms_url=` does not start with `phone=`.
    const extras = extractExtras(['phone_sms_url=https://s/1'])
    expect(extras.authPhoneSMSURL).toBe('https://s/1')
    expect(extras.authPhoneNumber).toBe('')
  })
})

describe('parseText', () => {
  it('reproduces import_accounts line handling for every body in the corpus', () => {
    for (const c of oracle.parseText) {
      expect(parseText(c.text), JSON.stringify(c.text)).toEqual(c.rows)
    }
  })

  it('numbers 第 N 行 over NON-BLANK lines only (app.py 14687 + 14695)', () => {
    // The trap: `enumerate(lines)` runs over the already-filtered list, so the
    // number in an error message is not the line number in the box. Reporting
    // the raw index would point the user at the wrong line.
    const rows = parseText('\n\na@b.com----pw----cid----rt\n\n\nbad-line\n\n')
    expect(rows.map((row) => row.line)).toEqual([1, 2])
    expect(rows[1].error).not.toBe('')
  })

  it('produces nothing for an empty or all-blank box', () => {
    expect(parseText('')).toEqual([])
    expect(parseText('   \n\t\n')).toEqual([])
  })

  it('handles CRLF, because the paste box and 从文件导入 both deliver it', () => {
    const rows = parseText('a@b.com----pw----cid----rt\r\nc@d.com----pw----cid----rt\r\n')
    expect(rows).toHaveLength(2)
    expect(rows.every((row) => row.error === '')).toBe(true)
    expect(rows[1].account?.refreshToken).toBe('rt')
  })
})

describe('importGroupFor', () => {
  it('agrees with app.py 14694 for every active filter in the corpus', () => {
    for (const c of oracle.importGroup) {
      expect(importGroupFor(c.active), c.active).toBe(c.group)
    }
  })

  it('falls back to 未分组 from 全部 and from 未分组 itself', () => {
    expect(importGroupFor(GROUP_ALL)).toBe(GROUP_DEFAULT)
    expect(importGroupFor(GROUP_DEFAULT)).toBe(GROUP_DEFAULT)
  })

  it('otherwise imports into the group the table is filtered to', () => {
    expect(importGroupFor('组A')).toBe('组A')
    // Untrimmed input is NOT special-cased: Python compares the raw StringVar.
    expect(importGroupFor('全部 ')).toBe('全部 ')
  })

  it('is preview only — ImportResult.group is the authority', () => {
    // `ImportAccounts(text)` takes no group; Go applies the same rule to the
    // PERSISTED account_group_filter (bindings.go:300). This test exists to
    // keep that sentence attached to the function.
    expect(importGroupFor('')).toBe('')
  })
})

describe('known divergence: Unicode digits in a positional phone field', () => {
  it('does not sniff a fullwidth or Arabic-Indic run as a phone number, where app.py does', () => {
    // app.py 1686 `re.fullmatch(r"[+\d][\d\s().-]{5,}", part)` — Python's \d is
    // Unicode-aware, so `１２３４５６７` IS a phone number to the Tk app. JS's \d is
    // [0-9] and internal/importer's is Go's ASCII \d, so neither the preview nor
    // the actual import treats it as one. The preview therefore agrees with the
    // IMPORT, which is what matters; the pair of them differ from app.py.
    for (const c of oracle.parseLineUnicodeDigits) {
      expect(c.account?.authPhoneNumber, 'app.py sniffs it as a phone').not.toBe('')
      expect(parseLine(c.line).account?.authPhoneNumber, 'the port does not').toBe('')
    }
  })
})
