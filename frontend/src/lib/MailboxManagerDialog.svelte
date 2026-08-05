<script module lang="ts">
  export type MailboxMessage = {
    id: string
    folder?: string
    kind?: string
    date?: string
    mailTime?: number
    mailTimeIso?: string
    from?: string
    to?: string
    subject?: string
    snippet?: string
    code?: string
    body?: string
  }

  export type MailboxDialogAction =
    | { kind: 'refresh_folders' }
    | { kind: 'refresh_messages'; openaiOnly: boolean }
    | { kind: 'read_body'; id: string; folder: string }
    | { kind: 'copy_body' }
    | { kind: 'copy_code'; id: string }

  export function mailboxDateText(message: MailboxMessage): string {
    return (message.mailTimeIso || message.date || '').slice(0, 19)
  }

  export function clampMailboxLimit(value: number): number {
    if (!Number.isFinite(value)) return 80
    return Math.min(500, Math.max(10, Math.trunc(value)))
  }
</script>

<script lang="ts">
  let {
    open,
    email,
    aliasParent = '',
    status = '准备读取邮箱',
    folders,
    folder,
    limit,
    search,
    messages,
    selectedMessageId = '',
    body = '',
    busy = false,
    error = '',
    onclose,
    onfolderchange,
    onlimitchange,
    onsearchchange,
    onselect,
    onaction,
  }: {
    open: boolean
    email: string
    aliasParent?: string
    status?: string
    folders: readonly string[]
    folder: string
    limit: number
    search: string
    messages: readonly MailboxMessage[]
    selectedMessageId?: string
    body?: string
    busy?: boolean
    error?: string
    onclose: () => void
    onfolderchange: (folder: string) => void
    onlimitchange: (limit: number) => void
    onsearchchange: (search: string) => void
    onselect: (id: string) => void
    onaction: (action: MailboxDialogAction) => void
  } = $props()

  let selectedMessage = $derived(messages.find((message) => message.id === selectedMessageId))

  function readSelected() {
    if (!selectedMessage) return
    onaction({
      kind: 'read_body',
      id: selectedMessage.id,
      folder: selectedMessage.folder || folder || 'INBOX',
    })
  }

  function handleRowKeydown(event: KeyboardEvent, message: MailboxMessage) {
    if (event.key === 'Enter') {
      onselect(message.id)
      onaction({ kind: 'read_body', id: message.id, folder: message.folder || folder || 'INBOX' })
    } else if (event.key === ' ') {
      event.preventDefault()
      onselect(message.id)
    }
  }

  function handleWindowKeydown(event: KeyboardEvent) {
    if (open && event.key === 'Escape' && !busy) onclose()
  }
</script>

<svelte:window onkeydown={handleWindowKeydown} />

{#if open}
  <div class="backdrop">
    <div
      class="dialog"
      role="dialog"
      aria-modal="false"
      aria-labelledby="mailbox-manager-title"
    >
      <header>
        <div>
          <h2 id="mailbox-manager-title">邮箱管理 - {email}</h2>
          <span>当前邮箱: {email}</span>
          {#if aliasParent}
            <span class="alias">读取主邮箱: {aliasParent}</span>
          {/if}
        </div>
        <strong title="邮箱读取任务状态">{status}</strong>
      </header>

      <div class="controls">
        <label for="mailbox-folder">文件夹</label>
        <select
          id="mailbox-folder"
          value={folder}
          title="选择要查看的邮箱文件夹；切换后由上层刷新该文件夹邮件。"
          onchange={(event) => onfolderchange(event.currentTarget.value)}
        >
          {#each (folders.length ? folders : ['INBOX']) as option (option)}
            <option value={option}>{option}</option>
          {/each}
        </select>

        <label for="mailbox-limit">最近</label>
        <input
          id="mailbox-limit"
          type="number"
          min="10"
          max="500"
          value={limit}
          title="每次读取最近 10–500 封邮件。"
          onchange={(event) => onlimitchange(clampMailboxLimit(Number.parseInt(event.currentTarget.value, 10)))}
        />
        <span>封</span>

        <label for="mailbox-search">搜索</label>
        <input
          id="mailbox-search"
          value={search}
          title="按标题、发件人或摘要过滤；按 Enter 刷新。"
          oninput={(event) => onsearchchange(event.currentTarget.value)}
          onkeydown={(event) => {
            if (event.key === 'Enter') onaction({ kind: 'refresh_messages', openaiOnly: false })
          }}
        />
      </div>

      <div class="body-grid">
        <div class="table-wrap">
          <table>
            <colgroup>
              <col class="kind-col" />
              <col class="date-col" />
              <col class="from-col" />
              <col class="subject-col" />
              <col class="snippet-col" />
            </colgroup>
            <thead>
              <tr>
                <th>类型</th>
                <th>时间</th>
                <th>发件人</th>
                <th>标题</th>
                <th>摘要</th>
              </tr>
            </thead>
            <tbody>
              {#each messages as message (message.id)}
                <tr
                  role="button"
                  tabindex="0"
                  aria-pressed={selectedMessageId === message.id}
                  class:selected={selectedMessageId === message.id}
                  title="单击选择邮件；双击读取完整正文。"
                  onclick={() => onselect(message.id)}
                  ondblclick={() =>
                    onaction({
                      kind: 'read_body',
                      id: message.id,
                      folder: message.folder || folder || 'INBOX',
                    })}
                  onkeydown={(event) => handleRowKeydown(event, message)}
                >
                  <td>{message.kind || ''}</td>
                  <td>{mailboxDateText(message)}</td>
                  <td title={message.from || ''}>{message.from || ''}</td>
                  <td title={message.subject || ''}>{message.subject || ''}</td>
                  <td title={message.snippet || ''}>{message.snippet || ''}</td>
                </tr>
              {/each}
            </tbody>
          </table>
          {#if messages.length === 0}
            <p class="empty">（暂无邮件）</p>
          {/if}
        </div>

        <div class="detail">
          <div class="detail-buttons">
            <button
              disabled={busy || !selectedMessage}
              title="读取左侧选中邮件完整正文。"
              onclick={readSelected}>读取正文</button
            >
            <button
              disabled={busy || !body.trim()}
              title="复制右侧当前只读正文内容。"
              onclick={() => onaction({ kind: 'copy_body' })}>复制正文</button
            >
            <button
              disabled={busy || (!selectedMessage && !body.trim())}
              title="复制选中邮件识别到的验证码；没有记录时从正文中提取。"
              onclick={() => onaction({ kind: 'copy_code', id: selectedMessage?.id ?? '' })}>复制验证码</button
            >
            <span>当前窗口只读，不会删除或移动邮件</span>
          </div>
          <textarea
            class="mono"
            readonly
            value={body}
            title="当前邮件的元数据和完整正文；此窗口不会删除或移动邮件。"
          ></textarea>
        </div>
      </div>

      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}

      <footer>
        <button
          disabled={busy}
          title="重新读取当前邮箱文件夹列表。"
          onclick={() => onaction({ kind: 'refresh_folders' })}>刷新文件夹</button
        >
        <button
          disabled={busy}
          title="读取当前文件夹最近邮件，可配合搜索框过滤。"
          onclick={() => onaction({ kind: 'refresh_messages', openaiOnly: false })}>刷新邮件</button
        >
        <button
          disabled={busy}
          title="只筛标题、发件人或摘要里包含 OpenAI 的最近邮件。"
          onclick={() => onaction({ kind: 'refresh_messages', openaiOnly: true })}>只看OpenAI</button
        >
        <button class="close" disabled={busy} title="关闭邮箱管理窗口。" onclick={onclose}>关闭</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 20;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgb(17 24 39 / 30%);
  }
  .dialog {
    width: 1120px;
    height: 720px;
    max-width: calc(100% - 24px);
    max-height: calc(100% - 24px);
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  header,
  .controls,
  .detail-buttons,
  footer {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 8px;
  }
  header {
    justify-content: space-between;
  }
  header > div {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 6px 10px;
  }
  h2,
  p {
    margin: 0;
  }
  h2 {
    font-size: 13px;
  }
  header strong {
    font-weight: 400;
    color: var(--attention);
  }
  .alias,
  .detail-buttons span {
    color: var(--muted);
  }
  .controls select {
    width: 26ch;
  }
  .controls input[type='number'] {
    width: 8ch;
  }
  .controls input:last-child {
    width: 26ch;
  }
  .body-grid {
    min-height: 0;
    flex: 1;
    display: grid;
    grid-template-columns: minmax(420px, 3fr) minmax(360px, 4fr);
    gap: 8px;
  }
  .table-wrap {
    min-width: 0;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  table {
    min-width: 965px;
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  .kind-col {
    width: 60px;
  }
  .date-col {
    width: 145px;
  }
  .from-col {
    width: 180px;
  }
  .subject-col {
    width: 260px;
  }
  .snippet-col {
    width: 320px;
  }
  th,
  td {
    height: var(--row-h);
    padding: 0 8px;
    text-align: left;
    border-bottom: 1px solid var(--border);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--head-bg);
    color: var(--head-fg);
  }
  th:first-child,
  td:first-child {
    text-align: center;
  }
  tbody tr {
    cursor: default;
  }
  tbody tr:hover {
    background: var(--head-bg);
  }
  tbody tr.selected {
    color: var(--sel-fg);
    background: var(--sel-bg);
  }
  .empty {
    padding: 12px;
    text-align: center;
    color: var(--muted);
  }
  .detail {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .detail-buttons span {
    margin-left: auto;
  }
  .detail textarea {
    min-height: 0;
    flex: 1;
    resize: none;
    white-space: pre-wrap;
  }
  .error {
    padding: 8px 10px;
    color: var(--err);
    background: #fee4e2;
    border: 1px solid #fda29b;
    border-radius: 4px;
  }
  footer .close {
    margin-left: auto;
  }
  @media (max-width: 840px) {
    .body-grid {
      grid-template-columns: 1fr;
      grid-template-rows: minmax(220px, 1fr) minmax(220px, 1fr);
    }
  }
</style>
