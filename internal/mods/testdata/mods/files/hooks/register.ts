export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'probe', description: 'read session id and files' })
    return next(e)
  })
  // Exercises $.session.id and the $.fs read API against the session cwd, the
  // calls squad makes every few seconds to render its offload panel.
  on('command.run', { command: 'probe' }, async ($: any) => {
    const id = await $.session.id()
    const existsHere = await $.fs.exists('a.txt')
    const existsNot = await $.fs.exists('nope.txt')
    const entries = await $.fs.list('.')
    const names = entries
      .map((x: any) => `${x.name}:${x.kind}:${x.isLink}`)
      .sort()
      .join(',')
    const body = await $.fs.read('a.txt')
    return { text: JSON.stringify({ id, existsHere, existsNot, names, body }) }
  })
}
