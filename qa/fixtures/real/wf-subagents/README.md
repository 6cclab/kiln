# fleet

Three small, independent packages (`pkg/auth`, `pkg/payments`, `pkg/storage`),
each with one deliberate error-handling gap, for exercising kiln's parallel
subagent audit flow. No package imports another, so each can be audited
independently.
