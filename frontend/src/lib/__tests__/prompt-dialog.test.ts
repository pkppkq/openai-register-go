/*
  S26 Prompt 输入.

  `promptTitle` is a straight port of `_handle_prompt_event`'s if/elif chain
  (app.py 19004-19011), so it is checked against that chain and not against
  itself: `promptTitles` in the oracle is the output of those eight lines, run
  under CPython over every prompt_type the port can see plus the ones it cannot.

  The default arm matters more than the named ones. `prompt_type` arrives as a
  bare string on a Wails event, so a backend that grows a sixth kind reaches
  this function as an unknown — Python answers 输入验证码 and so must the port,
  because the alternative is a modal with no heading over a worker that is
  blocked and burning a browser session.

  `formatRemaining` has no Tk counterpart at all: app.py 16406 waits forever
  (`result_queue.get()` with no timeout). The countdown exists because the port
  bounds the wait at ten minutes, so what is pinned here is arithmetic and the
  clamp, not fidelity.
*/
import { describe, expect, it } from 'vitest'
import { formatRemaining, promptTitle } from '../PromptDialog.svelte'
import { oracle } from './oracle'

describe('promptTitle', () => {
  it('answers exactly as app.py 19004-19011 does for every prompt_type in the corpus', () => {
    for (const c of oracle.promptTitles) {
      expect(promptTitle(c.kind), `prompt_type ${JSON.stringify(c.kind)}`).toBe(c.title)
    }
  })

  it('covers all four arms of the Python table', () => {
    // Guards the loop above: if the corpus were ever regenerated down to a
    // single case it would still pass, and this would not.
    expect(new Set(oracle.promptTitles.map((c) => c.title)).size).toBe(4)
  })

  it('falls back to 输入验证码 for an unknown kind, as app.py 19011 does', () => {
    expect(promptTitle('some-future-kind')).toBe('输入验证码')
  })

  it('falls back for undefined, which Python never sees but the component does', () => {
    // `request?.kind` on a null request. Python's prompt_type is always a str;
    // the extra arm is the port's, and it has to land on the same default.
    expect(promptTitle(undefined)).toBe('输入验证码')
  })

  it('is case-sensitive, like the Python `==` and `in {…}` tests', () => {
    expect(promptTitle('Phone')).toBe('输入验证码')
    expect(promptTitle('phone')).toBe('输入手机号')
  })
})

describe('formatRemaining', () => {
  it('renders m:ss with a zero-padded seconds field', () => {
    expect(formatRemaining(600_000)).toBe('10:00')
    expect(formatRemaining(61_000)).toBe('1:01')
    expect(formatRemaining(9_000)).toBe('0:09')
    expect(formatRemaining(600_000 - 1_000)).toBe('9:59')
  })

  it('rounds a part-second UP, so the clock never shows 0:00 while time is left', () => {
    // Math.ceil, not round: at 1 ms remaining the request is still live, and
    // 确定 is still worth pressing.
    expect(formatRemaining(1)).toBe('0:01')
    expect(formatRemaining(999)).toBe('0:01')
    expect(formatRemaining(1_001)).toBe('0:02')
  })

  it('clamps at zero rather than counting into negatives', () => {
    // The component derives `remaining` with its own Math.max, but this is the
    // function a caller could reach directly, and a `-1:-3` on screen next to
    // 等待已超过 10 分钟 would be nonsense.
    expect(formatRemaining(0)).toBe('0:00')
    expect(formatRemaining(-1)).toBe('0:00')
    expect(formatRemaining(-600_000)).toBe('0:00')
  })

  it('does not pad the minutes field', () => {
    // m:ss, not mm:ss — the dialog reads 剩余 9:59, not 剩余 09:59.
    expect(formatRemaining(540_000)).toBe('9:00')
  })
})
