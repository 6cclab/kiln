// Takes every press of the Button keyed "go": it answers without next(e), so
// the element's own onPress never runs.
export const register = (on: any) => {
  on('ui.press', { element: 'go' }, async () => ({ element: 'taken' }))
}
