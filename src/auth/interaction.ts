import { createInterface } from "node:readline/promises";
import { stdin, stdout } from "node:process";
import type { AuthEvent, AuthInteraction, AuthPrompt } from "@earendil-works/pi-ai";

/**
 * Terminal implementation of pi's `AuthInteraction`.
 *
 * This is the whole app-side cost of subscription auth. pi owns the protocols -
 * PKCE, device-code, token exchange and refresh live in
 * `pi-ai/auth/oauth/{anthropic,openai-codex,github-copilot,...}.ts`. The harness
 * only has to render four prompt kinds and four event kinds.
 *
 * Replaced by a pi-tui version in Phase 6; this one keeps `harness login`
 * working headlessly and over SSH, which is where a homelab tool usually lives.
 */

export interface TerminalAuthOptions {
	/** Print the auth URL instead of opening a browser. Default true over SSH. */
	printOnly?: boolean;
	signal?: AbortSignal;
}

function isHeadless(): boolean {
	return Boolean(process.env.SSH_TTY || process.env.SSH_CONNECTION) || !stdout.isTTY;
}

/** Best-effort browser open. Never throws: the URL is always printed as well. */
async function openBrowser(url: string): Promise<void> {
	const cmd =
		process.platform === "darwin" ? "open" : process.platform === "win32" ? "start" : "xdg-open";
	try {
		const { spawn } = await import("node:child_process");
		spawn(cmd, [url], { stdio: "ignore", detached: true }).unref();
	} catch {
		// Printed below regardless; a missing opener is not a failure.
	}
}

export function createTerminalAuthInteraction(opts: TerminalAuthOptions = {}): AuthInteraction {
	const printOnly = opts.printOnly ?? isHeadless();

	return {
		signal: opts.signal,

		async prompt(prompt: AuthPrompt): Promise<string> {
			const rl = createInterface({ input: stdin, output: stdout });
			try {
				if (prompt.type === "select") {
					console.log(`\n${prompt.message}`);
					prompt.options.forEach((o, i) => {
						console.log(`  ${i + 1}) ${o.label}${o.description ? ` - ${o.description}` : ""}`);
					});
					// Loop rather than reject: a typo during a login flow should
					// not discard a half-finished OAuth handshake.
					for (;;) {
						const raw = (await rl.question("> ", { signal: combine(opts.signal, prompt.signal) })).trim();
						const byIndex = prompt.options[Number(raw) - 1];
						if (byIndex) return byIndex.id;
						const byId = prompt.options.find((o) => o.id === raw || o.label === raw);
						if (byId) return byId.id;
						console.log(`  enter 1-${prompt.options.length}`);
					}
				}

				const suffix = prompt.placeholder ? ` [${prompt.placeholder}]` : "";
				const answer = await rl.question(`${prompt.message}${suffix}: `, {
					signal: combine(opts.signal, prompt.signal),
				});
				// Blank means "accept the placeholder"; providers read the
				// placeholder as their default, so pass the empty string through.
				return answer;
			} finally {
				rl.close();
			}
		},

		notify(event: AuthEvent): void {
			switch (event.type) {
				case "info":
					console.log(event.message);
					for (const link of event.links ?? []) {
						console.log(`  ${link.label ? `${link.label}: ` : ""}${link.url}`);
					}
					return;

				case "auth_url":
					console.log(`\n${event.instructions ?? "Open this URL to authorize:"}`);
					console.log(`\n  ${event.url}\n`);
					if (!printOnly) void openBrowser(event.url);
					return;

				case "device_code":
					// Both halves matter and users miss one of them constantly,
					// so give the code its own line rather than burying it.
					console.log(`\nGo to: ${event.verificationUri}`);
					console.log(`Enter code: ${event.userCode}\n`);
					if (event.expiresInSeconds) {
						console.log(`(expires in ${Math.round(event.expiresInSeconds / 60)} min)`);
					}
					if (!printOnly) void openBrowser(event.verificationUri);
					return;

				case "progress":
					console.log(`... ${event.message}`);
					return;
			}
		},
	};
}

/**
 * Merge the flow-level and per-prompt abort signals.
 *
 * pi races a `manual_code` prompt against its callback server and aborts the
 * prompt when the callback wins; without honoring `prompt.signal` the terminal
 * would sit waiting for input after login had already succeeded.
 */
function combine(a: AbortSignal | undefined, b: AbortSignal | undefined): AbortSignal | undefined {
	if (!a) return b;
	if (!b) return a;
	return AbortSignal.any([a, b]);
}
