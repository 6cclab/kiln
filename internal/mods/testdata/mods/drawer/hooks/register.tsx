import { atom, read, update } from 'claude-code'

const presses = atom({ plugin: 'drawer', key: 'presses' } as const, 0)
const big = atom({ plugin: 'drawer', key: 'big' } as const, false)

const Label = (props: { name: string; children?: any }) => {
  // A mod's own component: a plain function the factory calls.
  return props.children
}

export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'big', description: 'draw too much text' })
    return next(e)
  })
  on('command.run', { command: 'big' }, async ($: any) => {
    await update($, big, () => true)
    return { text: 'ok' }
  })
  on('ui.render', { component: 'Pane', requestId: 'drawer' }, async ($: any, e: any) => {
    const { Box, Text, Button, Raster } = $.ui.resolve(e)
    const n = await read($, presses)
    if (await read($, big)) return <Text>{'x'.repeat(90_000)}<Text>{'y'.repeat(60_000)}</Text></Text>
    const cells = new Uint8Array([1, 2, 3, 250]).toBase64()
    const utf8 = new TextDecoder().decode(new TextEncoder().encode('héllo'))
    return (
      <Box key="root" flexDirection="column" paddingLeft={2}>
        <Text bold color="#ffffff">
          Hi {e.props.title}, {n} presses
        </Text>
        {[1, 2].map(i => (
          <Text key={'row-' + i}>row {i}</Text>
        ))}
        {false}
        {null}
        <Label name="x">{btoa('ab')}</Label>
        <Button key="go" label="go" onPress={() => update($, presses, (v: number) => v + 1)} />
        <Raster columns={2} rows={1} cells={cells} />
        <>{utf8}</>
        <Text>{e.props.bodyColumns}x{e.props.scroll.bodyRows} {String(e.props.view.agentId)}</Text>
      </Box>
    )
  })
}
