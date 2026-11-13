// Drives $.process.spawn, $.mcp.connect and $.plugin: each command takes JSON
// and answers JSON.
const readAll = async (it: any, stopAfter?: number) => {
  const out: any = { stdout: '', stderr: '', pieces: 0 }
  for (;;) {
    const r = await it.next()
    if (r.done) { out.result = r.value; return out }
    out.pieces++
    out[r.value.stream] += r.value.text
    if (stopAfter && out.pieces >= stopAfter) {
      await it.return()
      out.left = true
      return out
    }
  }
}

export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    for (const name of ['spawn', 'spawnleft', 'spawnbg', 'connect', 'plug']) {
      await $.command.register({ name, description: name })
    }
    return next(e)
  })
  const json = (v: any) => ({ text: JSON.stringify(v) })
  on('command.run', { command: 'spawn' }, async ($: any, e: any) => {
    try {
      return json({ ok: await readAll($.process.spawn(JSON.parse(e.args))) })
    } catch (err: any) {
      return json({ err: String((err && err.message) || err) })
    }
  })
  // leaves the loop after the first piece: the child must be killed
  on('command.run', { command: 'spawnleft' }, async ($: any, e: any) => {
    return json({ ok: await readAll($.process.spawn(JSON.parse(e.args)), 1) })
  })
  // a loop that outlives the hook: the child lives until the engine closes
  on('command.run', { command: 'spawnbg' }, async ($: any, e: any) => {
    const it = $.process.spawn(JSON.parse(e.args))
    const first = await it.next()
    void (async () => { for (;;) { const r = await it.next(); if (r.done) return } })()
    return json({ ok: first.value })
  })
  on('command.run', { command: 'connect' }, async ($: any, e: any) => {
    try {
      return json({ ok: await $.mcp.connect(e.args) })
    } catch (err: any) {
      return json({ err: String((err && err.message) || err) })
    }
  })
  on('command.run', { command: 'plug' }, async ($: any) => {
    return json({ name: $.plugin.name, root: $.plugin.root })
  })
}
