// The outer of two mods: it rewrites the event on the way down and wraps
// the result on the way up.
export const register = (on: any) => {
  on('command.run', { command: 'chain' }, async ($: any, e: any, next: any) => {
    const r = await next({ ...e, args: e.args + '+a' })
    return { text: 'A(' + r.text + ')' }
  })
  // tool is pinned: a rewrite of it is refused, so this hook fails.
  on('tool.call', { tool: 'Pinned' }, async ($: any, e: any, next: any) => next({ ...e, tool: 'Elsewhere' }))
  // Leaving a pinned field out keeps it.
  on('tool.call', { tool: 'Kept' }, async ($: any, e: any, next: any) => {
    const { tool, ...rest } = e
    return next({ ...rest, extra: 1 })
  })
  on('agent.spawn', async ($: any, e: any, next: any) => next({ ...e, prompt: e.prompt + ' A' }))
}
