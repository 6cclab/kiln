import type { AuthType, Model, MutableModels, Provider } from "@earendil-works/pi-ai";
import { builtinModels } from "@earendil-works/pi-ai/providers/all";
import { FileCredentialStore } from "../auth/credential-store.ts";
import { createOllamaProvider, type OllamaProviderOptions } from "./ollama.ts";
import { type Tier, requireTierForWindow } from "../budget/tier.ts";
import { suppressionFor, type ReasoningSuppression } from "./reasoning.ts";

/**
 * The provider registry: every model the harness can drive, behind one type.
 *
 * Model-agnosticism is inherited rather than built. `builtinModels()` registers
 * all ~45 of pi's providers - Anthropic, OpenAI, Google, Bedrock, Groq and the
 * rest - each already carrying `contextWindow`, `cost` and auth semantics.
 * Ollama is added as one more entry, not as a special case. Nothing downstream
 * branches on which provider a model came from; the only thing that varies is
 * the `Tier`, and that is derived from a number every provider populates.
 */

export interface RegistryOptions {
	ollama?: OllamaProviderOptions;
	/** Path for persisted credentials. Defaults to ~/.harness/credentials.json. */
	credentialsPath?: string;
}

export interface Registry {
	models: MutableModels;
	/** Providers with usable auth, for a model picker. */
	available(): Promise<readonly Model<never>[]>;
	/** Run a provider's login flow and persist the credential. */
	login: MutableModels["login"];
	resolve(providerId: string, modelId: string): Promise<ResolvedModel>;
}

/** A chosen model plus everything the harness needs to run it. */
export interface ResolvedModel {
	model: Model<never>;
	tier: Tier;
	/** How to stop this model reasoning past its budget. */
	suppression: ReasoningSuppression;
}

export function createRegistry(opts: RegistryOptions = {}): Registry {
	const credentials = new FileCredentialStore(opts.credentialsPath);
	const models = builtinModels({ credentials });

	// One more provider alongside the built-ins. `setProvider` upserts by id, so
	// this also allows overriding a built-in later without special handling.
	models.setProvider(createOllamaProvider(opts.ollama) as Provider);

	return {
		models,

		available: async () => (await models.getAvailable()) as readonly Model<never>[],

		login: (providerId: string, type: AuthType, interaction) => models.login(providerId, type, interaction),

		resolve: async (providerId: string, modelId: string): Promise<ResolvedModel> => {
			const model = models.getModel(providerId, modelId);
			if (!model) {
				throw new Error(
					`Unknown model "${modelId}" on provider "${providerId}". ` +
						`Run a refresh first if the provider is dynamic.`,
				);
			}
			return {
				model: model as Model<never>,
				// Throws rather than returning an unrunnable tier: better to fail
				// at selection than to truncate mysteriously on the first turn.
				tier: requireTierForWindow(model.contextWindow),
				suppression: suppressionFor(model),
			};
		},
	};
}

/** Subscription-backed providers, for a "log in with your plan" picker. */
export function subscriptionProviders(models: MutableModels): Provider[] {
	return models.getProviders().filter((p) => p.auth?.oauth?.isSubscription === true);
}
