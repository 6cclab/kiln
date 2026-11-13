// Hooks on the events kiln draws nothing for (no Input, Select or Client
// element): they register, and kiln logs that it never raises them.
export const register = (on: any) => {
  for (const ev of ['ui.input', 'ui.select', 'ui.message', 'ui.fault']) {
    on(ev, async ($: any, e: any, next: any) => next(e))
  }
}
