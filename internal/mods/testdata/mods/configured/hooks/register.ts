let seen: any

export const register = (on: any, options: any) => {
  seen = options
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'options', description: 'show options' })
    return next(e)
  })
  on('command.run', { command: 'options' }, async () => ({ text: JSON.stringify(seen) }))
}
