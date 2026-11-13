// Shapes /config rows, config writes, telemetry records and skill prompts.
export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'cfgcall', description: 'call a $ method: {fn, args}' })
    await $.command.register({ name: 'cfgdump', description: 'collector records it rewrote' })
    return next(e)
  })
  on('command.run', { command: 'cfgcall' }, async ($: any, e: any) => {
    const { fn, args } = JSON.parse(e.args)
    let f: any = $
    for (const p of fn.split('.')) f = f[p]
    try {
      const out = await f(...(args || []))
      return { text: JSON.stringify({ ok: out === undefined ? null : out }) }
    } catch (err: any) {
      return { text: JSON.stringify({ err: String((err && err.message) || err) }) }
    }
  })
  on('config.describe', { key: 'model' }, async ($: any, e: any, next: any) => next({ ...e, label: 'Model!', description: 'the model in use' }))
  on('config.describe', { key: 'permissionMode' }, async ($: any, e: any, next: any) => next({ ...e, isHidden: true }))
  on('config.set', { key: 'model' }, async () => ({ deny: 'the model stays' }))
  on('config.set', { key: 'permissionMode' }, async ($: any, e: any, next: any) => next({ ...e, value: 'plan' }))
  on('config.set', { key: 'configured.greeting' }, async () => ({ value: 'claimed' }))
  on('telemetry.*', { kind: 'bad' }, async () => ({ deny: 'no bad marks' }))
  const tagged: any[] = []
  on('command.run', { command: 'cfgdump' }, async () => ({ text: JSON.stringify(tagged) }))
  on('telemetry.log', { to: 'collector' }, async ($: any, e: any, next: any) => {
    const out = { ...e, attributes: { ...e.attributes, tagged: true } }
    tagged.push(out)
    return next(out)
  })
  on('skill.prompt', { skill: 'commit' }, async ($: any, e: any, next: any) => {
    const r = await next(e)
    return { ...r, text: r.text + ' [haiku]' }
  })
  on('skill.prompt', { skill: 'pinned' }, async ($: any, e: any, next: any) => {
    try {
      await next({ ...e, source: 'plugin' })
    } catch (err: any) {
      return { text: 'refused: ' + String((err && err.message) || err) }
    }
    return { text: 'accepted' }
  })
}
