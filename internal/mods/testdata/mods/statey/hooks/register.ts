import { atom, read, update } from 'claude-code'

const shown = atom({ plugin: 'statey', key: 'shown' } as const, 0)
const hidden = atom({ plugin: 'statey', key: 'hidden' } as const, 0)

export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    for (const name of ['bump', 'same', 'hidden', 'theirs']) await $.command.register({ name, description: name })
    return next(e)
  })
  on('command.run', { command: 'bump' }, async ($: any) => ({ text: String(await update($, shown, (n: number) => n + 1)) }))
  on('command.run', { command: 'same' }, async ($: any) => ({ text: String(await update($, shown, (n: number) => n)) }))
  on('command.run', { command: 'hidden' }, async ($: any) => ({ text: String(await update($, hidden, (n: number) => n + 1)) }))
  on('command.run', { command: 'theirs' }, async ($: any) => {
    try {
      await $.state.set({ plugin: 'other', key: 'x', value: 1 })
      return { text: 'wrote' }
    } catch (err: any) {
      return { text: String(err.message) }
    }
  })
  on('ui.render', { component: 'Pane', requestId: 'statey' }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    const n = await read($, shown)
    let refused = ''
    try {
      await $.state.set({ plugin: 'statey', key: 'shown', value: 99 })
    } catch (err: any) {
      refused = ' (write refused)'
    }
    return Text({ children: 'shown ' + n + refused })
  })
}
