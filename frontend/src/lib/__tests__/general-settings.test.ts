/*
  S18 设置 — the three sound keys.

  Three constants, and the reason they are worth a test is that two of them
  default ON. `pause_others_on_link_success` in particular is not a
  notification preference: UI_SPEC row 105 records that when it is on, one
  account's link success sets the GLOBAL stop signal and cancels every other
  in-flight account. Flipping its seed to false would change what a fresh
  install DOES, not how it sounds, so the seed is pinned against app.py
  12391-12393 rather than left to look obvious.
*/
import { describe, expect, it } from 'vitest'
import { AUDIO_DEFAULT_DEVICE_LABEL, SOUND_DEFAULTS } from '../pages/GeneralSettings.svelte'
import { oracle } from './oracle'

describe('AUDIO_DEFAULT_DEVICE_LABEL', () => {
  it('is app.py 72 verbatim', () => {
    expect(AUDIO_DEFAULT_DEVICE_LABEL).toBe(oracle.constants.AUDIO_DEFAULT_DEVICE_LABEL)
  })

  it('is a sentinel label, not a device id', () => {
    // `settings.AudioDefaultDeviceLabel` is compared as a string against the
    // 刷新设备 list; the sentinel has to survive a save/load round trip intact.
    expect(AUDIO_DEFAULT_DEVICE_LABEL).toBe('系统默认')
    expect(AUDIO_DEFAULT_DEVICE_LABEL.trim()).toBe(AUDIO_DEFAULT_DEVICE_LABEL)
  })
})

describe('SOUND_DEFAULTS', () => {
  it('are the app.py 12391-12393 Var seeds', () => {
    expect(SOUND_DEFAULTS).toEqual({
      successSound: oracle.constants.SOUND_SEEDS.success_sound_enabled,
      pauseOthers: oracle.constants.SOUND_SEEDS.pause_others_on_link_success,
      audioDevice: oracle.constants.SOUND_SEEDS.success_audio_device,
    })
  })

  it('default both toggles ON, which is what app.py does', () => {
    expect(SOUND_DEFAULTS.successSound).toBe(true)
    // Not cosmetic: on, a link success cancels every other in-flight account
    // (UI_SPEC row 105). A fresh install behaves that way in Tk and must here.
    expect(SOUND_DEFAULTS.pauseOthers).toBe(true)
  })

  it('default the device to the sentinel rather than to an empty string', () => {
    // An empty value would render as a blank combo and would not match any
    // entry in the 刷新设备 list.
    expect(SOUND_DEFAULTS.audioDevice).toBe(AUDIO_DEFAULT_DEVICE_LABEL)
  })

  it('covers exactly the three keys S18 owns', () => {
    expect(Object.keys(SOUND_DEFAULTS).sort()).toEqual(['audioDevice', 'pauseOthers', 'successSound'])
  })
})
