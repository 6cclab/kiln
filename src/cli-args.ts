/**
 * Command-line parsing.
 *
 * Replaces scattered `process.argv.indexOf("--flag")` lookups. Those worked for
 * the space form and silently ignored `--flag=value`, which is the form people
 * reach for first and the form that appears in scripts — a flag that is quietly
 * dropped is worse than one that errors.
 *
 * The flag names and semantics come from `claude --help`, so muscle memory
 * carries over: someone who types `claude -c` should get the same result from
 * `harness -c`.
 */

export interface ParsedArgs {
	/** The first non-flag argument, when it names a subcommand. */
	command?: string;
	/** Positional arguments after the subcommand. */
	positional: string[];

	print?: boolean;
	printPrompt?: string;
	outputFormat?: "text" | "json" | "stream-json";

	continueLatest?: boolean;
	resume?: string | true;
	sessionId?: string;
	forkSession?: boolean;
	name?: string;

	model?: string;
	effort?: "low" | "medium" | "high" | "xhigh" | "max";
	permissionMode?: string;

	addDir: string[];
	allowedTools: string[];
	disallowedTools: string[];

	settings?: string;
	settingSources?: string[];
	mcpConfig?: string;
	strictMcpConfig?: boolean;

	systemPrompt?: string;
	appendSystemPrompt?: string;

	screenReader?: boolean;
	verbose?: boolean;
	version?: boolean;
	help?: boolean;

	/** Flags that look like flags but are not recognized. */
	unknown: string[];
}

/** Flags that take a value. Everything else is boolean. */
const VALUED = new Set([
	"--output-format",
	"--resume",
	"-r",
	"--session-id",
	"--name",
	"-n",
	"--model",
	"--effort",
	"--permission-mode",
	"--add-dir",
	"--allowed-tools",
	"--disallowed-tools",
	"--settings",
	"--setting-sources",
	"--mcp-config",
	"--system-prompt",
	"--append-system-prompt",
]);

/** Subcommands, so `harness login anthropic` is not read as a prompt. */
const COMMANDS = new Set(["providers", "models", "login", "logout", "doctor", "mcp"]);

/**
 * `"Bash(git *) Edit"` — space-separated, but parentheses may contain spaces.
 *
 * Splitting naively on whitespace turns one rule into two, and `Bash(git` is a
 * rule that matches nothing. Depth tracking keeps the rule intact.
 */
export function splitToolList(value: string): string[] {
	const out: string[] = [];
	let current = "";
	let depth = 0;
	for (const ch of value) {
		if (ch === "(") depth++;
		else if (ch === ")") depth = Math.max(0, depth - 1);
		if (/\s/.test(ch) && depth === 0) {
			if (current) out.push(current);
			current = "";
			continue;
		}
		current += ch;
	}
	if (current) out.push(current);
	// Also accept the comma form, which people use interchangeably.
	return out.flatMap((r) => (r.includes(",") && !r.includes("(") ? r.split(",") : [r])).filter(Boolean);
}

export function parseArgs(argv: string[]): ParsedArgs {
	const args: ParsedArgs = { positional: [], addDir: [], allowedTools: [], disallowedTools: [], unknown: [] };

	for (let i = 0; i < argv.length; i++) {
		const token = argv[i];

		if (!token.startsWith("-")) {
			if (args.command === undefined && args.positional.length === 0 && COMMANDS.has(token)) {
				args.command = token;
			} else {
				args.positional.push(token);
			}
			continue;
		}

		// `--flag=value` and `--flag value` are the same thing.
		const eq = token.indexOf("=");
		const flag = eq === -1 ? token : token.slice(0, eq);
		let value = eq === -1 ? undefined : token.slice(eq + 1);
		if (value === undefined && VALUED.has(flag)) {
			const next = argv[i + 1];
			// `--resume` with no id is valid (resume the latest), so a following
			// flag must not be swallowed as its value.
			if (next !== undefined && !next.startsWith("-")) {
				value = next;
				i++;
			}
		}

		switch (flag) {
			case "-p":
			case "--print":
				args.print = true;
				break;
			case "--output-format":
				if (value === "json" || value === "stream-json" || value === "text") args.outputFormat = value;
				break;
			case "-c":
			case "--continue":
				args.continueLatest = true;
				break;
			case "-r":
			case "--resume":
				args.resume = value ?? true;
				break;
			case "--session-id":
				args.sessionId = value;
				break;
			case "--fork-session":
				args.forkSession = true;
				break;
			case "-n":
			case "--name":
				args.name = value;
				break;
			case "--model":
				args.model = value;
				break;
			case "--effort":
				if (value === "low" || value === "medium" || value === "high" || value === "xhigh" || value === "max") {
					args.effort = value;
				}
				break;
			case "--permission-mode":
				args.permissionMode = value;
				break;
			case "--add-dir":
				// Repeatable, and `claude --help` documents it as variadic.
				if (value) args.addDir.push(...value.split(/[\s,]+/).filter(Boolean));
				break;
			case "--allowed-tools":
			case "--allowedTools":
				if (value) args.allowedTools.push(...splitToolList(value));
				break;
			case "--disallowed-tools":
			case "--disallowedTools":
				if (value) args.disallowedTools.push(...splitToolList(value));
				break;
			case "--settings":
				args.settings = value;
				break;
			case "--setting-sources":
				args.settingSources = value?.split(",").map((s) => s.trim()).filter(Boolean);
				break;
			case "--mcp-config":
				args.mcpConfig = value;
				break;
			case "--strict-mcp-config":
				args.strictMcpConfig = true;
				break;
			case "--system-prompt":
				args.systemPrompt = value;
				break;
			case "--append-system-prompt":
				args.appendSystemPrompt = value;
				break;
			case "--ax-screen-reader":
				args.screenReader = true;
				break;
			case "--verbose":
				args.verbose = true;
				break;
			case "-v":
			case "--version":
				args.version = true;
				break;
			case "-h":
			case "--help":
				args.help = true;
				break;
			default:
				// Collected rather than ignored: a mistyped flag that silently does
				// nothing is how a script ends up not doing what it says.
				args.unknown.push(flag);
		}
	}

	// In print mode the prompt is whatever positional text is left.
	if (args.print && args.positional.length > 0) args.printPrompt = args.positional.join(" ");

	return args;
}

export const HELP = `harness - a coding agent with Claude Code's interface, on any model

usage:
  harness [options]                  start an interactive session
  harness -p "prompt" [options]      run one prompt and exit
  harness <command> [args]

commands:
  providers                          list providers and auth status
  models                             list models, tiers and budgets
  login <provider>                   log in with an API key or subscription
  logout <provider>                  drop a stored credential

session:
  -c, --continue                     continue the most recent session here
  -r, --resume [id]                  resume a session by id
      --session-id <uuid>            use a specific session id
      --fork-session                 branch instead of continuing in place
  -n, --name <name>                  name the session

model:
      --model <provider/model>       e.g. ollama/qwen3.8:latest
      --effort <level>               low | medium | high | xhigh | max

permissions:
      --permission-mode <mode>       manual | acceptEdits | auto | plan |
                                     dontAsk | bypassPermissions
      --add-dir <dirs>               extra directories tools may touch
      --allowed-tools "Bash(git *)"  allow without prompting
      --disallowed-tools "Write"     deny outright

config:
      --settings <file>              extra settings file
      --setting-sources <list>       user,project,local
      --mcp-config <file>            MCP servers (default ~/.claude.json)
      --strict-mcp-config            use only --mcp-config
      --system-prompt <text>         replace the system prompt
      --append-system-prompt <text>  add to the system prompt

output:
  -p, --print                        non-interactive: print and exit
      --output-format <fmt>          text | json | stream-json
      --verbose                      report tool calls on stderr
      --ax-screen-reader             flat text, no borders or animation

  -v, --version
  -h, --help`;
