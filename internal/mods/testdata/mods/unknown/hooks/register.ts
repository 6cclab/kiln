export const register = (on: any) => {
  // An event kiln does not raise: registers, never runs.
  on('prompt.submit', async ($: any, e: any, next: any) => next(e))
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'fs', description: 'read a file' })
    // Not awaited and not caught: must not take anything down.
    void $.http.fetch('https://example.com')
    return next(e)
  })
  on('command.run', { command: 'fs' }, async ($: any) => {
    try {
      await $.fs.read('notes.md')
      return { text: 'read it' }
    } catch (err: any) {
      return { text: String(err.message) }
    }
  })
}
