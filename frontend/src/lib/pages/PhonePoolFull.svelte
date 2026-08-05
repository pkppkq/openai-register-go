<script module lang="ts">
  export type PhonePoolEntry = {
    number: string
    smsUrl?: string
    receiveCount: number
    status: string
    lastCode?: string
    lastError?: string
  }

  export type PhonePoolAction = 'import' | 'reset' | 'clear' | 'manual_code'

  export function phoneEntryKey(phone: PhonePoolEntry): string {
    return phone.number.trim()
  }

  /**
   * 达到次数上限时只派生显示“冻结”，不改写传入对象；Python 版在绘制时修改模型
   * 是已确认的数据层副作用，这里明确修复。
   */
  export function displayPhoneStatus(phone: PhonePoolEntry, maxReceiveCount: number): string {
    const limit = Math.max(0, Math.trunc(maxReceiveCount || 0))
    if (
      limit > 0 &&
      Math.max(0, Math.trunc(phone.receiveCount || 0)) >= limit &&
      phone.status !== '不可用' &&
      phone.status !== '冻结'
    ) {
      return '冻结'
    }
    return phone.status || '可用'
  }
</script>

<script lang="ts">
  let {
    phones,
    inputText,
    maxReceiveCount,
    selectedKey = '',
    busy = false,
    oninputchange,
    onmaxreceivechange,
    onselect,
    onaction,
  }: {
    phones: readonly PhonePoolEntry[]
    inputText: string
    maxReceiveCount: number
    selectedKey?: string
    busy?: boolean
    oninputchange: (text: string) => void
    onmaxreceivechange: (count: number) => void
    onselect: (key: string) => void
    onaction: (action: PhonePoolAction, selectedKey: string) => void
  } = $props()

  function clampMax(raw: string): number {
    const parsed = Number.parseInt(raw, 10)
    return Number.isFinite(parsed) ? Math.max(0, parsed) : 0
  }
</script>

<section class="card phone-pool-full" aria-label="手机号池">
  <header>
    <div>
      <h2>手机号池</h2>
      <p>每行：+手机号https://短信链接 或 +手机号----https://短信链接</p>
    </div>
    <label for="phone-max-receive">每个手机号最多接码次数（0=不限制）</label>
    <input
      id="phone-max-receive"
      type="number"
      min="0"
      value={maxReceiveCount}
      title="达到此接码次数后号码显示为冻结；0 表示不限制。"
      onchange={(event) => onmaxreceivechange(clampMax(event.currentTarget.value))}
    />
  </header>

  <div class="editor">
    <textarea
      class="mono"
      rows="3"
      spellcheck="false"
      value={inputText}
      title="输入待导入手机号；支持 +手机号https://短信链接 或 +手机号----https://短信链接。"
      oninput={(event) => oninputchange(event.currentTarget.value)}
    ></textarea>
    <div class="buttons">
      <button
        disabled={busy}
        title="解析输入框内容，按手机号去重并导入；已有号码会更新接码链接。"
        onclick={() => onaction('import', selectedKey)}>导入手机号</button
      >
      <button
        disabled={busy || phones.length === 0}
        title="把所有手机号恢复为可用，清空最近错误和接码次数。"
        onclick={() => onaction('reset', selectedKey)}>重置手机号</button
      >
      <button
        class="danger"
        disabled={busy || phones.length === 0}
        title="确认后清空全部手机号；任务运行中不可清空。"
        onclick={() => onaction('clear', selectedKey)}>清空手机号</button
      >
      <button
        disabled={busy || !selectedKey}
        title={selectedKey
          ? '轮询当前选中手机号已保存的接码链接，最多等待 30 秒；不会租用新号码。'
          : '请先在下方表格选择一个手机号。'}
        onclick={() => onaction('manual_code', selectedKey)}>手动取码</button
      >
    </div>
  </div>

  <div class="table-wrap">
    <table>
      <colgroup>
        <col class="pick-col" />
        <col class="phone-col" />
        <col class="count-col" />
        <col class="status-col" />
        <col class="code-col" />
      </colgroup>
      <thead>
        <tr>
          <th aria-label="选择"></th>
          <th>手机号</th>
          <th>接码次数</th>
          <th>状态</th>
          <th>最近验证码</th>
        </tr>
      </thead>
      <tbody>
        {#each phones as phone (phoneEntryKey(phone))}
          {@const key = phoneEntryKey(phone)}
          {@const status = displayPhoneStatus(phone, maxReceiveCount)}
          <tr class:selected={selectedKey === key}>
            <td class="pick">
              <input
                type="radio"
                name="phone-pool-selection"
                checked={selectedKey === key}
                aria-label={`选择手机号 ${phone.number}`}
                title={`选择 ${phone.number}，用于手动取码。`}
                onchange={() => onselect(key)}
              />
            </td>
            <td title={phone.smsUrl ? `${phone.number}\n${phone.smsUrl}` : phone.number}>{phone.number}</td>
            <td class="num">{Math.max(0, Math.trunc(phone.receiveCount || 0))}</td>
            <td title={phone.lastError || status}>{status}</td>
            <td class="mono" title={phone.lastCode || ''}>{phone.lastCode || ''}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if phones.length === 0}
      <p class="empty">（手机号池为空）</p>
    {/if}
  </div>
</section>

<style>
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  header {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 8px;
  }
  header > div {
    margin-right: auto;
  }
  h2,
  p {
    margin: 0;
  }
  h2 {
    font-size: 12px;
    color: var(--muted);
  }
  header p {
    margin-top: 3px;
    color: var(--muted);
  }
  header input {
    width: 10ch;
  }
  .editor {
    display: flex;
    align-items: stretch;
    gap: 8px;
  }
  .editor textarea {
    min-width: 0;
    flex: 1;
    resize: vertical;
  }
  .buttons {
    width: 104px;
    display: grid;
    grid-template-columns: 1fr;
    gap: 5px;
  }
  .buttons button {
    padding-inline: 6px;
  }
  .table-wrap {
    min-height: calc(4 * var(--row-h));
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }
  .pick-col {
    width: 36px;
  }
  .phone-col {
    width: 180px;
  }
  .count-col {
    width: 80px;
  }
  .status-col,
  .code-col {
    width: 120px;
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
    background: var(--head-bg);
    color: var(--head-fg);
  }
  td.pick,
  th:first-child {
    padding: 0;
    text-align: center;
  }
  td.num,
  th:nth-child(3) {
    text-align: center;
  }
  tr.selected {
    color: var(--sel-fg);
    background: var(--sel-bg);
  }
  .empty {
    padding: 12px;
    text-align: center;
    color: var(--muted);
  }
  @media (max-width: 640px) {
    .editor {
      flex-direction: column;
    }
    .buttons {
      width: auto;
      grid-template-columns: repeat(2, 1fr);
    }
  }
</style>
