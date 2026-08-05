<script module lang="ts">
  export type PaymentCardPoolEntry = {
    card: string
    month: string
    year: string
    cvv: string
    status: string
  }

  export type PaymentCardPoolAction = 'import' | 'reset'

  export function paymentCardEntryKey(card: PaymentCardPoolEntry): string {
    return card.card.trim()
  }

  /** Python 表格固定渲染 year/month，而导入格式仍是 card|month|year|cvv。 */
  export function paymentCardExpiry(card: PaymentCardPoolEntry): string {
    return `${card.year}/${card.month}`
  }
</script>

<script lang="ts">
  let {
    cards,
    inputText,
    busy = false,
    oninputchange,
    onaction,
  }: {
    cards: readonly PaymentCardPoolEntry[]
    inputText: string
    busy?: boolean
    oninputchange: (text: string) => void
    onaction: (action: PaymentCardPoolAction) => void
  } = $props()
</script>

<section class="card payment-card-pool-full" aria-label="支付卡池">
  <header>
    <div>
      <h2>支付卡池</h2>
      <p>每行：卡号|月|年|CVV，年可写 28 或 2028</p>
    </div>
    <span>未用 {cards.filter((card) => (card.status || '未用') === '未用').length} / 共 {cards.length}</span>
  </header>

  <div class="editor">
    <textarea
      class="mono"
      rows="3"
      spellcheck="false"
      value={inputText}
      title="输入待导入支付卡，每行格式为卡号|月|年|CVV；按卡号去重。"
      oninput={(event) => oninputchange(event.currentTarget.value)}
    ></textarea>
    <div class="buttons">
      <button
        disabled={busy || !inputText.trim()}
        title="解析输入框中的卡片，按卡号去重；替换已有卡片时保留原使用状态。"
        onclick={() => onaction('import')}>导入卡</button
      >
      <button
        disabled={busy || cards.length === 0}
        title="把所有已导入卡片状态恢复为未用，便于重新执行支付流程。"
        onclick={() => onaction('reset')}>重置卡</button
      >
    </div>
  </div>

  <p class="hint">
    每次打开支付链接会自动取一张“未用”卡，替换卡信息前三段后标记为“已用”；没有未用卡时应中止打开流程。
  </p>

  <div class="table-wrap">
    <table>
      <colgroup>
        <col class="card-col" />
        <col class="expiry-col" />
        <col class="cvv-col" />
        <col class="status-col" />
      </colgroup>
      <thead>
        <tr>
          <th>卡号</th>
          <th>有效期</th>
          <th>CVV</th>
          <th>状态</th>
        </tr>
      </thead>
      <tbody>
        {#each cards as card (paymentCardEntryKey(card))}
          <tr class:used={(card.status || '未用') !== '未用'}>
            <td class="mono" title={card.card}>{card.card}</td>
            <td title={paymentCardExpiry(card)}>{paymentCardExpiry(card)}</td>
            <td class="mono" title={card.cvv}>{card.cvv}</td>
            <td>{card.status || '未用'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if cards.length === 0}
      <p class="empty">（支付卡池为空）</p>
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
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
  }
  h2,
  p {
    margin: 0;
  }
  h2 {
    font-size: 12px;
    color: var(--muted);
  }
  header p,
  header span,
  .hint {
    color: var(--muted);
  }
  header p {
    margin-top: 3px;
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
    width: 90px;
    display: grid;
    grid-template-columns: 1fr;
    align-content: start;
    gap: 5px;
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
  .card-col {
    width: 220px;
  }
  .expiry-col {
    width: 100px;
  }
  .cvv-col,
  .status-col {
    width: 80px;
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
  tr.used {
    color: var(--muted);
  }
  .empty {
    padding: 12px;
    text-align: center;
    color: var(--muted);
  }
  @media (max-width: 600px) {
    .editor {
      flex-direction: column;
    }
    .buttons {
      width: auto;
      grid-template-columns: repeat(2, 1fr);
    }
  }
</style>
