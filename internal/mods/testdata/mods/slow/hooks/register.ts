import { atom, update } from 'claude-code'

const after = atom({ plugin: 'slow', key: 'after' } as const, '')

export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    for (const name of ['hang', 'spin']) await $.command.register({ name, description: name })
    return next(e)
  })
  // Never settles: its own time runs out.
  on('command.run', { command: 'hang' }, async () => new Promise(() => {}))
  // Never yields the worker: the watchdog interrupts it. The invalidate tells
  // the test the loop has started.
  on('command.run', { command: 'spin' }, ($: any) => {
    $.ui.invalidate()
    for (;;) {}
  })
  // Waits on next far longer than its budget: that wait is not its own time.
  on('tool.call', { tool: 'Slow' }, async ($: any, e: any, next: any) => {
    const r = await next(e)
    await update($, after, () => 'saw ' + r.text)
    return r
  })
}
