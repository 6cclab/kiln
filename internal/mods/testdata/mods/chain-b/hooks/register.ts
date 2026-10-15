export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'chain', description: 'chain test' })
    return next(e)
  })
  on('command.run', { command: 'chain' }, async ($: any, e: any) => ({ text: 'B:' + e.args }))
  on('agent.spawn', async ($: any, e: any, next: any) => {
    const started = await next({ ...e, prompt: e.prompt + ' B', model: 'haiku' })
    return started
  })
}
