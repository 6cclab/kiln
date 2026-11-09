export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'call', description: 'call an mcp tool' })
    return next(e)
  })
  on('command.run', { command: 'call' }, async ($: any) => {
    const res = await $.mcp.call('srv', 'echo', { msg: 'hi' })
    // McpToolResult: { content: [{type,text}], isError, structuredContent? }
    return { text: JSON.stringify({ text: res.content[0].text, isError: res.isError }) }
  })
}
