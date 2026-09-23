import type {
	ApiKeyCredential,
	AuthResult,
	Model,
	Provider,
	ProviderStreamOptions,
	RefreshModelsContext,
} from "@earendil-works/pi-ai";
import { stream, streamSimple } from "@earendil-works/pi-ai/compat";

/**
 * Ollama provider, pointed at either a bare Ollama host or the ollama-gateway
 * (one URL + one bearer key fronting several hosts).
 *
 * Modelled on pi's own `extensions/llama/provider.ts`, which is the reference
 * for a locally-served OpenAI-compatible provider.
 */

export const OLLAMA_PROVIDER_ID = "ollama";
export const DEFAULT_OLLAMA_URL = "http://127.0.0.1:11434";

/**
 * Fallback when a model's serving context cannot be determined.
 *
 * Ollama's own default is 4096, which every tier rejects as unrunnable. Guessing
 * high would be worse: an over-large window silently selects a bigger tier and
 * the model truncates mid-turn with no error. 8192 is the smallest window that
 * runs, so an unknown model starts conservative and is corrected the moment
 * /api/ps reports the truth.
 */
const FALLBACK_CONTEXT = 8_192;

interface OllamaTag {
	name: string;
	model: string;
	details?: { family?: string; parameter_size?: string };
}

interface OllamaPsEntry {
	model: string;
	/** Effective serving context of the loaded instance. Authoritative when present. */
	context_length?: number;
}

interface OllamaShow {
	parameters?: string;
	template?: string;
	capabilities?: string[];
	model_info?: Record<string, unknown>;
}

async function getJson<T>(base: string, path: string, key: string | undefined, init?: RequestInit): Promise<T> {
	const res = await fetch(`${base}${path}`, {
		...init,
		headers: {
			"content-type": "application/json",
			...(key ? { authorization: `Bearer ${key}` } : {}),
			...init?.headers,
		},
	});
	if (!res.ok) throw new Error(`${path} -> HTTP ${res.status}`);
	// Ollama occasionally emits raw control characters inside strings (seen in
	// `parameters`), which strict JSON.parse rejects. Scrub them rather than
	// lose the whole document.
	return JSON.parse((await res.text()).replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, " ")) as T;
}

/** `num_ctx 32768` inside Ollama's whitespace-aligned `parameters` blob. */
function numCtxFromParameters(parameters: string | undefined): number | undefined {
	const m = parameters?.match(/^\s*num_ctx\s+(\d+)\s*$/m);
	return m ? Number(m[1]) : undefined;
}

/**
 * Resolve the window the model will actually be served with.
 *
 * Ordering matters, and getting it wrong is not a small error: every model here
 * reports a *training* context of 262144 in `model_info`, while qwen3-cc is
 * actually served at 32768 because its Modelfile sets `num_ctx`. Trusting
 * model_info would select the `large` tier and load ~32k of tool schemas into a
 * 32k window.
 */
export function resolveContextWindow(args: {
	ps?: OllamaPsEntry;
	show?: OllamaShow;
	/** Server-wide OLLAMA_CONTEXT_LENGTH, if the operator has told us. */
	serverDefault?: number;
}): number {
	// 1. What the loaded instance is actually running. Only Ollama knows this,
	//    and only while the model is resident.
	if (args.ps?.context_length && args.ps.context_length > 0) return args.ps.context_length;

	// 2. A num_ctx pinned in the Modelfile overrides the server default.
	const numCtx = numCtxFromParameters(args.show?.parameters);
	if (numCtx && numCtx > 0) return numCtx;

	// 3. The server-wide default applies to any model that pins nothing.
	if (args.serverDefault && args.serverDefault > 0) return args.serverDefault;

	// 4. Deliberately NOT model_info.*.context_length - that is the training
	//    context and is wildly larger than what gets served.
	return FALLBACK_CONTEXT;
}

function toPiModel(args: {
	id: string;
	baseUrl: string;
	contextWindow: number;
	capabilities: string[];
}): Model<"openai-completions"> {
	const { id, baseUrl, contextWindow, capabilities } = args;
	const reasoning = capabilities.includes("thinking");
	const vision = capabilities.includes("vision");

	return {
		id,
		name: id,
		api: "openai-completions",
		provider: OLLAMA_PROVIDER_ID,
		baseUrl,
		reasoning,
		...(reasoning && {
			// Phase 0 finding: qwen3-cc and qwen3:30b-a3b ignore Ollama's
			// `think:false` - they keep reasoning, move it from `thinking` into
			// `content`, and exhaust max_tokens without ever emitting the tool
			// call. "off" must therefore be a reachable level.
			thinkingLevelMap: { off: "off", minimal: null, low: null, medium: "medium", high: null, xhigh: null },
		}),
		input: vision ? ["text", "image"] : ["text"],
		// Self-hosted: no per-token cost. Keeps cost reporting honest rather
		// than inventing a number.
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		contextWindow,
		maxTokens: contextWindow,
		compat: {
			supportsStore: false,
			supportsDeveloperRole: false,
			// Verified in Phase 0: `reasoning_effort` is accepted and ignored.
			supportsReasoningEffort: false,
			supportsUsageInStreaming: true,
			supportsStrictMode: false,
			maxTokensField: "max_tokens",
			// NOT setting thinkingFormat. pi's "qwen-chat-template" is correct for
			// llama.cpp directly, but scripts/check-thinking.ts measured Ollama
			// 0.32.15 silently dropping chat_template_kwargs, top-level
			// enable_thinking, AND thinking_budget_tokens - all three produced
			// byte-identical output on qwen3-cc. Setting it here would look like
			// the problem was handled while changing nothing.
			// Suppression is done in src/provider/reasoning.ts instead.
		},
	};
}

export interface OllamaProviderOptions {
	/** Base URL of the Ollama host or gateway. */
	url?: string;
	/** Bearer token, for the gateway. Bare Ollama needs none. */
	apiKey?: string;
	/** Server-wide OLLAMA_CONTEXT_LENGTH, used for models that pin no num_ctx. */
	serverDefaultContext?: number;
	/** Drop models that cannot call tools. Default true: the agent loop needs them. */
	requireTools?: boolean;
}

/** Discover models and their true serving windows. Exported for direct testing. */
export async function discoverModels(opts: OllamaProviderOptions): Promise<Model<"openai-completions">[]> {
	const base = (opts.url ?? DEFAULT_OLLAMA_URL).replace(/\/+$/, "");
	const key = opts.apiKey;

	const { models: tags } = await getJson<{ models: OllamaTag[] }>(base, "/api/tags", key);

	// Loaded models report their real context window; unloaded ones cannot
	// without being loaded, which would be a rude side effect of listing.
	const ps = await getJson<{ models: OllamaPsEntry[] }>(base, "/api/ps", key).catch(() => ({ models: [] }));
	const loaded = new Map(ps.models.map((m) => [m.model, m]));

	const described = await Promise.all(
		tags.map(async (tag) => {
			const show = await getJson<OllamaShow>(base, "/api/show", key, {
				method: "POST",
				body: JSON.stringify({ model: tag.model }),
			}).catch(() => undefined);

			return {
				model: toPiModel({
					id: tag.model,
					baseUrl: `${base}/v1`,
					contextWindow: resolveContextWindow({
						ps: loaded.get(tag.model),
						show,
						serverDefault: opts.serverDefaultContext,
					}),
					capabilities: show?.capabilities ?? [],
				}),
				capabilities: show?.capabilities ?? [],
			};
		}),
	);

	return described
		// An Ollama host serves embedding models (nomic-embed-text) alongside
		// chat models, and they cannot hold a conversation at all. Offering one
		// in the model picker produces a baffling failure at first turn.
		.filter((d) => d.capabilities.includes("completion"))
		// The agent loop is tool calls. A chat-only model would connect, respond
		// in prose, and never act - worse than refusing it up front.
		.filter((d) => opts.requireTools === false || d.capabilities.includes("tools"))
		.map((d) => d.model);
}

export function createOllamaProvider(opts: OllamaProviderOptions = {}): Provider<"openai-completions"> {
	let models: readonly Model<"openai-completions">[] = [];
	const defaultUrl = opts.url ?? DEFAULT_OLLAMA_URL;

	const urlFrom = (credential: ApiKeyCredential | undefined): string =>
		(typeof credential?.env?.OLLAMA_BASE_URL === "string" && credential.env.OLLAMA_BASE_URL.trim()) || defaultUrl;

	return {
		id: OLLAMA_PROVIDER_ID,
		name: "Ollama",
		baseUrl: `${defaultUrl.replace(/\/+$/, "")}/v1`,
		auth: {
			apiKey: {
				name: "Ollama server",
				login: async (interaction): Promise<ApiKeyCredential> => {
					const entered = await interaction.prompt({
						type: "text",
						message: "Ollama server or gateway URL",
						placeholder: process.env.OLLAMA_HOST ?? DEFAULT_OLLAMA_URL,
					});
					const url = (entered.trim() || process.env.OLLAMA_HOST || DEFAULT_OLLAMA_URL).replace(/\/+$/, "");
					const apiKey = (
						await interaction.prompt({
							type: "secret",
							message: "Bearer token (blank for a bare Ollama host)",
						})
					).trim();

					// Fail at login rather than at first turn.
					await getJson<{ models: OllamaTag[] }>(url, "/api/tags", apiKey || undefined);

					return { type: "api_key", key: apiKey || undefined, env: { OLLAMA_BASE_URL: url } };
				},
				check: async ({ ctx, credential }) => {
					const url = credential?.env?.OLLAMA_BASE_URL ?? (await ctx.env("OLLAMA_HOST"));
					return url ? { type: "api_key", source: credential ? "stored credential" : "OLLAMA_HOST" } : undefined;
				},
				resolve: async ({ ctx, credential }): Promise<AuthResult | undefined> => {
					const url = urlFrom(credential) || (await ctx.env("OLLAMA_HOST"));
					if (!url) return undefined;
					return {
						// Ollama ignores the key; the gateway requires it. "local"
						// keeps the OpenAI client happy when there is none.
						auth: { apiKey: credential?.key ?? "local", baseUrl: `${url.replace(/\/+$/, "")}/v1` },
						env: { ...credential?.env, OLLAMA_BASE_URL: url },
						source: credential ? "stored credential" : "OLLAMA_HOST",
					};
				},
			},
		},
		getModels: () => models,
		refreshModels: async (context: RefreshModelsContext): Promise<void> => {
			if (context.stored) {
				const restored = context.stored.models.filter(
					(m): m is Model<"openai-completions"> => m.provider === OLLAMA_PROVIDER_ID && m.api === "openai-completions",
				);
				if (!(await context.publish({ update: () => { models = restored; } }))) return;
			}
			if (!context.allowNetwork || context.signal.aborted) return;

			const credential = context.credential?.type === "api_key" ? context.credential : undefined;
			const refreshed = await discoverModels({
				url: urlFrom(credential),
				apiKey: credential?.key ?? opts.apiKey,
				serverDefaultContext: opts.serverDefaultContext,
			});
			if (context.signal.aborted) return;

			await context.publish({
				persist: { models: refreshed, checkedAt: Date.now() },
				update: () => { models = refreshed; },
			});
		},
		stream: (model, context, options) => stream(model, context, options as ProviderStreamOptions | undefined),
		streamSimple: (model, context, options) => streamSimple(model, context, options),
	};
}
