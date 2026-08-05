<script module lang="ts">
  export type CloudMailSettings = {
    enabled: boolean
    baseUrl: string
    token: string
  }
</script>

<script lang="ts">
  /*
    S14 邮箱 / 导入邮箱 — ported from app.py 13358–13386 (Tk tab title 导入邮箱,
    sidebar entry 邮箱). Labels, the hint and the button captions are verbatim
    from the Tk source; the button `title=` strings are the Tk tooltips, which
    UI_SPEC §0.7 says to carry over as help text.

    Cloud Mail 的保存、只读探测和程序 Token 生成均已由 Go 暴露。管理员密码
    只存在于本组件输入框中，由事件交给父层后立即使用，不写入设置。
  */

  let {
    text,
    busy = false,
    canloadfile = true,
    cloudMail,
    cloudBusy = false,
    cloudStatus = '',
    cloudError = '',
    showImport = true,
    onchange,
    onloadfile,
    onimport,
    oncloudchange,
    oncloudsave,
    oncloudprobe,
    oncloudtoken,
  }: {
    /** Contents of the import box (app.py `self.import_text`). */
    text: string
    /** Disables both actions while an import task is running. */
    busy?: boolean
    /** 宿主是否启用了原生文件选择；未启用时按钮保持禁用。 */
    canloadfile?: boolean
    cloudMail: CloudMailSettings
    cloudBusy?: boolean
    cloudStatus?: string
    cloudError?: string
    showImport?: boolean
    /** Fired on every keystroke; the parent owns the value. */
    onchange: (text: string) => void
    /** `从文件导入` — parent opens a file dialog and appends into `text`. */
    onloadfile: () => void
    /** `导入账号` — parent parses `text` into the current group. */
    onimport: () => void
    oncloudchange: (next: CloudMailSettings) => void
    oncloudsave: () => void
    oncloudprobe: (probeEmail: string) => void
    oncloudtoken: (adminEmail: string, adminPassword: string) => void
  } = $props()

  // app.py 13385: `self._scrolled_text(import_pane, height=8)` — Tk `height` is
  // in text lines, which maps onto <textarea rows>.
  const IMPORT_TEXT_ROWS = 8
  let probeEmail = $state('')
  let adminEmail = $state('')
  let adminPassword = $state('')

  function patchCloud(part: Partial<CloudMailSettings>) {
    oncloudchange({ ...cloudMail, ...part })
  }

  function generateToken() {
    const password = adminPassword
    adminPassword = ''
    oncloudtoken(adminEmail, password)
  }
</script>

<section class="card">
  <fieldset>
    <legend>Cloud Mail API</legend>
    <div class="row">
      <label class="check">
        <input
          type="checkbox"
          checked={cloudMail.enabled}
          disabled={cloudBusy}
          onchange={(event) => patchCloud({ enabled: event.currentTarget.checked })}
        />
        启用
      </label>
      <label for="cloud-mail-base">Base</label>
      <input
        id="cloud-mail-base"
        class="mono grow"
        value={cloudMail.baseUrl}
        disabled={cloudBusy}
        placeholder="https://cloud-mail.example.com"
        oninput={(event) => patchCloud({ baseUrl: event.currentTarget.value })}
      />
      <label for="cloud-mail-token">Token</label>
      <input
        id="cloud-mail-token"
        type="password"
        class="mono token"
        autocomplete="off"
        value={cloudMail.token}
        disabled={cloudBusy}
        oninput={(event) => patchCloud({ token: event.currentTarget.value })}
      />
    </div>

    <div class="row">
      <label for="cloud-mail-probe-email">探针邮箱</label>
      <input
        id="cloud-mail-probe-email"
        class="mono grow"
        value={probeEmail}
        disabled={cloudBusy}
        placeholder="留空时使用随机健康检查地址"
        oninput={(event) => (probeEmail = event.currentTarget.value)}
      />
      <button disabled={cloudBusy} title="只读取至多一条邮件列表，不读取正文。" onclick={() => oncloudprobe(probeEmail)}
        >测试连接</button
      >
      <button disabled={cloudBusy} title="保存 Cloud Mail 启用状态、Base URL 和 Token。" onclick={oncloudsave}
        >保存</button
      >
    </div>

    <div class="row">
      <label for="cloud-mail-admin-email">管理员邮箱</label>
      <input
        id="cloud-mail-admin-email"
        class="grow"
        autocomplete="username"
        value={adminEmail}
        disabled={cloudBusy}
        oninput={(event) => (adminEmail = event.currentTarget.value)}
      />
      <label for="cloud-mail-admin-password">管理员密码</label>
      <input
        id="cloud-mail-admin-password"
        type="password"
        class="grow"
        autocomplete="current-password"
        value={adminPassword}
        disabled={cloudBusy}
        oninput={(event) => (adminPassword = event.currentTarget.value)}
      />
      <button
        disabled={cloudBusy}
        title="生成并保存新的程序 Token；旧 Token 会立即失效。"
        onclick={generateToken}>生成Token</button
      >
    </div>

    <p class="hint">域名分邮箱固定后缀：@mail.example.com；启用后按完整收件地址查询邮件。</p>
    {#if cloudError}<p class="error" role="alert">{cloudError}</p>{/if}
    {#if cloudStatus}<p class="ok">{cloudStatus}</p>{/if}
  </fieldset>

  {#if showImport}
    <h2>导入邮箱</h2>

    <div class="row">
      <span class="hint"
        >格式：email----password----client_id----refresh_token；域名转发追加
        ----receive_mailbox=接收主邮箱</span
      >
      <button
        class="spacer-left"
        disabled={busy || !canloadfile}
        title={canloadfile
          ? '选择邮箱账号文本文件并导入到上方输入框；不会立即开始注册。'
          : '当前窗口未启用文件选择，请直接粘贴到下方输入框。'}
        onclick={onloadfile}>从文件导入</button
      >
    </div>

    <textarea
      class="mono import"
      rows={IMPORT_TEXT_ROWS}
      spellcheck="false"
      value={text}
      oninput={(e) => onchange(e.currentTarget.value)}
    ></textarea>

    <div class="row end">
      <button disabled={busy} title="把导入框中的邮箱加入当前分组。" onclick={onimport}>导入账号</button>
    </div>
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
  fieldset {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin: 0;
    padding: 6px 12px 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  legend {
    padding: 0 4px;
    color: var(--muted);
  }
  h2 {
    font-size: 12px;
    font-weight: 600;
    color: var(--muted);
    margin: 0 0 2px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .row.end {
    justify-content: flex-end;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .grow {
    flex: 1 1 26ch;
    min-width: 0;
  }
  .token {
    width: 28ch;
    max-width: 100%;
  }
  /* Tk packs 从文件导入 with side=RIGHT against the hint on the same row. */
  .spacer-left {
    margin-left: auto;
  }
  .hint {
    color: var(--muted);
    overflow-wrap: anywhere;
  }
  .error,
  .ok {
    margin: 0;
  }
  .error {
    color: var(--err);
  }
  .ok {
    color: var(--ok);
  }
  textarea.import {
    width: 100%;
    resize: vertical;
    line-height: 1.5;
  }
</style>
