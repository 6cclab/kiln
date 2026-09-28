---
name: api-conventions
description: House conventions for HTTP endpoints in this service. The model should consult this before adding or changing any route.
user-invocable: false
---
Every endpoint in this service must:

- Set a response header `X-Api-Version: 2`.
- Return JSON shaped as `{"data": ..., "meta": {}}`, never a bare object or list.

These conventions are not visible anywhere else in the codebase — they live
only in this skill.
