// Restyles the Text every other plugin draws a command's output with, and
// leaves Text out of the table the others read for an InfoNotice (a fragment
// there). Its own drawing reads the plain table: its own hook is skipped.
export const register = (on: any) => {
  on('ui.resolve', { component: 'CommandOutput' }, async ($: any, e: any, next: any) => {
    const t = await next(e)
    const Plain = t.Text
    return { ...t, Text: (p: any) => <Plain>[restyled] {p.children}</Plain> }
  })
  on('ui.resolve', { component: 'InfoNotice' }, async ($: any, e: any, next: any) => {
    const t = await next(e)
    return { ...t, Text: undefined }
  })
  on('ui.render', { component: 'CommandOutput', requestId: 'own' }, async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return <Text>own row</Text>
  })
}
