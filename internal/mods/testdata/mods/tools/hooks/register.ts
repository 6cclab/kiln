import { update } from 'claude-code'
import { atom } from 'claude-code'

const seen = atom({ plugin: 'tools', key: 'seen' } as const, [])

export const register = (on: any) => {
  on('tool.call', { tool: 'Bash' }, async ($: any, e: any, next: any) => {
    if (e.command.includes('rm ')) return { deny: 'no deleting' }
    if (e.command === 'date') return { result: { stdout: 'today' } }
    const r = await next({ ...e, command: e.command + ' -la' })
    await update($, seen, (list: any[]) => [...list, { command: e.command, text: r.text, isError: r.isError === true }])
    return r
  })
  on('tool.call', { tool: 'Bash' }, async ($: any, e: any, next: any) => {
    if (e.agentId === 'blocked') return { deny: 'inner deny' }
    return next(e)
  })
}
