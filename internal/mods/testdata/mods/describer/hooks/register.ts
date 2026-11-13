// Rewrites how kiln's tools, commands and agents present, and what it permits.
export const register = (on: any) => {
  on('tool.describe', async ($: any, e: any, next: any) => {
    const r = await next(e)
    return { ...r, description: r.description + ' [described]' }
  })
  on('command.describe', async ($: any, e: any, next: any) => {
    const r = await next(e)
    return { ...r, description: 'D: ' + r.description, argumentHint: '<hint>' }
  })
  on('agent.offer', { agent: 'secret' }, async () => ({ isOffered: false }))
  on('tool.check', { tool: 'Bash' }, async ($: any, e: any, next: any) => {
    if (JSON.stringify(e.input).includes('rm -rf')) return { decision: 'deny', reason: 'no rm -rf here' }
    return next(e)
  })
}
