export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'fetch', description: 'fetch a url' })
    return next(e)
  })
  on('command.run', { command: 'fetch' }, async ($: any) => {
    const url = await $.env.get('KILN_MODS_TEST_URL')
    const res = await $.http.fetch(url, { method: 'POST', headers: { 'X-Mod': 'kiln' }, body: 'ping' })
    // headers must be keyed lowercase; read one back to prove it.
    return { text: JSON.stringify({ status: res.status, ok: res.ok, ctype: res.headers['content-type'], body: res.text }) }
  })
}
