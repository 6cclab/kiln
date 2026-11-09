export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'meta', description: 'session meta' })
    return next(e)
  })
  on('command.run', { command: 'meta' }, async ($: any) => {
    const version = await $.session.version()
    const repo = await $.session.repo()
    const turns = await $.session.turns()
    // repo is SessionRepo | null; forward it (or the string "NULL") verbatim.
    return { text: JSON.stringify({ version: version.version, repo: repo === null ? 'NULL' : repo, turns }) }
  })
}
