package router

// ProviderRoles is the model each modelRoles role uses on one provider
// when the user's modelRoles leaves that role out.
type ProviderRoles struct {
	Provider string
	// Roles maps a role name (fast, structured, heavy) to a model ID on
	// Provider.
	Roles map[string]string
}

// DefaultRoles is kiln/auto's zero-config table, in the order providers
// are tried. It is hand-picked, as pi's defaultModelPerProvider is
// (model-resolver.ts): catalog prices and flags do not rank models — an
// older Opus costs more than a newer one, and almost every model is
// flagged as reasoning. Update it when a provider ships a new generation.
// openai-codex follows pi's jev-router (Luna, Terra, Sol).
var DefaultRoles = []ProviderRoles{
	{Provider: "anthropic", Roles: map[string]string{
		"fast":       "claude-haiku-4-5",
		"structured": "claude-sonnet-5",
		"heavy":      "claude-opus-5-5",
	}},
	{Provider: "openai-codex", Roles: map[string]string{
		"fast":       "gpt-5.6-luna",
		"structured": "gpt-5.6-terra",
		"heavy":      "gpt-5.6-sol",
	}},
}

// DefaultRoleNames lists the roles DefaultRoles fills, in note order.
var DefaultRoleNames = []string{"fast", "structured", "heavy"}
