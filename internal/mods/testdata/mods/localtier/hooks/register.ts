const ENV = 'KILN_MODS_TEST_ENV_SET'

export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'local', description: 'exercise the local-tier $ methods' })
    return next(e)
  })
  on('command.run', { command: 'local' }, async ($: any) => {
    await $.fs.write('out/w.txt', 'written')
    const read = await $.fs.read('out/w.txt')
    const file = await $.fs.stat('out/w.txt')
    const dir = await $.fs.stat('out')
    const link = await $.fs.stat('lnk')
    const resolved = await $.fs.stat('lnk', { resolve: true })
    const anc = await $.fs.ancestors({ names: ['MARK.md', 'nope.md'], of: 'out' })
    await $.store.set('a', 1)
    await $.store.set('b', { x: [1, 2] })
    await $.store.set('c', 'three')
    const keys1 = await $.store.keys()
    const b = await $.store.get('b')
    await $.store.set('a', 10)
    await $.store.delete('b')
    const keys2 = await $.store.keys()
    const a = await $.store.get('a')
    const gone = (await $.store.get('b')) === undefined
    await $.env.set(ENV, 'v1')
    const envSet = await $.env.get(ENV)
    await $.env.set(ENV)
    const envUnset = (await $.env.get(ENV)) === undefined
    return { text: JSON.stringify({ read, file, dir, link, resolved, anc, keys1, b, keys2, a, gone, envSet, envUnset }) }
  })
}
