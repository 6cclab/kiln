import { DatabaseSync } from "node:sqlite";
import { mkdir, readdir, readFile, stat } from "node:fs/promises";
import { dirname, join } from "node:path";
import { homedir } from "node:os";
import type {
	EntrySearchHit,
	SearchQuery,
	SessionSearchHit,
	SessionSearchService,
} from "@earendil-works/pi-agent-core";

/**
 * Session search over SQLite FTS5.
 *
 * pi defines `SessionSearchService` as an interface and ships no implementation
 * — `packages/agent/src/search/index.ts` is 636 bytes of pure types — so the
 * contract exists and the backing store is ours.
 *
 * Storage: `node:sqlite`, built into Node 24, with FTS5 and `snippet()`
 * confirmed available (SQLite 3.50.4). No dependency needed.
 *
 * The design constraint that shapes everything here: **recall must cost tens of
 * tokens, not thousands.** A search that returns whole entries defeats its own
 * purpose on a 32k window — it would spend more context than simply keeping the
 * history would have. So the index stores snippets and returns snippets.
 */

const DEFAULT_DB = join(homedir(), ".harness", "search.db");
/**
 * Indexed by default: the harness's own sessions AND existing Claude Code
 * history. Both are JSONL with the same message shape (they differ only in
 * `uuid` vs `id` and string vs numeric timestamps, both handled), so prior work
 * is recallable from the first run rather than only after the harness has
 * accumulated a history of its own.
 */
const DEFAULT_SESSIONS = [join(homedir(), ".harness", "sessions"), join(homedir(), ".claude", "projects")];

/** Snippet width in tokens, roughly. Keeps a hit affordable on a small window. */
const SNIPPET_TOKENS = 12;

/**
 * Normalize a timestamp to epoch milliseconds.
 *
 * pi writes a number; Claude Code writes an ISO-8601 string. Running `Number()`
 * over the latter yields NaN, which SQLite stores happily and which then sorts
 * and displays as garbage - a silent corruption rather than an error.
 */
function toEpochMs(value: unknown): number {
	if (typeof value === "number" && Number.isFinite(value)) return value;
	if (typeof value === "string") {
		const parsed = Date.parse(value);
		if (Number.isFinite(parsed)) return parsed;
	}
	return 0;
}

export interface SqliteSearchOptions {
	dbPath?: string;
	/** Roots to index. Defaults to the harness store plus Claude Code history. */
	sessionsDir?: string | string[];
}

interface IndexedEntry {
	sessionId: string;
	entryId: string;
	timestamp: number;
	role: string;
	text: string;
}

export class SqliteSessionSearch implements SessionSearchService {
	private db: DatabaseSync;
	private sessionsDirs: string[];
	private dirty = new Set<string>();

	constructor(opts: SqliteSearchOptions = {}) {
		const dbPath = opts.dbPath ?? DEFAULT_DB;
		const dirs = opts.sessionsDir ?? DEFAULT_SESSIONS;
		this.sessionsDirs = Array.isArray(dirs) ? dirs : [dirs];
		this.db = new DatabaseSync(dbPath);
		this.migrate();
	}

	static async create(opts: SqliteSearchOptions = {}): Promise<SqliteSessionSearch> {
		await mkdir(dirname(opts.dbPath ?? DEFAULT_DB), { recursive: true });
		return new SqliteSessionSearch(opts);
	}

	private migrate(): void {
		// WAL: indexing runs while a session is being written. Without it a sync
		// would block the agent loop's own appends.
		this.db.exec("PRAGMA journal_mode = WAL");

		// `content=''` makes this a contentless FTS table: the text lives only in
		// the index, never duplicated. Session JSONL remains the source of truth.
		this.db.exec(`
			CREATE VIRTUAL TABLE IF NOT EXISTS entries USING fts5(
				session_id UNINDEXED,
				entry_id UNINDEXED,
				timestamp UNINDEXED,
				role UNINDEXED,
				text,
				tokenize = 'porter unicode61'
			)
		`);

		// Tracks which session files have been indexed and at what size, so a sync
		// re-reads only what grew. Re-parsing every file on every sync would make
		// search cost scale with total history rather than with new content.
		this.db.exec(`
			CREATE TABLE IF NOT EXISTS indexed_files (
				path TEXT PRIMARY KEY,
				size INTEGER NOT NULL,
				mtime INTEGER NOT NULL,
				session_id TEXT NOT NULL
			)
		`);
	}

	/** Pull readable text out of an entry, ignoring structure we cannot render. */
	private extractText(entry: Record<string, unknown>): { role: string; text: string } | undefined {
		const message = entry.message as { role?: string; content?: unknown } | undefined;
		if (!message?.role) return undefined;

		const content = message.content;
		if (typeof content === "string") return { role: message.role, text: content };
		if (!Array.isArray(content)) return undefined;

		const text = content
			.filter((b): b is { type: string; text: string } => (b as { type?: string })?.type === "text")
			.map((b) => b.text)
			.join("\n")
			.trim();

		return text ? { role: message.role, text } : undefined;
	}

	private async findSessionFiles(): Promise<string[]> {
		const found: string[] = [];
		const walk = async (dir: string): Promise<void> => {
			let entries;
			try {
				entries = await readdir(dir, { withFileTypes: true });
			} catch {
				return;
			}
			for (const e of entries) {
				const full = join(dir, e.name);
				if (e.isDirectory()) await walk(full);
				else if (e.name.endsWith(".jsonl")) found.push(full);
			}
		};
		for (const dir of this.sessionsDirs) await walk(dir);
		return found;
	}

	/**
	 * Index any session file that is new or has grown.
	 *
	 * Size+mtime is a cheap change check. JSONL sessions are append-only, so a
	 * file that has not grown cannot have new entries — no hashing required.
	 */
	async sync(): Promise<void> {
		const files = await this.findSessionFiles();
		const known = new Map<string, { size: number; mtime: number }>();
		for (const row of this.db.prepare("SELECT path, size, mtime FROM indexed_files").all() as Array<{
			path: string;
			size: number;
			mtime: number;
		}>) {
			known.set(row.path, { size: row.size, mtime: row.mtime });
		}

		for (const file of files) {
			const info = await stat(file).catch(() => undefined);
			if (!info) continue;

			const prior = known.get(file);
			if (prior && prior.size === info.size && prior.mtime === Math.floor(info.mtimeMs)) continue;

			await this.indexFile(file, info.size, Math.floor(info.mtimeMs));
		}
		this.dirty.clear();
	}

	private async indexFile(path: string, size: number, mtime: number): Promise<void> {
		let raw: string;
		try {
			raw = await readFile(path, "utf8");
		} catch {
			return;
		}

		const sessionId = path.split("/").pop()?.replace(/\.jsonl$/, "") ?? path;

		// Re-index the whole file rather than tracking byte offsets. Sessions are
		// small and this avoids an entire class of partial-line bugs at the cost
		// of re-parsing a file that changed.
		this.db.prepare("DELETE FROM entries WHERE session_id = ?").run(sessionId);

		const insert = this.db.prepare(
			"INSERT INTO entries(session_id, entry_id, timestamp, role, text) VALUES (?, ?, ?, ?, ?)",
		);

		for (const line of raw.split("\n")) {
			if (!line.trim()) continue;
			let entry: Record<string, unknown>;
			try {
				entry = JSON.parse(line) as Record<string, unknown>;
			} catch {
				// A truncated final line is normal while a session is being written.
				continue;
			}
			const extracted = this.extractText(entry);
			if (!extracted) continue;

			insert.run(
				sessionId,
				// pi uses `id`; Claude Code uses `uuid`. Indexing both formats means
				// the harness can search existing Claude Code history, not just its own.
				String(entry.uuid ?? entry.id ?? entry.entryId ?? ""),
				toEpochMs(entry.timestamp ?? entry.ts),
				extracted.role,
				extracted.text,
			);
		}

		this.db
			.prepare("INSERT INTO indexed_files(path, size, mtime, session_id) VALUES (?, ?, ?, ?) ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime")
			.run(path, size, mtime, sessionId);
	}

	/**
	 * FTS5 MATCH syntax is a query language, and user text is not.
	 * An apostrophe or a bare `AND` would otherwise be a syntax error rather
	 * than a search. Quoting each term makes every input a literal phrase.
	 */
	private toMatchQuery(text: string): string {
		const terms = text
			.toLowerCase()
			.split(/[^\p{L}\p{N}_-]+/u)
			.filter((t) => t.length > 1);
		if (terms.length === 0) return "";
		return terms.map((t) => `"${t.replace(/"/g, "")}"`).join(" OR ");
	}

	async searchEntries(query: SearchQuery): Promise<EntrySearchHit[]> {
		const match = this.toMatchQuery(query.text);
		if (!match) return [];

		const rows = this.db
			.prepare(
				`SELECT session_id, entry_id, timestamp,
				        snippet(entries, 4, char(171), char(187), char(8230), ${SNIPPET_TOKENS}) AS snip,
				        rank
				 FROM entries WHERE entries MATCH ? ORDER BY rank LIMIT ?`,
			)
			.all(match, query.limit ?? 10) as Array<{
			session_id: string;
			entry_id: string;
			timestamp: number;
			snip: string;
			rank: number;
		}>;

		return rows.map((r) => ({
			sessionId: r.session_id,
			entryId: r.entry_id,
			timestamp: r.timestamp,
			snippet: r.snip,
			// bm25 returns lower-is-better; invert so callers can treat score as
			// "higher is more relevant" without knowing the backend.
			score: -r.rank,
		}));
	}

	async searchSessions(query: SearchQuery): Promise<SessionSearchHit[]> {
		const entries = await this.searchEntries({ ...query, limit: (query.limit ?? 10) * 4 });

		// Best hit per session: a session matching twenty times should appear once,
		// not twenty times, or one verbose session crowds out everything else.
		const best = new Map<string, EntrySearchHit>();
		for (const hit of entries) {
			const current = best.get(hit.sessionId);
			if (!current || (hit.score ?? 0) > (current.score ?? 0)) best.set(hit.sessionId, hit);
		}

		return [...best.values()]
			.sort((a, b) => (b.score ?? 0) - (a.score ?? 0))
			.slice(0, query.limit ?? 10)
			.map((hit) => ({
				sessionId: hit.sessionId,
				score: hit.score,
				top: { entryId: hit.entryId, snippet: hit.snippet, timestamp: hit.timestamp },
			}));
	}

	/** Mark a session changed. The next `sync()` picks it up. */
	notify(sessionId: string): void {
		this.dirty.add(sessionId);
	}

	async remove(sessionId: string): Promise<void> {
		this.db.prepare("DELETE FROM entries WHERE session_id = ?").run(sessionId);
		this.db.prepare("DELETE FROM indexed_files WHERE session_id = ?").run(sessionId);
	}

	async close(): Promise<void> {
		this.db.close();
	}
}
