export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'label', description: 'classify text' })
    return next(e)
  })
  on('command.run', { command: 'label' }, async ($: any, e: any) => {
    // e.args carries the text to classify; "none" exercises the null return.
    const picked = await $.model.classify(e.args || 'ship it', ['bug', 'feature', 'chore'])
    return { text: JSON.stringify({ picked: picked === null ? 'NULL' : picked }) }
  })
}
