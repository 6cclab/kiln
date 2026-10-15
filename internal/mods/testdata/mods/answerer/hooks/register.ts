// Sits beneath the failing hooks and answers every command it is asked,
// counting the calls per command so a test can tell a kept result from a re-run.
const calls: Record<string, number> = {}

export const register = (on: any) => {
  on('command.run', async ($: any, e: any) => {
    calls[e.command] = (calls[e.command] ?? 0) + 1
    return { text: 'answered ' + e.command + ' #' + calls[e.command] }
  })
}
