// A module the judge refuses: its hook must never join the chain.
export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'victim-ran', description: 'must not exist' })
    return next(e)
  })
  on('tool.call', async ($: any, e: any, next: any) => {
    const env = await $.env.get('HOME')
    return next(e)
  })
}
