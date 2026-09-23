import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { homedir } from "node:os";
import type {
	AuthOperationOptions,
	Credential,
	CredentialInfo,
	CredentialStore,
} from "@earendil-works/pi-ai";

/**
 * File-backed credential store.
 *
 * pi ships only `InMemoryCredentialStore`, which is correct for a library and
 * useless for a CLI: an OAuth login would have to be repeated every launch.
 *
 * Two properties the contract demands and that are easy to get wrong:
 *
 *  - **Serialized writes per provider.** OAuth refresh is read-modify-write. Two
 *    concurrent refreshes racing on the same provider can persist a stale
 *    refresh token, which locks you out until you log in again. Writes go
 *    through a per-provider promise chain.
 *  - **Atomic replacement.** A crash mid-write must not leave a truncated file;
 *    that would take out every provider's credentials at once, not just the one
 *    being written. Write to a temp file, then rename.
 */

const DEFAULT_PATH = join(homedir(), ".harness", "credentials.json");

type Stored = Record<string, Credential>;

export class FileCredentialStore implements CredentialStore {
	private path: string;
	private chains: Map<string, Promise<unknown>>;

	constructor(path: string = DEFAULT_PATH) {
		this.path = path;
		this.chains = new Map();
	}

	private async load(): Promise<Stored> {
		try {
			return JSON.parse(await readFile(this.path, "utf8")) as Stored;
		} catch (err) {
			// Absent is the normal first-run state. Anything else is real and
			// must not be silently turned into "no credentials" - that would
			// present as a spurious logout and invite the user to re-auth over
			// a file that is merely unreadable.
			if ((err as NodeJS.ErrnoException).code === "ENOENT") return {};
			throw err;
		}
	}

	private async save(data: Stored): Promise<void> {
		await mkdir(dirname(this.path), { recursive: true });
		const tmp = `${this.path}.${process.pid}.tmp`;
		// 0600: these are bearer tokens for paid accounts.
		await writeFile(tmp, JSON.stringify(data, null, 2), { mode: 0o600 });
		await rename(tmp, this.path);
	}

	/** Queue work on this provider's chain so writes never interleave. */
	private enqueue<T>(providerId: string, fn: () => Promise<T>): Promise<T> {
		const prior = this.chains.get(providerId) ?? Promise.resolve();
		// Swallow the predecessor's rejection: one failed write must not poison
		// every subsequent write for that provider.
		const next = prior.then(fn, fn);
		this.chains.set(
			providerId,
			next.catch(() => {}),
		);
		return next;
	}

	async read(providerId: string, _options?: AuthOperationOptions): Promise<Credential | undefined> {
		return (await this.load())[providerId];
	}

	async list(_options?: AuthOperationOptions): Promise<readonly CredentialInfo[]> {
		const data = await this.load();
		// Metadata only - the contract forbids exposing secrets here.
		return Object.entries(data).map(([providerId, c]) => ({ providerId, type: c.type }));
	}

	async modify(
		providerId: string,
		fn: (current: Credential | undefined) => Promise<Credential | undefined>,
		_options?: AuthOperationOptions,
	): Promise<Credential | undefined> {
		return this.enqueue(providerId, async () => {
			const data = await this.load();
			const next = await fn(data[providerId]);
			// undefined means "leave unchanged", NOT "delete" - deletion is
			// `delete()`. Conflating them would drop a credential whenever a
			// refresh decided it had nothing new to store.
			if (next === undefined) return data[providerId];
			data[providerId] = next;
			await this.save(data);
			return next;
		});
	}

	async delete(providerId: string, _options?: AuthOperationOptions): Promise<void> {
		await this.enqueue(providerId, async () => {
			const data = await this.load();
			if (!(providerId in data)) return;
			delete data[providerId];
			await this.save(data);
		});
	}
}
