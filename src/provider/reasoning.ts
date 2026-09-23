/**
 * Reasoning suppression for local models.
 *
 * Why this module exists, and why it is not provider config:
 *
 * Some Qwen models keep reasoning no matter what the transport asks. They do not
 * refuse - they relocate the reasoning from the `thinking` field into `content`,
 * then exhaust `max_tokens` without ever emitting a tool call. An agent loop
 * driving such a model simply never acts.
 *
 * pi-ai models this properly and offers several transport-level fields for it
 * (`thinkingFormat: "qwen" | "qwen-chat-template"`, `thinkingTokenBudgetField`).
 * Measured against Ollama 0.32.15 on 2026-09-22, **none of them work**:
 *
 *   qwen3-cc:latest, identical results across all three variants
 *     chat_template_kwargs.enable_thinking   800 tok, 3086 reasoning chars, no call
 *     top-level enable_thinking              800 tok, 3086 reasoning chars, no call
 *     thinking_budget_tokens: 64             800 tok, 3086 reasoning chars, no call
 *     /no_think suffix                       621 tok, 0 reasoning chars, call emitted
 *
 * Byte-identical output across three different request fields is the tell:
 * Ollama drops them rather than forwarding them to llama.cpp. So suppression has
 * to happen in the one channel Ollama cannot ignore - the prompt itself.
 *
 * Re-run `scripts/check-thinking.ts <model>` after an Ollama upgrade; if a
 * transport field starts working, delete this module and set the provider field.
 */

/** How to stop a model reasoning past its token budget. */
export type ReasoningSuppression =
	/** Nothing needed: the model honors `think:false`, or does not reason. */
	| "none"
	/** Append Qwen's in-prompt switch to the last user message. */
	| "no_think_suffix";

export const NO_THINK = "/no_think";

/**
 * Models measured to need the suffix.
 *
 * Deliberately a list of verified ids rather than a regex over "qwen3": Qwen3.5
 * and qwen3.8 are both Qwen 3.x and both behave correctly, so a family-wide rule
 * would suppress reasoning on models that were using it well.
 */
const NEEDS_SUFFIX: ReadonlySet<string> = new Set(["qwen3-cc:latest", "qwen3:30b-a3b", "qwen3:30b"]);

/** Models measured to be fine without it. Kept explicit so the list is auditable. */
const VERIFIED_CLEAN: ReadonlySet<string> = new Set(["Qwen3.5:9b", "Qwen3.5:latest", "qwen3.8:latest"]);

export interface SuppressionOptions {
	/** Operator override, e.g. from config. Wins over every heuristic. */
	override?: ReasoningSuppression;
}

/**
 * Providers whose transport cannot control reasoning, so the prompt must.
 *
 * This gate is load-bearing. `/no_think` is a *Qwen prompt convention*, not a
 * general switch: appending it to a Claude or GPT request injects a stray token
 * into the user's message and suppresses nothing. Hosted providers already
 * express reasoning properly through pi's `thinkingLevelMap`, which the
 * transport honors, so the harness must leave them alone.
 */
const PROMPT_SUPPRESSED_PROVIDERS: ReadonlySet<string> = new Set(["ollama"]);

/**
 * Decide how to suppress reasoning for a model.
 *
 * Within a prompt-suppressed provider, unknown reasoning-capable models default
 * to the suffix. That asymmetry is deliberate: an unnecessary `/no_think` costs
 * a few tokens and a slightly shallower answer, while a missing one costs the
 * entire turn. Outside those providers the default is "none" - there, the
 * expensive mistake runs the other way.
 */
export function suppressionFor(
	model: { id: string; provider?: string; reasoning?: boolean },
	opts: SuppressionOptions = {},
): ReasoningSuppression {
	if (opts.override) return opts.override;
	if (!model.reasoning) return "none";
	// A model reached over a transport that honors thinking config needs
	// nothing from us, whatever its family.
	if (model.provider && !PROMPT_SUPPRESSED_PROVIDERS.has(model.provider)) return "none";
	if (VERIFIED_CLEAN.has(model.id)) return "none";
	if (NEEDS_SUFFIX.has(model.id)) return "no_think_suffix";
	return "no_think_suffix";
}

type Msg = { role: string; content?: unknown };

/**
 * Apply suppression to a message list, returning a new list.
 *
 * The suffix goes on the **last user message** rather than the system prompt,
 * because that is where Qwen's template looks for it. Putting it in the system
 * prompt looks tidier and does nothing.
 */
export function applySuppression<T extends Msg>(messages: readonly T[], mode: ReasoningSuppression): T[] {
	if (mode === "none") return [...messages];

	const lastUser = messages.findLastIndex((m) => m.role === "user");
	if (lastUser === -1) return [...messages];

	const target = messages[lastUser];
	// Only string content can take a suffix; multimodal content parts are left
	// alone rather than guessed at.
	if (typeof target.content !== "string") return [...messages];
	if (target.content.includes(NO_THINK)) return [...messages];

	const out = [...messages];
	out[lastUser] = { ...target, content: `${target.content} ${NO_THINK}` };
	return out;
}
