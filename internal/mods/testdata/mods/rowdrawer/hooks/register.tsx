// Draws a slash command's output row in its own words.
export const register = (on: any) => {
  on('ui.render', { component: 'CommandOutput', props: { command: 'status' } }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return <Text>mod row for {e.props.command}: {e.props.text} ({e.requestId})</Text>
  })
}
