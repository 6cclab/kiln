// Records the events it is told to watch and lets a test call any $ method.
const seen: any[] = []
const EVENTS = [
  'session.end', 'session.compact', 'session.append', 'turn.abort',
  'tool.call', 'tool.check', 'agent.spawn', 'agent.register', 'agent.offer',
]

export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'call', description: 'call a $ method: {fn, args}' })
    await $.command.register({ name: 'dump', description: 'what the recorder saw' })
    return next(e)
  })
  for (const ev of EVENTS) {
    on(ev, async ($: any, e: any, next: any) => {
      seen.push({ event: ev, e })
      return next(e)
    })
  }
  on('turn.step', async function* ($: any, e: any, next: any) {
    const chunks: any[] = []
    const it = next(e)
    let r = await it.next()
    while (!r.done) {
      chunks.push(r.value)
      yield r.value
      r = await it.next()
    }
    seen.push({ event: 'turn.step', chunks, answer: r.value && r.value.answer })
    return r.value
  })
  on('command.run', { command: 'dump' }, async () => ({ text: JSON.stringify(seen) }))
  on('command.run', { command: 'call' }, async ($: any, e: any) => {
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
}
