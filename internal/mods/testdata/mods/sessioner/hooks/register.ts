// Rewrites what comes into and goes out of the session: session.receive
// (tags a delivery, consumes one that says "mute") and session.send (shouts
// the text, refuses a send to "blocked").
export const register = (on: any) => {
  on('session.receive', async ($: any, e: any, next: any) => {
    if (e.text.includes('mute')) return { consumed: 'muted by sessioner' }
    return next({ ...e, text: e.text + ' [seen]' })
  })
  on('session.send', async ($: any, e: any, next: any) => {
    if (e.to === 'blocked') return { isDelivered: false, reason: 'sessioner blocks this recipient' }
    return next({ ...e, text: e.text.toUpperCase() })
  })
}
