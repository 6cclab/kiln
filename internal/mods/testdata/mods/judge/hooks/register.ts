// Judges every module admitted after it: refuses the one named "victim".
const seen: any[] = []
export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'dump', description: 'what plugin.register saw' })
    return next(e)
  })
  on('plugin.register', async ($: any, e: any, next: any) => {
    seen.push(e)
    if (e.name === 'victim') return { refuse: 'managed only' }
    return next(e)
  })
  on('command.run', { command: 'dump' }, async () => ({ text: JSON.stringify(seen) }))
}
