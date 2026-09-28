# ext-plugins fixture

Minimal project used only to give kiln a trusted cwd while the scenario
inspects whether anything from the real `~/.claude/plugins` (marketplaces,
commands, skills, agents, MCP servers) is visible to kiln. Kiln has no
plugin loader, so nothing here is expected to change that.
