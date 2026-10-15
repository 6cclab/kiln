// kiln e2e fixture mod: a command that opens a pane, a tool that reports
// what the mod saw (Claude Code tool names, which conversation called it,
// subagents spawned), a pane and a band drawn from $.state.
import { atom, read, update } from 'claude-code'

const seen = atom({ plugin: 'probe', key: 'seen' } as const, [] as string[])
const notes = atom({ plugin: 'probe', key: 'notes' } as const, [] as string[])

export function register(on: any) {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'probe', description: 'open the probe pane' })
    await $.tool.register({
      name: 'step',
      description: 'report a step to the probe pane',
      inputSchema: { type: 'object', properties: { note: { type: 'string' } }, required: ['note'] },
    })
    return next(e)
  })

  on('command.run', { command: 'probe' }, async ($: any) => {
    await $.ui.open({ id: 'probe', title: 'probe' })
    return { text: 'Probe pane opened.' }
  })

  on('agent.spawn', async ($: any, e: any, next: any) => {
    await update($, seen, (s: string[]) => [...s, 'spawn:' + e.subagentType])
    return next({ ...e, prompt: e.prompt + '\n\n(probe: report with mcp__probe__step)' })
  })

  on('tool.call', { tool: 'mcp__probe__step' }, async ($: any, e: any) => {
    const who = e.agentId ? 'sub' : 'main'
    await update($, notes, (n: string[]) => [...n, who + ':' + e.note])
    const s = await read($, seen)
    return { result: 'probe saw [' + s.join(',') + '] from ' + who }
  })

  on('tool.call', async ($: any, e: any, next: any) => {
    if (e.tool !== 'mcp__probe__step') await update($, seen, (s: string[]) => [...s, e.tool])
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: 'probe' }, async ($: any, e: any) => {
    const { Box, Text } = $.ui.resolve(e)
    const n = await read($, notes)
    return (
      <Box flexDirection="column">
        <Text bold>probe notes: {n.length ? n.join(', ') : 'none'}</Text>
      </Box>
    )
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    const n = await read($, notes)
    return <Text>probe band · {n.length} notes</Text>
  })
}
