/**
 * Phase 1c: determine which reasoning-suppression mechanism Ollama honors.
 *
 * Phase 0 established the problem: qwen3-cc and qwen3:30b-a3b keep reasoning
 * despite `think:false`, relocating it from the `thinking` field into `content`,
 * and exhaust max_tokens without emitting a tool call. pi-ai already names the
 * failure (types.ts:719) and offers several transport-level fields for it. Which
 * one Ollama actually implements is an empirical question, so ask.
 *
 * A mechanism "works" if the model emits a tool call in few tokens without a
 * wall of prose. Passing means the harness can configure it in the provider
 * instead of appending `/no_think` to the user's message.
 *
 * Usage: node --experimental-strip-types scripts/check-thinking.ts [model]
 */

const HOST = (process.env.OLLAMA_HOST ?? "http://127.0.0.1:11434").replace(/\/+$/, "");
const MODEL = process.argv[2] ?? "qwen3.8:latest";
const TIMEOUT_MS = 180_000;

const TOOLS = [
	{
		type: "function",
		function: {
			name: "bash",
			description: "Run a shell command and return its output.",
			parameters: {
				type: "object",
				properties: { command: { type: "string" } },
				required: ["command"],
			},
		},
	},
];

const SYSTEM = "You are a coding assistant. Use the provided tools when they are needed.";
const PROMPT = "Count the lines in every .go file under the current directory.";

interface Outcome {
	label: string;
	ok: boolean;
	detail: string;
}

async function post(path: string, body: unknown): Promise<any> {
	const res = await fetch(`${HOST}${path}`, {
		method: "POST",
		signal: AbortSignal.timeout(TIMEOUT_MS),
		headers: { "content-type": "application/json" },
		body: JSON.stringify(body),
	});
	const text = await res.text();
	if (!res.ok) throw new Error(`HTTP ${res.status}: ${text.slice(0, 160)}`);
	return JSON.parse(text.replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, " "));
}

/** OpenAI-shaped attempt: the wire format pi-ai's `openai-completions` API drives. */
async function viaOpenAI(label: string, extra: Record<string, unknown>): Promise<Outcome> {
	try {
		const body = await post("/v1/chat/completions", {
			model: MODEL,
			temperature: 0,
			max_tokens: 800,
			tools: TOOLS,
			messages: [
				{ role: "system", content: SYSTEM },
				{ role: "user", content: PROMPT },
			],
			...extra,
		});
		const choice = body.choices?.[0];
		const calls = choice?.message?.tool_calls;
		const reasoning = (choice?.message?.reasoning ?? "").length;
		const used = body.usage?.completion_tokens ?? 0;
		return {
			label,
			ok: Array.isArray(calls) && calls.length > 0,
			detail: `tokens=${used} finish=${choice?.finish_reason} reasoning_chars=${reasoning} call=${calls?.[0]?.function?.name ?? "NONE"}`,
		};
	} catch (err) {
		return { label, ok: false, detail: `ERROR ${(err as Error).message.slice(0, 80)}` };
	}
}

/** Native attempt, for mechanisms Ollama exposes only on /api/chat. */
async function viaNative(label: string, extra: Record<string, unknown>, userSuffix = ""): Promise<Outcome> {
	try {
		const body = await post("/api/chat", {
			model: MODEL,
			stream: false,
			tools: TOOLS,
			options: { temperature: 0, num_predict: 800 },
			messages: [
				{ role: "system", content: SYSTEM },
				{ role: "user", content: PROMPT + userSuffix },
			],
			...extra,
		});
		const calls = body.message?.tool_calls;
		return {
			label,
			ok: Array.isArray(calls) && calls.length > 0,
			detail: `tokens=${body.eval_count} done=${body.done_reason} thinking_chars=${(body.message?.thinking ?? "").length} call=${calls?.[0]?.function?.name ?? "NONE"}`,
		};
	} catch (err) {
		return { label, ok: false, detail: `ERROR ${(err as Error).message.slice(0, 80)}` };
	}
}

console.log(`model: ${MODEL}   host: ${HOST}\n`);
console.log("Does Ollama honor each reasoning-suppression mechanism?\n");

const results: Outcome[] = [
	// Baseline: the documented control that Phase 0 showed does not work.
	await viaNative("native think:false (baseline)", { think: false }),

	// pi-ai thinkingFormat: "qwen-chat-template" -> chat_template_kwargs.
	// This is what pi sets for llama.cpp-backed Qwen models.
	await viaOpenAI("chat_template_kwargs.enable_thinking", {
		chat_template_kwargs: { enable_thinking: false },
	}),

	// pi-ai thinkingFormat: "qwen" -> top-level enable_thinking.
	await viaOpenAI("top-level enable_thinking", { enable_thinking: false }),

	// pi-ai thinkingTokenBudgetField: "thinking_budget_tokens" (llama.cpp).
	// Caps reasoning rather than disabling it, leaving room for the answer.
	await viaOpenAI("thinking_budget_tokens: 64", { thinking_budget_tokens: 64 }),

	// The Phase 0 fallback that is already proven to work.
	await viaNative("/no_think suffix (known good)", { think: false }, " /no_think"),
];

for (const r of results) {
	console.log(`  ${r.ok ? "WORKS " : "fails "} ${r.label.padEnd(38)} ${r.detail}`);
}

const winner = results.find((r) => r.ok && !r.label.startsWith("/no_think") && !r.label.includes("baseline"));
console.log(
	`\nVERDICT: ${
		winner
			? `use provider config -> ${winner.label}`
			: "no transport-level mechanism works; keep the /no_think suffix fallback"
	}`,
);
