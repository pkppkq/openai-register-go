<script module lang="ts">
  /*
    A line-for-line port of internal/importer/importer.go, which is itself a
    port of parse_account_line / extract_account_extras (app.py 1617–1690).

    It exists only to render the preview. The import itself is done by the Go
    side (api.ImportAccounts) — this must never become the second
    implementation that decides what gets imported. The rules below are
    therefore transcribed, not re-derived, and the three that most often drift
    are called out where they live:
      · blank lines are skipped AND NOT COUNTED, so the `第 N 行` in an error
        is the N-th NON-BLANK line (importer.go:203, app.py:14687);
      · a cloudmail account is the one case allowed to omit the OAuth pair
        (importer.go:146, app.py:1626);
      · `type=` / `account_type=` accepts ONLY free/plus/team — not k12, not
        pro, even though both are valid account types elsewhere
        (importer.go:99, app.py:1677).
  */

  // The two group constants live next to the table that owns the 分组 filter.
  import { GROUP_ALL, GROUP_DEFAULT } from './Workbench.svelte'

  /** importer.Separator. */
  export const SEPARATOR = '----'

  /** The subset of models.MailAccount one pasted line can produce. */
  export type ParsedAccount = {
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

  /** One non-blank input line: either an account or a reason it was rejected. */
  export type ParsedLine = {
    /** 1-based index over NON-BLANK lines only. */
    line: number
    account: ParsedAccount | null
    error: string
  }

  /** importer.Extras. */
  type Extras = {
    openaiRT: string
    authPhoneNumber: string
    authPhoneSMSURL: string
    receiveMailbox: string
    mailProvider: string
    accountType: string
  }

  // importer.go:36-43. Order matters: these are a first-match-wins chain, and
  // "auth_phone=" must be tested before "auth_phone_sms_url=" would be.
  const PREFIX_OPENAI_RT = ['rt_token=', 'openai_rt=']
  const PREFIX_AUTH_PHONE = ['auth_phone=', 'auth_phone_number=', 'phone=']
  const PREFIX_AUTH_SMS_URL = ['auth_phone_sms_url=', 'auth_sms_url=', 'phone_sms_url=', 'sms_url=']
  const PREFIX_RECEIVE_MAIL = ['receive_mailbox=', 'mailbox_email=', 'receive_email=', 'inbox=']
  const PREFIX_MAIL_PROVIDER = ['mail_provider=', 'mail_type=']
  const PREFIX_ACCOUNT_TYPE = ['account_type=', 'type=']

  // importer.go:47-50.
  const RE_INLINE_PHONE = /^([+\d][\d\s().\-]*)(https?:\/\/\S+)$/
  const RE_BARE_PHONE = /^[+\d][\d\s().\-]{5,}$/
  const RE_BARE_URL = /^https?:\/\/\S+$/

  // models.go:458 reEmail, and the cutset models.NormalizeEmailAddress strips
  // (models.go:475) — Go's strings.Trim takes a SET of runes, so the JS
  // equivalent is a character class anchored at both ends, not a substring cut.
  const RE_EMAIL = /[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/
  const RE_EMAIL_CUTSET = /^[ \t\r\n"'“”‘’<>()[\]{}，,;；]+|[ \t\r\n"'“”‘’<>()[\]{}，,;；]+$/g

  /** models.NormalizeEmailAddress (models.go:473). */
  export function normalizeEmailAddress(value: string): string {
    const text = value.trim().replace(RE_EMAIL_CUTSET, '')
    return RE_EMAIL.exec(text)?.[0] ?? text
  }

  function hasAnyPrefix(lower: string, prefixes: string[]): boolean {
    return prefixes.some((prefix) => lower.startsWith(prefix))
  }

  /** importer.valueAfterEq — Python's `part.split("=", 1)[1].strip()`. */
  function valueAfterEq(part: string): string {
    const at = part.indexOf('=')
    return at < 0 ? '' : part.slice(at + 1).trim()
  }

  /** importer.ExtractExtras (importer.go:72). */
  export function extractExtras(parts: string[]): Extras {
    const out: Extras = {
      openaiRT: '',
      authPhoneNumber: '',
      authPhoneSMSURL: '',
      receiveMailbox: '',
      mailProvider: '',
      accountType: '',
    }
    for (const raw of parts) {
      const part = raw.trim()
      if (part === '') continue
      const lower = part.toLowerCase()

      if (hasAnyPrefix(lower, PREFIX_OPENAI_RT)) {
        out.openaiRT = valueAfterEq(part)
      } else if (hasAnyPrefix(lower, PREFIX_AUTH_PHONE)) {
        out.authPhoneNumber = valueAfterEq(part)
      } else if (hasAnyPrefix(lower, PREFIX_AUTH_SMS_URL)) {
        out.authPhoneSMSURL = valueAfterEq(part)
      } else if (hasAnyPrefix(lower, PREFIX_RECEIVE_MAIL)) {
        out.receiveMailbox = normalizeEmailAddress(valueAfterEq(part))
      } else if (hasAnyPrefix(lower, PREFIX_MAIL_PROVIDER)) {
        // Only these two are accepted; anything else leaves the field EMPTY
        // rather than storing the raw text (importer.go:93).
        const provider = valueAfterEq(part).toLowerCase()
        if (provider === 'cloudmail' || provider === 'outlook') out.mailProvider = provider
      } else if (hasAnyPrefix(lower, PREFIX_ACCOUNT_TYPE)) {
        const accountType = valueAfterEq(part).toLowerCase()
        if (accountType === 'free' || accountType === 'plus' || accountType === 'team') {
          out.accountType = accountType
        }
      } else {
        // Positional fallbacks, sniffed by shape. Each fills only a still-empty
        // field, so an explicit key=value earlier in the line wins.
        const inline = RE_INLINE_PHONE.exec(part)
        if (inline) {
          if (out.authPhoneNumber === '') out.authPhoneNumber = inline[1].trim()
          if (out.authPhoneSMSURL === '') out.authPhoneSMSURL = inline[2].trim()
          continue
        }
        if (out.authPhoneNumber === '' && RE_BARE_PHONE.test(part)) {
          out.authPhoneNumber = part
          continue
        }
        if (out.authPhoneSMSURL === '' && RE_BARE_URL.test(part)) {
          out.authPhoneSMSURL = part
        }
      }
    }
    return out
  }

  /**
   * importer.ParseLine (importer.go:127). Returns the account, or the bare
   * error text the Go side would have produced — same strings, so the preview
   * cannot contradict the import.
   */
  export function parseLine(line: string): { account: ParsedAccount | null; error: string } {
    const fields = line
      .trim()
      .split(SEPARATOR)
      .map((field) => field.trim())
    if (fields.length < 4) {
      return { account: null, error: '格式错误，应为 email----password----client_id----refresh_token' }
    }

    const email = normalizeEmailAddress(fields[0])
    const [, password, clientID, refreshToken] = fields
    const extras = extractExtras(fields.slice(4))

    if (email === '') {
      return { account: null, error: 'email 不能为空' }
    }
    if ((clientID === '' || refreshToken === '') && extras.mailProvider !== 'cloudmail') {
      return { account: null, error: '非 Cloud Mail 邮箱的 client_id / refresh_token 不能为空' }
    }

    // An account that already carries an OpenAI refresh token is assumed Plus
    // (importer.go:150, app.py:1635).
    let accountType = extras.accountType
    if (accountType === '') accountType = extras.openaiRT !== '' ? 'plus' : 'free'

    // importer.go:161 — a three-way conditional, in this precedence.
    let status = ''
    if (extras.openaiRT !== '') status = '已绑定手机号'
    else if (extras.authPhoneNumber !== '' && extras.authPhoneSMSURL !== '') status = '待获取RT'

    return {
      account: {
        email,
        password,
        clientID,
        refreshToken,
        accountType,
        status,
        openaiRT: extras.openaiRT,
        authPhoneNumber: extras.authPhoneNumber,
        authPhoneSMSURL: extras.authPhoneSMSURL,
        receiveMailbox: extras.receiveMailbox,
        mailProvider: extras.mailProvider,
      },
      error: '',
    }
  }

  /** importer.ParseText (importer.go:198). Blank lines are skipped and not counted. */
  export function parseText(text: string): ParsedLine[] {
    const out: ParsedLine[] = []
    let n = 0
    for (const raw of text.split('\n')) {
      if (raw.trim() === '') continue
      n += 1
      const { account, error } = parseLine(raw)
      out.push({ line: n, account, error })
    }
    return out
  }

  /**
   * app.py 14694: new accounts land in the active group filter, unless that is
   * 全部 or 未分组, in which case 未分组. (UI_SPEC §2 action 15 calls the
   * fallback "默认"; app.py's constant is ACCOUNT_DEFAULT_GROUP = 未分组.)
   *
   * PREVIEW ONLY. `ImportAccounts(text)` takes no group — Go applies this same
   * rule to the PERSISTED `settings.account_group_filter` (bindings.go:300).
   * Until the filter is persisted (SaveSettings, still a stub) the two can
   * disagree, which is why the authoritative answer is `ImportResult.group`.
   */
  export function importGroupFor(activeGroup: string): string {
    return activeGroup === GROUP_ALL || activeGroup === GROUP_DEFAULT ? GROUP_DEFAULT : activeGroup
  }
</script>

<script lang="ts">
  /*
    S14 导入邮箱 — the paste box plus the preview the Tk app never had.

    The box itself is MailImport.svelte, unchanged: it is already the verbatim
    port of app.py 13358–13386 (hint, 从文件导入, textarea height 8) and there
    is no reason for a second copy. Everything added here is preview: what the
    N lines currently in the box will do when 导入账号 is pressed, and why any
    of them will be rejected — which matters because app.py reports failures
    only AFTER the fact, in one collapsed log line
    (`已导入 N 个邮箱；失败: …`, 14721).
  */

  import MailImport, { type CloudMailSettings } from './MailImport.svelte'

  let {
    text,
    group,
    existing = [],
    busy = false,
    canloadfile = false,
    importError = '',
    lastImport = '',
    cloudMail,
    cloudBusy = false,
    cloudStatus = '',
    cloudError = '',
    onchange,
    onloadfile,
    onimport,
    oncloudchange,
    oncloudsave,
    oncloudprobe,
    oncloudtoken,
  }: {
    /** Contents of the paste box (app.py `self.import_text`). */
    text: string
    /** The active 分组 filter — previews the group NEW accounts land in. */
    group: string
    /** Keys (lowercased emails) already in the list, for the 新增/更新 split. */
    existing?: string[]
    busy?: boolean
    /** 透传给 MailImport；独立预览未启用宿主文件选择时默认为 false。 */
    canloadfile?: boolean
    /** Set when the ImportAccounts binding rejected. */
    importError?: string
    /** Result line from the last successful import (app.py logs `已导入 N 个邮箱`). */
    lastImport?: string
    cloudMail: CloudMailSettings
    cloudBusy?: boolean
    cloudStatus?: string
    cloudError?: string
    onchange: (text: string) => void
    onloadfile: () => void
    onimport: () => void
    oncloudchange: (next: CloudMailSettings) => void
    oncloudsave: () => void
    oncloudprobe: (probeEmail: string) => void
    oncloudtoken: (adminEmail: string, adminPassword: string) => void
  } = $props()

  let parsed = $derived(parseText(text))
  let failures = $derived(parsed.filter((row) => row.account === null))
  let accounts = $derived(
    parsed.flatMap((row) => (row.account === null ? [] : [row.account])),
  )
  let existingKeys = $derived(new Set(existing))

  // importer.MergeInto (importer.go:228) keys on the lowercased email, so a
  // line repeated inside one paste updates the row the earlier line added.
  let split = $derived.by(() => {
    const seen = new Set(existingKeys)
    let added = 0
    let updated = 0
    for (const account of accounts) {
      const key = account.email.toLowerCase()
      if (seen.has(key)) {
        updated += 1
        continue
      }
      seen.add(key)
      added += 1
    }
    return { added, updated }
  })

  let importGroup = $derived(importGroupFor(group))
</script>

<MailImport
  {text}
  {busy}
  {canloadfile}
  {cloudMail}
  {cloudBusy}
  {cloudStatus}
  {cloudError}
  {onchange}
  {onloadfile}
  {onimport}
  {oncloudchange}
  {oncloudsave}
  {oncloudprobe}
  {oncloudtoken}
/>

<section class="card">
  <!-- Tk has no preview: app.py reports failures only after the fact, in one
       collapsed log line. The headings below reuse app.py's own column
       vocabulary (ACCOUNT_SORT_LABELS 邮箱/类型/状态) plus 行, which is the unit
       its per-line errors already count in (`第 N 行: …`). -->
  <h2>导入预览</h2>

  {#if importError}
    <p class="banner err">{importError}</p>
  {/if}
  {#if lastImport}
    <p class="ok">{lastImport}</p>
  {/if}

  {#if parsed.length === 0}
    <p class="muted">请先粘贴邮箱账户</p>
  {:else}
    <div class="row">
      <span>可导入 {accounts.length} 个邮箱</span>
      <span class="muted">新增 {split.added} · 更新 {split.updated}</span>
      <span class:err={failures.length > 0}>失败 {failures.length}</span>
      <span class="muted">导入到分组：{importGroup}</span>
    </div>

    {#if accounts.length > 0}
      <div class="table-wrap">
        <table>
          <colgroup>
            <col style="width: 48px" />
            <col style="width: 260px" />
            <col style="width: 72px" />
            <col style="width: 160px" />
          </colgroup>
          <thead>
            <tr>
              <th class="num">行</th>
              <th>邮箱</th>
              <th>类型</th>
              <th>状态</th>
            </tr>
          </thead>
          <tbody>
            {#each parsed as row (row.line)}
              {#if row.account}
                <tr>
                  <td class="num muted">{row.line}</td>
                  <td title={row.account.email}>{row.account.email}</td>
                  <td>{row.account.accountType}</td>
                  <!-- Blank is the common case: importer.go:161 writes a status
                       only for an RT or a phone+SMS-URL pair. -->
                  <td>{row.account.status}</td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    {#if failures.length > 0}
      <!-- importer.LineError.Error(): `第 %d 行: %v` (importer.go:193). -->
      <ol>
        {#each failures as row (row.line)}
          <li class="err">第 {row.line} 行: {row.error}</li>
        {/each}
      </ol>
    {/if}
  {/if}
</section>

<style>
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  h2 {
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    margin: 0;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px 14px;
  }
  .table-wrap {
    max-height: 260px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  th {
    position: sticky;
    top: 0;
    background: var(--head-bg);
    color: var(--head-fg);
    font-weight: 600;
    text-align: left;
    height: var(--row-h);
    padding: 0 8px;
    border-bottom: 1px solid var(--border);
  }
  td {
    height: var(--row-h);
    padding: 0 8px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  th.num,
  td.num {
    text-align: center;
  }
  ol {
    margin: 0;
    padding: 0;
    list-style: none;
    max-height: 160px;
    overflow: auto;
  }
  ol li {
    padding: 2px 0;
  }
  .banner {
    margin: 0;
    padding: 8px 12px;
    border-radius: 4px;
    background: #fee4e2;
    border: 1px solid #fda29b;
  }
  .err {
    color: var(--err);
  }
  .ok {
    color: var(--ok);
  }
</style>
