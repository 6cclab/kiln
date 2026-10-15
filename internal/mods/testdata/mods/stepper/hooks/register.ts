import { atom, update } from 'claude-code'

const steps = atom({ plugin: 'stepper', key: 'steps' } as const, [])

export const register = (on: any) => {
  on('turn.step', async function* ($: any, e: any, next: any) {
    const before = { model: e.model, effort: e.effort, agentId: e.agentId }
    const result = yield* next({ ...e, effort: 'low' })
    await update($, steps, (list: any[]) => [
      ...list,
      { before, answer: result.answer, tools: result.toolUses.map((t: any) => t.name), out: result.usage && result.usage.output_tokens, model: result.usage && result.usage.model, stop: result.stopReason },
    ])
    return result
  })
}
