import { atom, update } from 'claude-code'

const ticks = atom({ plugin: 'ticker', key: 'ticks' } as const, 0)
const once = atom({ plugin: 'ticker', key: 'once' } as const, 0)
let timer: any

export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'stop', description: 'stop ticking' })
    timer = $.clock.every(1000, () => update($, ticks, (n: number) => n + 1))
    $.clock.after(500, () => update($, once, (n: number) => n + 1))
    return next(e)
  })
  on('command.run', { command: 'stop' }, async ($: any) => {
    timer.cancel()
    return { text: 'stopped at ' + (await $.clock.now()) }
  })
}
