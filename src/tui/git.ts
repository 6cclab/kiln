import { exec } from "node:child_process";

/**
 * Git branch and whether the tree is dirty, for the status line.
 *
 * Shelled out rather than parsed from `.git`: a worktree, a submodule, or a
 * detached HEAD each store that differently, and `git` already knows. The cost
 * is a process spawn, which is why the caller reads this at startup and after a
 * turn rather than on every render — the status line redraws on each keystroke,
 * and spawning git at that rate would make typing feel heavy.
 */

export interface GitStatus {
	branch: string;
	dirty: boolean;
}

function run(command: string, cwd: string, timeoutMs: number): Promise<string | undefined> {
	return new Promise((resolve) => {
		exec(command, { cwd, timeout: timeoutMs, windowsHide: true }, (err, stdout) => {
			resolve(err ? undefined : stdout.trim());
		});
	});
}

export async function readGitStatus(cwd: string): Promise<GitStatus | undefined> {
	// Short timeout: on a huge or network-backed repo this can block, and a
	// status line segment is never worth stalling startup for.
	const branch = await run("git rev-parse --abbrev-ref HEAD", cwd, 2000);
	// Not a repo, or git is unavailable. Absent rather than an error - most of
	// the status line is still useful without it.
	if (!branch) return undefined;

	const porcelain = await run("git status --porcelain", cwd, 2000);
	return {
		// A detached HEAD reports "HEAD", which says nothing; the short sha does.
		branch: branch === "HEAD" ? ((await run("git rev-parse --short HEAD", cwd, 2000)) ?? "detached") : branch,
		dirty: (porcelain?.length ?? 0) > 0,
	};
}
