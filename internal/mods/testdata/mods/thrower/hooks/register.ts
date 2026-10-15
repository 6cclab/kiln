export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    for (const name of ['before', 'after', 'caught', 'shape']) await $.command.register({ name, description: name })
    return next(e)
  })
  // Throws before next: skipped, the chain runs on without it.
  on('command.run', { command: 'before' }, async () => {
    throw new Error('boom')
  })
  // Throws after next: what next produced stands.
  on('command.run', { command: 'after' }, async ($: any, e: any, next: any) => {
    await next(e)
    throw new Error('late')
  })
  // Throws, and its .catch handler answers.
  on('command.run', { command: 'caught' }, async () => {
    throw new Error('guarded')
  }).catch(async ($: any, e: any, next: any) => ({ text: 'caught ' + next.error.kind + ' called=' + next.called }))
  // Returns what the site refuses.
  on('command.run', { command: 'shape' }, async () => 42)
}
