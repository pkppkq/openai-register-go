<script module lang="ts">
  /**
   * How many ZIP member names are listed before the tail is summarised.
   *
   * Higher than ConfirmAction's eight because this dialog is 760px wide against
   * that one's 460 and nothing here spends money — the list is informational
   * and the buttons below it are not a spend, so a longer list costs nothing.
   */
  export const MAX_LISTED_ENTRIES = 12

  /**
   * The member names to show, and how many the list is hiding.
   *
   * Same shape and the same reason as ConfirmAction's `listEmails`: pure and
   * exported so the slice/overflow pair is checked rather than eyeballed. The
   * overflow line quotes the FULL total, not the remainder.
   */
  export function listEntries(entries: string[]): { listed: string[]; overflow: number } {
    return {
      listed: entries.slice(0, MAX_LISTED_ENTRIES),
      overflow: Math.max(0, entries.length - MAX_LISTED_ENTRIES),
    }
  }
</script>

<script lang="ts">
  /*
    S25 导出预览 — _preview_and_save_text's modal Toplevel (app.py:24055-24095).

    Tk shows the text in an EDITABLE Text widget, and UI_SPEC row 103 records the
    bug that follows: 复制内容 copies what the user edited, while the file gets the
    original string. The spec's ruling is "fix in the port — write what the user
    sees", and this does it the other way round, which reaches the same place
    with less machinery: the box is READ-ONLY, so what the user sees is always
    what both the clipboard and the file get. There is nothing to diverge.

    Editing was never a feature anyone asked for — the dialog exists so the
    export can be CHECKED before it is written — and an editable box would also
    mean the frontend, not internal/export, decided the bytes.

    Pure presentational: everything arrives as props, both buttons are callbacks,
    nothing here imports ../wailsjs.
  */

  import type { ExportPreview } from './api'

  let {
    preview,
    busy = false,
    oncancel,
    oncopy,
    onsave,
  }: {
    /** The built document, or null when the dialog is closed. */
    preview: ExportPreview | null
    /** A save or copy is in flight; both buttons are held. */
    busy?: boolean
    oncancel: () => void
    oncopy: () => void
    onsave: () => void
  } = $props()

  let cancelButton = $state<HTMLButtonElement | null>(null)

  // Focus the safe answer, as ConfirmAction does. An export writes a file that
  // can hold refresh tokens, so Enter-on-arrival should close, not write.
  $effect(() => {
    if (preview && cancelButton) cancelButton.focus()
  })

  let summary = $derived(listEntries(preview?.entries ?? []))
  /** ZIP has no Tk preview at all (app.py:24386 goes straight to the dialog). */
  let isArchive = $derived(!!preview && preview.text === '' && preview.entries.length > 0)
</script>

<svelte:window
  onkeydown={(e) => {
    if (preview && e.key === 'Escape') oncancel()
  }}
/>

{#if preview}
  <div class="modal-backdrop">
    <div class="modal" role="dialog" aria-modal="true" aria-label={preview.title}>
      <h2>{preview.title}</h2>

      <!-- app.py:24060, verbatim. -->
      <p class="hint">请先核对导出内容，可复制；点击“确定导出”后选择保存文件。</p>

      <p class="count">
        共 {preview.count} 条{#if preview.suggestedName}
          · 建议文件名 <span class="mono">{preview.suggestedName}</span>{/if}
      </p>

      {#if preview.skippedNote}
        <p class="skipped">{preview.skippedNote}</p>
      {/if}

      {#if isArchive}
        <p class="hint">
          ZIP 不做文本预览（Tk 版同样直接进入保存对话框），下面是压缩包内的文件名：
        </p>
        <ul class="entries mono">
          {#each summary.listed as entry (entry)}
            <li>{entry}</li>
          {/each}
          {#if summary.overflow > 0}
            <li class="muted">…等共 {preview.entries.length} 个</li>
          {/if}
        </ul>
      {:else}
        <!-- Read-only on purpose; see the block comment above. -->
        <label class="sr-only" for="export-preview-text">导出内容预览</label>
        <textarea id="export-preview-text" class="mono" readonly value={preview.text}></textarea>
      {/if}

      <footer>
        <button
          disabled={busy || isArchive}
          title={isArchive ? 'ZIP 是二进制内容，没有可复制的文本' : '把上面的内容复制到剪贴板（去掉结尾换行，与 Tk 一致）'}
          onclick={oncopy}>复制内容</button
        >
        <span class="spacer"></span>
        <button bind:this={cancelButton} disabled={busy} title="不导出，关闭本窗口。（Esc）" onclick={oncancel}
          >取消</button
        >
        <button class="primary" disabled={busy} title="选择保存位置并写入文件" onclick={onsave}>确定导出</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgb(17 24 39 / 45%);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 10;
  }
  .modal {
    /* S25: 760x520. */
    width: 760px;
    max-width: calc(100% - 32px);
    max-height: calc(100% - 32px);
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  h2 {
    margin: 0;
    font-size: 13px;
    color: var(--text);
  }
  p {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .hint,
  .count {
    color: var(--muted);
  }
  .skipped {
    color: var(--manual);
  }
  textarea {
    /* S25: h24 rows. */
    height: 340px;
    resize: vertical;
    white-space: pre;
    overflow: auto;
    padding: 8px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--surface);
    color: var(--text);
  }
  .entries {
    max-height: 340px;
    overflow: auto;
    margin: 0;
    list-style: none;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--surface);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .entries li {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  footer {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 4px;
  }
  /* 复制内容 sits on the left, 取消 / 确定导出 on the right (app.py:24089-24094). */
  .spacer {
    flex: 1;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>
