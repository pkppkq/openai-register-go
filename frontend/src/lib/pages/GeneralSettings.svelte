<script module lang="ts">
  /** app.py 72 `AUDIO_DEFAULT_DEVICE_LABEL`; `settings.AudioDefaultDeviceLabel`. */
  export const AUDIO_DEFAULT_DEVICE_LABEL = '系统默认'

  /** The three settings keys S18 owns (UI_SPEC §3). */
  export type SoundSettings = {
    /** `success_sound_enabled` — default ON. */
    successSound: boolean
    /** `pause_others_on_link_success` — default ON. */
    pauseOthers: boolean
    /** `success_audio_device` — a `刷新设备` label, or 系统默认. */
    audioDevice: string
  }

  /** app.py 12391–12393: the Var seeds for a fresh state file. */
  export const SOUND_DEFAULTS: SoundSettings = {
    successSound: true,
    pauseOthers: true,
    audioDevice: AUDIO_DEFAULT_DEVICE_LABEL,
  }
</script>

<script lang="ts">
  /*
    S18 设置 — ported from app.py 13644–13656 (Tk tab title `提示音`, sidebar
    entry `设置`). The nav key `settings` maps onto `sound_frame` (app.py 14021),
    so this pane IS the 提示音 tab and nothing else; it is short because the Tk
    screen is short. Labels and button captions are verbatim; `title=` strings
    are the Tk tooltips (UI_SPEC §0.7).

    SCOPE. §6's slice-1 table does not list S18. It is built here because all
    three controls are plain persisted settings with a real SaveSettings behind
    them, and `长链成功后暂停其他账户` in particular changes what a running batch
    does. Note that persisting it is only HALF of §2 action 105: when it is on,
    a link success is also supposed to set the global stop signal and cancel
    every other in-flight account (`账号 X 长链已提取，已暂停其他账户继续尝试`).
    That half lives in the task registry, not here.

    `刷新设备` 与 `测试提示音` 由壳层直接使用 WebAudio：枚举 audiooutput，
    并播放 0.42 秒的 880/1320 Hz 测试音。WebView 不支持 setSinkId 时会明确
    回退到系统默认输出，不需要新增 Go 绑定。

    Pure presentational: data in through props, changes out through callbacks,
    nothing imported from ../../wailsjs.
  */

  let {
    value,
    busy = false,
    devices,
    canrefreshdevices = false,
    cantestsound = false,
    onchange,
    onrefreshdevices,
    ontestsound,
  }: {
    /** Current settings; the parent owns them. */
    value: SoundSettings
    /** Disables every control before settings have loaded. */
    busy?: boolean
    /**
     * The enumerated output devices, `刷新设备`'s list: `系统默认` first, then
     * `{index}: {name} / {hostapi}` per device (app.py 13270). Omitted while
     * WebAudio 不可用时可省略；组件会保留当前已保存标签作为回退。
     */
    devices?: readonly string[]
    /** Whether WebView supports `enumerateDevices`. */
    canrefreshdevices?: boolean
    /** Whether WebView supports WebAudio tone playback. */
    cantestsound?: boolean
    onchange: (next: SoundSettings) => void
    /** `刷新设备` — app.py 13253 `_refresh_audio_devices`, logs `已刷新音频输出设备: N 个`. */
    onrefreshdevices: () => void
    /** `测试提示音` — app.py 13656 `_play_success_sound_async(force=True)`. */
    ontestsound: () => void
  } = $props()

  function patch(part: Partial<SoundSettings>) {
    onchange({ ...value, ...part })
  }

  /**
   * The combo's option list. With `devices` supplied this is exactly it; without
   * it, 系统默认 plus whatever is currently saved — app.py's own fallback rule
   * (13275–13280) is "keep the previous selection if it is still in the list,
   * else 系统默认", and here nothing can prove a saved device is gone, so it
   * stays selectable rather than being coerced away.
   */
  let options = $derived.by(() => {
    if (devices !== undefined) return [...devices]
    const saved = value.audioDevice.trim()
    return saved === '' || saved === AUDIO_DEFAULT_DEVICE_LABEL
      ? [AUDIO_DEFAULT_DEVICE_LABEL]
      : [AUDIO_DEFAULT_DEVICE_LABEL, saved]
  })

  /** Mirrors app.py 14202 / snapshot.go:297 — a blank label reads as 系统默认. */
  let selectedDevice = $derived(value.audioDevice.trim() || AUDIO_DEFAULT_DEVICE_LABEL)

  const DEVICES_UNBOUND_HINT =
    '当前宿主没有提供 WebAudio 设备枚举或测试音能力；仍保留「系统默认」和状态文件中已保存的设备标签。'
</script>

<section class="card">
  <h2>提示音</h2>

  <div class="row">
    <label class="check">
      <input
        type="checkbox"
        disabled={busy}
        checked={value.successSound}
        onchange={(e) => patch({ successSound: e.currentTarget.checked })}
      />
      成功提示音
    </label>

    <label class="check">
      <input
        type="checkbox"
        disabled={busy}
        checked={value.pauseOthers}
        onchange={(e) => patch({ pauseOthers: e.currentTarget.checked })}
      />
      长链成功后暂停其他账户
    </label>
  </div>

  <div class="row">
    <label for="success-audio-device">输出设备</label>
    <!-- app.py 13652: Combobox(state="readonly", width=72) packed fill=X
         expand=True — 72ch is the basis, not a minimum. -->
    <select
      id="success-audio-device"
      class="grow72"
      disabled={busy}
      title={devices === undefined ? DEVICES_UNBOUND_HINT : ''}
      value={selectedDevice}
      onchange={(e) => patch({ audioDevice: e.currentTarget.value })}
    >
      {#each options as option (option)}
        <option value={option}>{option}</option>
      {/each}
    </select>

    <button
      disabled={busy || !canrefreshdevices}
      title={canrefreshdevices ? '重新扫描系统可用的音频输出设备，并更新下拉列表。' : DEVICES_UNBOUND_HINT}
      onclick={onrefreshdevices}>刷新设备</button
    >
    <button
      disabled={busy || !cantestsound}
      title={cantestsound ? '立即播放一次成功提示音，用于确认当前输出设备可用。' : DEVICES_UNBOUND_HINT}
      onclick={ontestsound}>测试提示音</button
    >
  </div>

  {#if devices === undefined}
    <p class="hint">{DEVICES_UNBOUND_HINT}</p>
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
    gap: 10px;
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
    align-items: center;
    gap: 6px 8px;
  }
  .row label {
    color: var(--text);
  }
  .row label + select {
    margin-right: 8px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-right: 8px;
  }
  /* pack(fill=X, expand=True) on a width=72 widget. */
  .grow72 {
    flex: 1 1 72ch;
    min-width: 0;
  }
  .hint {
    margin: 0;
    color: var(--muted);
    overflow-wrap: anywhere;
  }
</style>
