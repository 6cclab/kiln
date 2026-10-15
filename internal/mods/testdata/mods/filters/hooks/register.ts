export const register = (on: any) => {
  on('tool.call', { tool: 'Bash' }, async () => ({ deny: 'no bash' }))
  on('tool.call', { tool: ['Read', 'Grep'] }, async ($: any, e: any) => ({ result: 'listed ' + e.tool }))
  on('tool.call', { tool: /^mcp__x__/ }, async () => ({ result: 'regex' }))
  on('tool.call', { tool: 'Read', agentId: 'sub' }, async () => ({ result: 'never: the list hook answers first' }))
  on('ui.render', { component: 'Pane', requestId: 'p1' }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'one' })
  })
  on('ui.render', { component: 'AbovePrompt' }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'band' })
  })
}
