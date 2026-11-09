export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'facts', description: 'session facts' })
    return next(e)
  })
  on('command.run', { command: 'facts' }, async ($: any) => {
    const root = await $.session.root()
    const model = await $.session.model()
    return { text: JSON.stringify({ root, model }) }
  })
}
