// Adds the noun $.voice at engine.create and uses it from a command.
const facts: any = {}
export const register = (on: any) => {
  on('engine.create', async ($: any, e: any, next: any) => {
    const built = await next(e)
    facts.keysOfDollar = Object.keys($).length
    facts.plugins = e.plugins
    facts.builtHasUi = typeof built.ui === 'object'
    return { ...built, voice: { say: (t: string) => 'said ' + t } }
  })
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'say', description: 'say via $.voice' })
    await $.command.register({ name: 'facts', description: 'what the fold saw' })
    return next(e)
  })
  on('command.run', { command: 'say' }, async ($: any, e: any) => ({ text: $.voice.say(e.args) }))
  on('command.run', { command: 'facts' }, async () => ({ text: JSON.stringify(facts) }))
}
