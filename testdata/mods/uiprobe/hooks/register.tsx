// kiln QA fixture mod for the ui.* surface: /uiprobe pins a status line, logs a
// transcript row and opens a scrollable pane with a keyed Button; hooks on
// ui.press and ui.scroll log what the person's keys raised; /uiask asks a
// question in kiln's own dialog.
export function register(on: any) {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'uiprobe', description: 'pin a status line and open the ui pane' })
    await $.command.register({ name: 'uiask', description: 'ask a question with $.ui.ask' })
    return next(e)
  })

  on('command.run', { command: 'uiprobe' }, async ($: any) => {
    $.ui.status('uiprobe status: ready')
    $.ui.log('uiprobe log: opened')
    await $.ui.open({ id: 'uiprobe', title: 'uiprobe', focus: true, rows: 5 })
    return { text: 'uiprobe opened.' }
  })

  on('command.run', { command: 'uiask' }, async ($: any) => {
    const answer = await $.ui.ask('Pick one?', ['Alpha', 'Beta'])
    $.ui.log('asked: ' + answer)
    return { text: 'uiask done.' }
  })

  on('ui.press', async ($: any, e: any, next: any) => {
    $.ui.log('press hook saw ' + e.element + ' on ' + e.component)
    return next(e)
  })

  on('ui.scroll', async ($: any, e: any, next: any) => {
    $.ui.log('scroll hook saw ' + e.origin.kind + ' by ' + e.by)
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: 'uiprobe' }, async ($: any, e: any) => {
    const { Box, Text, Button } = $.ui.resolve(e)
    const rows = []
    for (let i = 0; i < 20; i++) rows.push(<Text key={'row-' + i}>uiprobe row {i}</Text>)
    return (
      <Box flexDirection="column">
        <Button key="go" label="go" onPress={() => $.ui.log('pressed go')} />
        {rows}
      </Box>
    )
  })
}
