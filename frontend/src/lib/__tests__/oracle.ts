/*
  The answers app.py itself gives, as data.

  `app-py-oracle.json` is GENERATED. It is the output of running relevant slices
  of the legacy Python app.py — sliced out by line number and exec'd, not retyped
  — under CPython 3.12 over a corpus, and it is
  the only thing the ported logic is compared against in this suite.

  That distinction is the whole point. An expectation written by reading
  `ImportAccounts.svelte` and copying what it does proves the file agrees with
  itself. These expectations were produced by the Tk original, so a test that
  passes means the port matches the program it is a port OF. Where the two
  cannot agree — Python's `\d` and `str.isdigit()` are Unicode-aware, JS's are
  not — the fixture carries the divergent cases in their own keys, and the tests
  that read those keys assert the DIFFERENCE deliberately rather than papering
  over it.

  To regenerate, from `frontend/`:

      $env:OPENAI_REGISTER_PYTHON_REFERENCE = "<旧版 Python 仓库目录>"
      python src/lib/__tests__/app-py-oracle.py src/lib/__tests__/app-py-oracle.json

  The generator is checked in next to its output on purpose. A golden file
  nobody can rebuild stops being an oracle the first time app.py moves — it
  becomes a snapshot of whatever the port did on the day it was written, which
  is the thing this file exists not to be. Do not hand-edit the JSON.

  The `as unknown as` cast is here so that TypeScript models the fixture from
  the declarations below rather than inferring 70 KB of string literal types
  into every test that touches it.
*/
import type { AccountRow } from '../api'
import raw from './app-py-oracle.json'

/** One `MailAccount` as `parse_account_line` (app.py:1617) built it. */
export type OracleAccount = {
  email: string
  password: string
  clientID: string
  refreshToken: string
  accountType: string
  status: string
  openaiRT: string
  authPhoneNumber: string
  authPhoneSMSURL: string
  receiveMailbox: string
  mailProvider: string
}

/** One input line and what app.py made of it; `error` is its ValueError text. */
export type OracleParseCase = {
  line: string
  error: string
  account: OracleAccount | null
}

/** One paste-box body and app.py's per-line verdicts (`import_accounts`, 14686). */
export type OracleParseTextCase = {
  text: string
  rows: { line: number; error: string; account: OracleAccount | null }[]
}

/**
 * A table row as Python derived it. The Go backend computes `statusText`,
 * `hasSession`, `link` and `attempts` for the real UI, so pinning the TS
 * filters means feeding them the same DERIVED row Python's own
 * `_account_status_text` / `session_results` / `results` produced.
 */
export type OracleRow = {
  email: string
  account_type: string
  group: string
  statusText: string
  hasSession: boolean
  link: string
  attempts: number
}

/** `_account_visible_indices` (app.py:19108) for one filter triple. */
export type OracleVisibleCase = {
  group: string
  statusFilter: string
  search: string
  /** Indices into `oracle.rows`, in app.py's own order. */
  visible: number[]
}

/** `_account_display_indices` (app.py:19726) for one column/direction. */
export type OracleSortCase = {
  column: string
  direction: string
  emails: string[]
}

/** `_toggle_account_sort` (app.py:19045) as a state transition. */
export type OracleToggleCase = {
  from: { column: string; direction: string }
  clicked: string
  to: { column: string; direction: string }
}

/** `_smsbower_settings` + `save_smsbower_settings` (app.py:14366/14388). */
export type OracleSmsCase = {
  /** enabled, api_key, service, country, max_price — the five StringVars. */
  input: [boolean, string, string, string, string]
  /** app.py's own ValueError text, or '' when it saved. */
  error: string
  /** What it would have persisted, or null when it raised. */
  normalized: {
    enabled: boolean
    apiKey: string
    service: string
    country: string
    maxPrice: string
  } | null
}

export type Oracle = {
  _generated_by: string
  constants: {
    DEFAULT_PAYPAL_EXTENSION_DIR: string
    AUDIO_DEFAULT_DEVICE_LABEL: string
    SMSBOWER_DEFAULT_SERVICE: string
    SMSBOWER_DEFAULT_COUNTRY: string
    SMSBOWER_DEFAULT_MAX_PRICE: string
    PROXY_ROUTE_MODE_DEFAULT: string
    PROXY_ROUTE_MODE_LOCAL_ONLY: string
    ACCOUNT_ALL_GROUP: string
    ACCOUNT_DEFAULT_GROUP: string
    ACCOUNT_SORT_COLUMNS: string[]
    ACCOUNT_SORT_LABELS: Record<string, string>
    ACCOUNT_STATUS_FILTER_ALL: string
    ACCOUNT_STATUS_FILTER_OPTIONS: string[]
    FAILURE_WORDS: string[]
    ACCOUNT_COLUMN_WIDTHS: Record<string, number>
    SOUND_SEEDS: {
      success_sound_enabled: boolean
      success_audio_device: string
      pause_others_on_link_success: boolean
    }
  }
  /**
   * The 导出转换 group's `_button_grid` rows (app.py 13765-13773): the Tk button
   * CAPTION, its handler, the tooltip, and the line the handler is defined on.
   * Read out of the grid call, not out of the preview-dialog titles the
   * handlers pass to `_preview_and_save_text` — two of the six differ.
   */
  exportGroup: { label: string; handler: string; tooltip: string; handlerLine: number }[]
  promptTitles: { kind: string; title: string }[]
  normalizeEmail: { input: string; output: string }[]
  parseLine: OracleParseCase[]
  /** Cases where Python's Unicode-aware `\d` is expected to beat JS's ASCII one. */
  parseLineUnicodeDigits: OracleParseCase[]
  parseText: OracleParseTextCase[]
  importGroup: { active: string; group: string }[]
  rows: OracleRow[]
  visible: OracleVisibleCase[]
  sorted: OracleSortCase[]
  toggle: OracleToggleCase[]
  smsbower: OracleSmsCase[]
  /** Cases where `float()` / `str.isdigit()` accept what the editor refuses. */
  smsbowerDivergent: OracleSmsCase[]
}

export const oracle = raw as unknown as Oracle

/**
 * An `OracleRow` widened into the `ui.AccountRow` the components take.
 *
 * The persisted half is filled with placeholders on purpose: not one of the
 * ported predicates reads `password`, `raw`, `client_id` or the rest, and
 * giving them plausible values would invite a future test to depend on
 * something the Go row does not actually drive the table with.
 */
export function accountRow(row: OracleRow): AccountRow {
  return {
    email: row.email,
    password: '',
    client_id: '',
    refresh_token: '',
    raw: '',
    account_type: row.account_type,
    // `status` is the STORED value; the filters read `statusText`. Left blank so
    // a test that accidentally reads the wrong one fails loudly.
    status: '',
    openai_rt: '',
    auth_phone_number: '',
    auth_phone_sms_url: '',
    receive_mailbox: '',
    mail_provider: '',
    group: row.group,
    key: row.email.toLowerCase(),
    statusText: row.statusText,
    attempts: row.attempts,
    hasSession: row.hasSession,
    link: row.link,
  }
}

/** Every oracle row, in app.py's list order, as `AccountRow`s. */
export const accountRows: AccountRow[] = oracle.rows.map(accountRow)
