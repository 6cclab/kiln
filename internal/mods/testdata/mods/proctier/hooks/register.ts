export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'run', description: 'run a subprocess' })
    await $.command.register({ name: 'mark', description: 'run a subprocess that leaves a file' })
    return next(e)
  })
  on('command.run', { command: 'run' }, async ($: any) => {
    // stdout, stderr, a non-zero exit and stdin all in one subprocess.
    const res = await $.process.run(
      ['sh', '-c', 'cat; printf oops >&2; exit 3'],
      { stdin: 'hello', env: { MOD_MARK: 'kiln' } },
    )
    return { text: JSON.stringify({ exitCode: res.exitCode, stdout: res.stdout, stderr: res.stderr }) }
  })
  on('command.run', { command: 'mark' }, async ($: any) => {
    // Leaves "ran" in the session's cwd if it runs; a refusal comes back as text.
    try {
      await $.process.run(['sh', '-c', 'echo x > ran'])
      return { text: 'ran' }
    } catch (err: any) {
      return { text: 'refused: ' + String((err && err.message) || err) }
    }
  })
}
