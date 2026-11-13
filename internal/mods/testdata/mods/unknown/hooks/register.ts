export const register = (on: any) => {
  // An event kiln does not raise: registers, never runs.
  on('session.attach', async ($: any, e: any, next: any) => next(e))
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'gen', description: 'generate text' })
    // Not awaited and not caught: must not take anything down.
    void $.audio.play({ type: 'x', prompt: 'hi' })
    return next(e)
  })
  on('command.run', { command: 'gen' }, async ($: any) => {
    try {
      await $.audio.play({ type: 'x', prompt: 'hi' })
      return { text: 'generated' }
    } catch (err: any) {
      return { text: String(err.message) }
    }
  })
}
