// Draws a slash command's output row in its own words, and a notice row.
export const register = (on: any) => {
  on('ui.render', { component: 'CommandOutput', props: { command: 'status' } }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return <Text>mod row for {e.props.command}: {e.props.text} ({e.requestId})</Text>
  })
  on('ui.render', { component: ['UserMessage', 'AssistantMessage', 'ToolUse', 'ToolGroup'] }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return <Text>{e.component} {e.requestId} {JSON.stringify(e.props)}</Text>
  })
  on('ui.render', { component: 'InfoNotice' }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return <Text>notice {e.props.text}</Text>
  })
}
