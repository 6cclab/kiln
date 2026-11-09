export const register = (on: any) => {
  on('session.start', async ($: any, e: any, next: any) => {
    await $.command.register({ name: 'run', description: 'run a subprocess' })
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
}
