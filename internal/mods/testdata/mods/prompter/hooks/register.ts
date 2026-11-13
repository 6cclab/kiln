// Rewrites and gates what the person types, mentions and is told.
export const register = (on: any) => {
  on('prompt.submit', async ($: any, e: any, next: any) => {
    if (e.text.includes('forbidden')) return { drop: 'no forbidden words' }
    let extra = ''
    if (e.attachments && e.attachments[0]) {
      const m = await $.prompt.media({ id: e.attachments[0].id })
      extra = 'media:' + (m.mediaType || m.deny || m.unreadable) + ':' + m.size
    }
    return next({ ...e, text: e.text.toUpperCase(), context: [...(e.context ?? []), 'ctx:' + extra] })
  })
  on('prompt.suggest', async ($: any, e: any, next: any) => {
    if (e.text.includes('secret')) return { isShown: false }
    return next({ ...e, text: e.text.trim() })
  })
  on('prompt.fill', async ($: any, e: any, next: any) => next({ ...e, text: e.text.trim() }))
  on('prompt.autocomplete', { token: /^#/ }, async () => ({ suggestions: [{ text: '#123', label: 'issue 123', description: 'a bug' }, { text: '' }] }))
  on('prompt.mention', { path: /\.env$/ }, async () => ({ deny: 'a secret' }))
  on('prompt.mention', { mention: 'redirect.txt' }, async ($: any, e: any, next: any) => {
    const r = await next({ ...e, path: e.path.replace('redirect.txt', 'real.txt') })
    return { ...r, context: [...(r.context ?? []), 'mention-context'] }
  })
  on('prompt.attachment', { type: 'plan_mode' }, async ($: any, e: any, next: any) => next({ ...e, text: e.text + '!' }))
  on('prompt.attachment', { type: 'drop_me' }, async () => ({ text: null }))
  on('prompt.section', { name: 'memory' }, async () => ({ text: null }))
  on('prompt.section', { name: 'base' }, async ($: any, e: any, next: any) => {
    const r = await next(e)
    return { text: r.text + ' [S]' }
  })
  on('prompt.compose', async ($: any, e: any, next: any) => {
    const r = await next(e)
    return { sections: [...r.sections, { id: 'prompter:extra', text: 'EXTRA ' + e.model, scope: 'session' }] }
  })
}
