# Design: MCP OAuth for http servers (interactive browser flow)

> Status: implemented. `internal/mcp/hub.go:372-377` builds the OAuth handler from `cfg.OAuth`.
> The text below is the original design; "Today" in the Goal means before that work.

## Goal
Let kiln connect to an http MCP server that requires OAuth (the deployed
design studio: `/.well-known/oauth-protected-resource/mcp` → Pocket ID IdP,
preregistered client, scopes "openid groups"). Today kiln has no MCP OAuth
flow (`internal/mcp/hub.go:101-106` says so), so OAuth-only servers, including
the studio channel, can't connect except on loopback.

## Approach (wire-up, not new crypto)
- The go-sdk ships `auth.AuthorizationCodeHandler` (OAuth 2.1: protected-
  resource discovery → auth-server metadata → PKCE → token exchange/refresh,
  SSRF-safe http client). `StreamableClientTransport.OAuthHandler` plugs it in
  (`streamable.go:1993`, `:2433` calls `TokenSource`).
- kiln already has loopback callback servers + PKCE + token refresh in
  `internal/auth/oauth/` (e.g. `anthropic.go:114 startAnthropicCallbackServer`)
  and a credential store (`internal/auth/file_store.go`, `credential.go`).
- kiln therefore supplies three things to the SDK handler: a **preregistered
  client** (the clientId from the server's `oauth` config), a **RedirectURL**
  (loopback), and an **AuthorizationCodeFetcher** that opens the browser and
  captures the code via a loopback server. It also supplies **token
  persistence** into kiln's credential store via
  `NewTokenSource`/`InitialTokenSource`.

## Config
MCP server config gains an `oauth` block (dropped today):
```jsonc
"oauth": { "clientId": "…", "callbackPort": 43123, "scopes": ["openid","groups"] }
```
`internal/mcp/config.go ServerConfig` gets an `OAuth *OAuthConfig`. Plugin
manifest parsing (`internal/claude/plugins/plugins.go MCPServers`) and `.mcp.json`
carry it through. Only servers with `oauth` set get an OAuthHandler.

## The new authentication paths — who/what/approve/worst-case

1. **Interactive sign-in (AuthorizationCodeFetcher).**
   - *Who triggers*: the local user, when a configured OAuth server is connected
     and no valid token is cached (first run, or after refresh fails).
   - *What proves they may*: they complete the IdP (Pocket ID) browser sign-in;
     PKCE (`code_verifier`) + a random `state` + RFC 9207 `iss` check bind the
     redirect to this request.
   - *Who approves*: the IdP (Pocket ID) and its group policy (scopes
     "openid groups"); kiln only relays the code.
   - *Worst case if forged/replayed*: a local process races the loopback
     callback and steals the `code`. Mitigations: bind to `127.0.0.1` only;
     verify `state` (reject mismatch); PKCE means a stolen code can't be
     exchanged without the verifier kiln never sent; single-use listener closed
     after the first valid redirect; short timeout; exact redirect path.

2. **Token storage & refresh.**
   - *Who triggers*: kiln, transparently, via the SDK `TokenSource`.
   - *What proves it*: a stored refresh token.
   - *Worst case if leaked*: the refresh token grants studio access as the user.
     Mitigation: store it via the existing credential store (`file_store.go`,
     same place/perms as provider tokens, `0600`), namespaced per
     server+issuer; never logged; redacted in diag.

## Loopback callback security (the main risk)
- Listener on `127.0.0.1:<callbackPort>` only (never `0.0.0.0`).
- Fixed redirect path; reject any other path/method.
- Require `state` to equal the value kiln generated. On a mismatch, reject and
  fail the flow; don't keep waiting, or an attacker could exhaust attempts.
- Close the listener after the first response; context timeout (e.g. 3 min).
- PKCE S256 (kiln's `pkce.go`) so an intercepted code is useless.
- If `callbackPort` is busy, fail with a clear message (don't fall back to a
  random public port).

## Failure modes handled
- No browser / headless (e.g. `--print`): don't block. Return a clear error with
  the URL to open manually, and skip the server rather than hang the session.
- Discovery fails / server not actually OAuth: fall back to today's
  "needs authentication" status; don't crash.
- Token refresh fails: clear the stored token, re-run interactive sign-in once;
  if that fails, surface the connect error as today.
- OAuth server connect must not block other MCP servers' connects (it already
  runs under the per-server goroutine in `connectOne`).

## Wiring
- `connectOne` (`hub.go`): if `cfg.OAuth != nil`, build the handler and set
  `transport.OAuthHandler` before `client.Connect`. Channel servers keep their
  legacy-protocol + channelTap wrapping; OAuth is orthogonal (the OAuthHandler
  authorizes the same transport the tap wraps).
- One handler instance per server (token cache lives in it); persisted token
  seeds `InitialTokenSource`.

## Out of scope (v1)
- Dynamic Client Registration (studio uses a preregistered client); CIMD.
- A `kiln mcp login <server>` subcommand (nice later; v1 signs in lazily on
  first connect).
- Non-loopback redirect strategies.

## Adversarial review findings (self-review, Opus; not independent eyes)

- **[P0] The 30 s connect timeout kills the browser flow.** `connectOne` wraps
  `client.Connect` in `connectCtx` (~30 s, `hub.go`). The SDK runs Authorize
  (→ the fetcher → human browser sign-in) *inside* Connect, and a person won't
  finish in 30 s. Fix: for an OAuth server, authorize under a long, separate
  deadline (e.g. 5 min), not the 30 s connect timeout. It must not block other
  servers (already a per-server goroutine).
- **[P0] Is the token applied to the GET channel stream, not just POST?**
  Channels exist for the server-initiated GET/SSE stream. If the transport's
  OAuthHandler only authorizes POST, the channel 401s on the deployed studio.
  Verify that the SDK applies the token to the standalone GET before claiming
  channels-over-OAuth works; if not, the seam must change.
- **[P1] Redirect URI must exactly match Pocket ID's registration for
  `STUDIO_MCP_CLIENT_ID`.** The manifest gives only `callbackPort: 43123`, not
  the path. If kiln uses `/callback` but the registered URI differs, authorize
  is rejected. Fix: confirm the registered redirect URI in Pocket ID before
  building, make the path part of the `oauth` config, and default to the
  registered one.
- **[P1] Legacy protocol + OAuth is an untested combination.** Channel servers
  connect with `2025-11-25` *and* would now carry an OAuthHandler. The two are
  orthogonal in principle but unverified; confirm that the legacy handshake
  still issues the 401/`WWW-Authenticate` challenge the handler needs.
- **[P2] Interactive flow needs a user-visible notice** ("open your browser to
  sign in to design-studio") via OnServer/a notice, or the connect looks hung.
- **[P2] Print/headless mode must not attempt the browser flow.** Return a
  clear "run interactively to sign in" error and skip the server.
- **[P2] Token storage key must be stable** across restarts: key by server
  origin URL + issuer, not the qualified hub name; `0600`, never logged.
- **[P3] Concurrent sign-ins race the fixed `callbackPort`; refresh-token
  rotation can invalidate another session's token.** Acceptable for v1; document.

### What these change in the build order
1. Verify first (cheap, before auth code): (a) the SDK applies OAuth to the GET
   stream; (b) Pocket ID's registered redirect URI for the client. Together
   they decide whether the chosen seam works at all.
2. Then build with a long authorize deadline, a user notice, a headless guard,
   and a stable token key.

## Test plan
- Unit: `oauth` config parse; the AuthorizationCodeFetcher against the SDK's
  `internal/oauthtest` fake authorization server (code+state+PKCE happy path,
  state-mismatch rejection, timeout).
- Integration: the channels raw-MCP httptest server (item 2) gains an optional
  401 + `WWW-Authenticate` resource-metadata challenge to exercise the handler
  end-to-end against a fake IdP.
