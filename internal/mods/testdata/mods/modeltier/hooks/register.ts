export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'complete', description: 'one-shot completion' })
    await $.command.register({ name: 'fork', description: 'fork the main thread' })
    return next(e)
  })
  on('command.run', { command: 'complete' }, async ($: any, e: any) => {
    const r = await $.model.complete({ model: '', prompt: e.args || 'hello', system: 'be terse', maxTokens: 50 })
    return { text: JSON.stringify(r) }
  })
  on('command.run', { command: 'fork' }, async ($: any, e: any) => {
    const r = await $.model.fork({ prompt: e.args || 'continue' })
    return { text: JSON.stringify(r) }
  })
}
