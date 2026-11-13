// An engine.create step that replaces a noun of core's table: it unloads.
export const register = (on: any) => {
  on('engine.create', async ($: any, e: any, next: any) => {
    const built = await next(e)
    return { ...built, ui: { open: () => 'mine' } }
  })
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'replacer-ran', description: 'must not exist' })
    return next(e)
  })
}
