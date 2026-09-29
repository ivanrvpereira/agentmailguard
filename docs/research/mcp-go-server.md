# Building an MCP server in Go (late 2026)

Research date: 2026-09-29 UTC.

## Summary

The latest stable MCP revision is **2026-07-28**. It changes the protocol more than any earlier revision. The protocol is now stateless: there is no `initialize` handshake, no `Mcp-Session-Id`, no HTTP GET stream, and no `Last-Event-ID` resumability. Every request carries its protocol version and client capabilities in `_meta`. A new `server/discover` RPC is mandatory. Server-to-client requests (elicitation, sampling, roots) are replaced by Multi Round-Trip Requests (MRTR). Roots, Sampling, Logging, and Dynamic Client Registration are deprecated. The draft has no changes yet. The Go library to use is the official **`github.com/modelcontextprotocol/go-sdk` v1.8.0** (2026-09-14). It is v1-stable, listed as a Tier 1 SDK, supports every revision from 2024-11-05 to 2026-07-28 on the same API, targets `go 1.25.0` (the same `go` directive this repository already uses), and gives typed tools with inferred input and output schemas. Its `mcp` package links 7 third-party modules. For this project the best starting transport is **stdio**, run as a CLI subcommand. It forwards tool calls to the existing loopback server, so the MCP process never holds credentials. All four target clients support stdio, and stdio is the only local option in Claude Desktop. Clients still use legacy revisions (Claude Code keeps stdio servers on the legacy handshake by default), so the server must serve both eras. The SDK does this automatically.

Sources are cited inline. Specification text was read from the spec source repository, `modelcontextprotocol/modelcontextprotocol` at commit [`046fa30e`](https://github.com/modelcontextprotocol/modelcontextprotocol/tree/046fa30efd374370afb87ef830bd788eac5f217e) (main, 2026-09-28). The same text is published at `https://modelcontextprotocol.io/specification/<rev>/...`. SDK facts come from module source downloaded through the Go module proxy (`go mod download`) and from the GitHub API (`gh api`).

## 1. Spec revisions

| Revision | GitHub release | Status | Source |
| --- | --- | --- | --- |
| 2024-10-07 | tag only, created 2024-11-06 | pre-launch tag | [tags](https://github.com/modelcontextprotocol/modelcontextprotocol/tags) |
| 2024-11-05 | release 2025-01-17, `2024-11-05-final` 2025-03-26 | superseded | [spec](https://modelcontextprotocol.io/specification/2024-11-05) |
| 2025-03-26 | 2025-03-26 | superseded | [changelog](https://modelcontextprotocol.io/specification/2025-03-26/changelog) |
| 2025-06-18 | 2025-06-18 | superseded | [changelog](https://modelcontextprotocol.io/specification/2025-06-18/changelog) |
| 2025-11-25 | RC 2025-11-15, final 2025-11-25 | previous stable (latest "legacy handshake" revision) | [changelog](https://modelcontextprotocol.io/specification/2025-11-25/changelog) |
| 2026-07-28 | RC 2026-05-29, final 2026-07-28 | **current stable** ("marks the stable release of the 2026-07-28 revision") | [release](https://github.com/modelcontextprotocol/modelcontextprotocol/releases/tag/2026-07-28), [changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog) |
| draft | — | no changes; changelog says "Changes since the most recent release will accumulate here." | [draft changelog source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/draft/changelog.mdx) |

Release dates come from `gh api repos/modelcontextprotocol/modelcontextprotocol/releases`.

### 2025-03-26 (vs 2024-11-05)

From the [2025-03-26 changelog](https://modelcontextprotocol.io/specification/2025-03-26/changelog):

- OAuth 2.1 authorization framework.
- Streamable HTTP replaces HTTP+SSE.
- JSON-RPC batching (removed again in 2025-06-18).
- Tool annotations (`readOnlyHint`, `destructiveHint`, and others).
- `message` on progress notifications, audio content, and the `completions` capability.

### 2025-06-18

From the [2025-06-18 changelog](https://modelcontextprotocol.io/specification/2025-06-18/changelog):

- Batching removed.
- Structured tool output (`structuredContent`, `outputSchema`).
- MCP servers classified as OAuth resource servers with Protected Resource Metadata.
- Clients MUST implement RFC 8707 resource indicators.
- Security best-practices page.
- Elicitation.
- Resource links in tool results.
- `MCP-Protocol-Version` header required on HTTP after negotiation.
- `_meta` on more types.
- `context` on completion requests.
- `title` fields.

### 2025-11-25

From the [2025-11-25 changelog](https://modelcontextprotocol.io/specification/2025-11-25/changelog):

- OpenID Connect Discovery for authorization servers.
- `icons` on tools, resources, resource templates, and prompts (SEP-973).
- Incremental scope consent via `WWW-Authenticate` (SEP-835).
- Tool-name guidance (SEP-986).
- Standards-based enum schemas in elicitation.
- **URL mode elicitation** (SEP-1036).
- Tool calling inside sampling.
- **Client ID Metadata Documents (CIMD)** as recommended client registration (SEP-991).
- **Experimental tasks** (SEP-1686).
- Minor changes:
  - stdio servers may use stderr for all logging.
  - Invalid `Origin` MUST get HTTP 403.
  - Input validation errors are tool execution errors, not protocol errors (SEP-1303).
  - SSE polling and resumption clarifications.
  - JSON Schema 2020-12 is the default dialect (SEP-1613).
- Governance: SDK tiering (SEP-1730).

### 2026-07-28 (current)

From the [2026-07-28 changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog) ([source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/changelog.mdx)).

Major changes:

1. Protocol-level sessions and `Mcp-Session-Id` are removed. List endpoints no longer vary per connection. Cross-call state uses "explicit, server-minted handles passed as ordinary tool arguments" (SEP-2567).
2. The `initialize`/`notifications/initialized` handshake is removed. Every request carries `io.modelcontextprotocol/protocolVersion` and `io.modelcontextprotocol/clientCapabilities` in `_meta`. Version mismatches return `UnsupportedProtocolVersionError` (SEP-2575).
3. `server/discover` is added. Servers MUST implement it.
4. The HTTP GET endpoint and `resources/subscribe` are replaced by `subscriptions/listen`: one long-lived POST response stream for opted-in change notifications.
5. `ping`, `logging/setLevel`, and `notifications/roots/list_changed` are removed. The log level is set per request in `_meta`.
6. Tasks move out of core into an official extension, `io.modelcontextprotocol/tasks` (SEP-2663).
7. Multi Round-Trip Requests (MRTR) replace server-initiated `roots/list`, `sampling/createMessage`, and `elicitation/create`. The server returns `resultType: "input_required"` with `inputRequests`. The client retries the original request with `inputResponses` (SEP-2322).
8. Every result carries a required `resultType`.
9. SSE resumability (`Last-Event-ID`) is removed. A broken stream loses the request, and the client re-issues it.

Minor changes:

- `extensions` capability field.
- OpenTelemetry `_meta` keys.
- Deterministic `tools/list` order (SHOULD).
- Required `Mcp-Method` and `Mcp-Name` HTTP headers, and `x-mcp-header` parameter mirroring (SEP-2243).
- Required `ttlMs` and `cacheScope` on list and read results (SEP-2549).
- Resource-not-found error code changes to `-32602`.
- RFC 9207 `iss` validation.
- `application_type` in DCR.
- Credentials bound to their issuer.
- `inputSchema`/`outputSchema` accept any JSON Schema 2020-12 keyword, and `structuredContent` accepts any JSON value (SEP-2106).
- `notifications/elicitation/complete` is removed.
- Error-code allocation policy: `-32020` to `-32099` is reserved for MCP.

Deprecated, with earliest removal in the first revision on or after 2027-07-28 ([deprecated registry](https://modelcontextprotocol.io/specification/2026-07-28/deprecated)):

- Roots, Sampling, and Logging (SEP-2577). Suggested migration: "log to `stderr` (stdio) or use OpenTelemetry".
- Dynamic Client Registration, in favour of CIMD.
- `includeContext: thisServer/allServers`.
- The HTTP+SSE transport.

Governance: a feature lifecycle policy with a minimum twelve-month deprecation window (SEP-2596).

## 2. Transports

The 2026-07-28 spec defines **stdio** and **Streamable HTTP**. HTTP+SSE (2024-11-05) is Deprecated ([streamable-http source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/basic/transports/streamable-http.mdx)).

### stdio (2026-07-28)

Source: [stdio.mdx](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/basic/transports/stdio.mdx).

- The client launches the server as a subprocess. Messages are newline-delimited JSON-RPC and "MUST NOT contain embedded newlines".
- The server "MAY write UTF-8 strings to `stderr` for any logging purposes". The server "MUST NOT write anything to its `stdout` that is not a valid MCP message".
- "The server MUST NOT write JSON-RPC requests to stdout". Server-to-client interactions use `InputRequiredResult` (MRTR).
- Cancellation is `notifications/cancelled`.
- Shutdown: the client closes stdin. "Servers SHOULD exit promptly when their standard input is closed".
- Backward compatibility: a dual-era client "SHOULD probe with `server/discover`" and falls back to `initialize` if the server is legacy.

### Streamable HTTP (2026-07-28)

- There is one MCP endpoint that accepts POST. Every client message is its own POST with `Accept: application/json, text/event-stream`.
- For a request, the server answers with either `application/json` or an SSE stream scoped to that request. The stream carries request-related notifications and then the response. The server "MUST NOT send independent JSON-RPC requests on this stream".
- Long-lived change notifications use a `subscriptions/listen` POST whose response stream stays open.
- Closing the SSE response stream "MUST be treated by the server as cancellation".
- Headers:
  - `MCP-Protocol-Version` is required on every POST and must match `_meta` (else 400 `HeaderMismatch` `-32020`).
  - `Mcp-Method` is required on all requests.
  - `Mcp-Name` is required for `tools/call`, `resources/read`, and `prompts/get`.
  - Optional `Mcp-Param-*` headers come from `x-mcp-header` schema annotations.
- "Resumable SSE streams via `Last-Event-ID` are not supported."
- For older clients, a server that supports only 2026-07-28 SHOULD:
  - answer GET/DELETE with 405;
  - ignore `Mcp-Session-Id` and "do not mint or echo session IDs";
  - ignore `Last-Event-ID`.

### Streamable HTTP in 2025-03-26 to 2025-11-25 (legacy)

Legacy clients still need this. Source: [2025-11-25 transports](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).

- The server MAY assign an `MCP-Session-Id` in the `InitializeResult` response. The client MUST echo it. The server returns 404 for a terminated session. The client MAY send DELETE to end the session.
- The client MAY open a standalone GET SSE stream for server-initiated messages.
- Streams are resumable with `Last-Event-ID`.
- Stateless mode was not defined in the spec. The Go SDK docs say "Stateless mode is not directly discussed in the spec" for these revisions ([go-sdk docs/protocol.md](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/docs/protocol.md)).

### Security requirements (Streamable HTTP, 2026-07-28 "Security & Endpoint")

1. "Servers MUST validate the `Origin` header on all incoming connections to prevent DNS rebinding attacks." An invalid `Origin` MUST get 403.
2. "When running locally, servers SHOULD bind only to localhost (127.0.0.1)".
3. "Servers SHOULD implement proper authentication for all connections."

The [security best practices](https://modelcontextprotocol.io/docs/tutorials/security/security_best_practices) page ([2026-07-28 source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/docs/2026-07-28/tutorials/security/security_best_practices.mdx), "Local MCP Server Compromise") says local servers SHOULD "Use the `stdio` transport to limit access to just the MCP client". If they use HTTP, they SHOULD "Require an authorization token" or "Use unix domain sockets or other Interprocess Communication (IPC) mechanisms with restricted access".

## 3. Authorization

Source: [2026-07-28 authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization) ([source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/basic/authorization/index.mdx)).

- "Authorization is OPTIONAL". HTTP transports "SHOULD conform". "Implementations using an STDIO transport SHOULD NOT follow this specification, and instead retrieve credentials from the environment." (The spec says SHOULD NOT, not MUST NOT.)
- Base standards:
  - OAuth 2.1 (draft-ietf-oauth-v2-1-13)
  - RFC 6750 and RFC 8414
  - RFC 7591 (DCR, deprecated)
  - RFC 8707 resource indicators
  - RFC 9728 Protected Resource Metadata
  - RFC 9207 issuer identification
  - Client ID Metadata Documents (draft-ietf-oauth-client-id-metadata-document-00)
  - OpenID Connect Discovery 1.0
- Rules for MCP servers and clients:
  - The MCP server is an OAuth 2.1 resource server. It "MUST implement OAuth 2.0 Protected Resource Metadata (RFC9728)". Clients MUST use it for authorization-server discovery.
  - Authorization servers and clients SHOULD support CIMD. DCR is only MAY and is deprecated.
  - Clients MUST send `resource` (RFC 8707) with the server's canonical URI.
  - Servers MUST validate that tokens were issued for them as the audience. "MCP servers MUST NOT accept or transit any other tokens." Invalid or expired tokens get 401.
  - Scope step-up uses `WWW-Authenticate` `insufficient_scope` (since 2025-11-25).
- Extensions ([ext-auth](https://github.com/modelcontextprotocol/ext-auth)) are optional and include Enterprise-Managed Authorization and OAuth Client Credentials ([extension overview source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/extensions/client-matrix.mdx)).

## 4. Server and client features

### Tool definition (2026-07-28)

Source: [server/tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools) ([source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/server/tools.mdx)).

A tool has these fields:

- `name`: SHOULD be 1 to 128 characters of `A-Z a-z 0-9 _ - .`
- `title`
- `description`
- `icons`
- `inputSchema`: JSON Schema, 2020-12 by default. For a tool with no parameters, the recommended schema is `{"type":"object","additionalProperties":false}`.
- `outputSchema`
- `annotations`

Tool annotations, quoted from [schema.ts](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/schema/2026-07-28/schema.ts):

| Annotation | Spec text | Default |
| --- | --- | --- |
| `title` | "A human-readable title for the tool." | — |
| `readOnlyHint` | "If true, the tool does not modify its environment." | false |
| `destructiveHint` | "If true, the tool may perform destructive updates to its environment. If false, the tool performs only additive updates. (This property is meaningful only when `readOnlyHint == false`)" | **true** |
| `idempotentHint` | "If true, calling the tool repeatedly with the same arguments will have no additional effect on its environment. (meaningful only when `readOnlyHint == false`)" | false |
| `openWorldHint` | "If true, this tool may interact with an 'open world' of external entities. If false, the tool's domain of interaction is closed." | **true** |

The schema also says: "all properties in `ToolAnnotations` are **hints**. They are not guaranteed to provide a faithful description of tool behavior … Clients should never make tool use decisions based on `ToolAnnotations` received from untrusted servers." The tools page says: "clients MUST consider tool annotations to be untrusted unless they come from trusted servers."

### Structured output

- `structuredContent` may be any JSON value.
- "For backwards compatibility, a tool that returns structured content SHOULD also return the serialized JSON in a TextContent block."
- If an `outputSchema` is given, "Servers MUST provide structured results that conform to this schema. Clients SHOULD validate".

### Errors

- Protocol errors (JSON-RPC) cover unknown tools and malformed requests.
- Tool execution errors (`isError: true`) cover API failures, input validation, and business logic, so "language models can use [them] to self-correct".

### Stateful tools

This is non-normative guidance. With no sessions, cross-call state uses opaque handles passed as arguments. The server should validate authorization "against the handle on every call". An expired or unknown handle should get a tool execution error.

### Security considerations (tools)

- "Servers MUST: Validate all tool inputs; Implement proper access controls; Rate limit tool invocations; Sanitize tool outputs."
- "Clients SHOULD: Prompt for user confirmation on sensitive operations; Show tool inputs to the user …; Validate tool results before passing to LLM; Implement timeouts; Log tool usage".
- "there SHOULD always be a human in the loop with the ability to deny tool invocations."

The spec overview ([index source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/index.mdx), "Tool Safety") says "Hosts must obtain explicit user consent before invoking any tool".

### Other server features

- **Resources and resource templates** ([resources](https://modelcontextprotocol.io/specification/2026-07-28/server/resources)):
  - Resources are application-driven context identified by URI.
  - Resources and content blocks may carry `annotations`: `audience` (`"user"`, `"assistant"`), `priority` (0 to 1), and `lastModified`.
  - Servers "MUST validate all resource URIs".
  - Subscriptions now go through `subscriptions/listen`.
- **Prompts**: user-controlled templates, for example slash commands.
- **List changed notifications**: `notifications/{tools,prompts,resources}/list_changed`. In 2026-07-28 they are delivered on a `subscriptions/listen` stream.
- **Caching** (2026-07-28): list and read results carry `ttlMs` and `cacheScope`.
- **Pagination**: opaque cursors on list methods ([pagination source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/server/utilities/pagination.mdx)). Pagination does not apply to tool results. A tool that lists mail needs its own cursor argument.
- **Completion**: argument autocompletion for prompts and resource templates.
- **Progress** (`notifications/progress` with a `progressToken`) and **cancellation** (stream close on HTTP, `notifications/cancelled` on stdio).
- **Logging** (`notifications/message`): Deprecated in 2026-07-28. Use stderr.

### Client features the server can call

- **Elicitation** ([client/elicitation](https://modelcontextprotocol.io/specification/2026-07-28/client/elicitation)) has two modes.
  - **Form** mode collects structured data with a flat schema. "Servers MUST NOT use form mode elicitation to request sensitive information such as passwords, API keys, access tokens".
  - **URL** mode sends the user to an out-of-band URL, for example for auth. It was new in 2025-11-25, and the spec says it "may change in future protocol revisions".
  - In 2026-07-28, elicitation is delivered through MRTR.
- **Sampling** and **Roots**: Deprecated in 2026-07-28.
- **MRTR `requestState`**: "servers MUST treat `requestState` as an attacker-controlled input. If `requestState` influences authorization, resource access, or business logic, servers MUST protect its integrity (e.g. HMAC or AEAD)" ([mrtr source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/specification/2026-07-28/basic/patterns/mrtr.mdx)).
- **Tasks**:
  - 2025-11-25: experimental core feature.
  - 2026-07-28: moved to the official extension `io.modelcontextprotocol/tasks`, specified in [ext-tasks](https://github.com/modelcontextprotocol/ext-tasks) (polling with `tasks/get`, input with `tasks/update`).
  - Tasks are not part of the stable core.
  - The Go SDK roadmap lists Tasks only as "experimental" work ([ROADMAP.md](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/ROADMAP.md)).
- **Extensions**: MCP Apps (`io.modelcontextprotocol/ui`), Skills, and the auth extensions. The support matrix shows MCP Apps in Claude Desktop and Cursor ([client-matrix](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/extensions/client-matrix.mdx)).

## 5. Go libraries

Evidence collected on 2026-09-29:

- `gh api repos/<repo>` and `gh api repos/<repo>/releases`.
- `go list -m -versions` against the module proxy.
- `go mod download` for the go.mod contents.
- `go list -deps <pkg>` in throwaway modules to find the third-party packages that a server import links.

| | modelcontextprotocol/go-sdk | mark3labs/mcp-go | metoro-io/mcp-golang | ThinkInAIXYZ/go-mcp |
| --- | --- | --- | --- | --- |
| Module path | `github.com/modelcontextprotocol/go-sdk` | `github.com/mark3labs/mcp-go` | `github.com/metoro-io/mcp-golang` | `github.com/ThinkInAIXYZ/go-mcp` |
| Latest tag | v1.8.0, 2026-09-14 | v1.1.1, 2026-09-23 | v0.16.1, 2026-02-25 | v0.2.29, 2026-06-29 |
| v1 stable since | v1.0.0, 2025-09-30 | v1.0.0, 2026-09-02 | no | no |
| Newest revision | 2026-07-28 (since v1.7.0) | 2026-07-28 (since v1.0.0, PR #951) | 2024-11-05 hard-coded in `server.go` | `Version = "2025-03-26"` in `protocol/types.go` |
| Server transports | stdio, Streamable HTTP (stateful and stateless), legacy SSE, in-memory, custom | stdio, Streamable HTTP (with `WithStateLess`), SSE | stdio, stateless HTTP | stdio, SSE, Streamable HTTP (per README; not inspected) |
| `go` directive | 1.25.0 | 1.25.5 | 1.21 | 1.18 |
| go.mod requires | 8 direct + 3 indirect | 7 direct + 6 indirect (testify, go-internal as direct) | invopop/jsonschema, pkg/errors, testify, … | uuid, concurrent-map, gjson, uritemplate, … |
| Third-party modules linked by the server package | jsonschema-go, segmentio/encoding (+asm, x/sys), uritemplate, x/oauth2, x/sync, x/time | uuid, jsonschema-go, spf13/cast, uritemplate, x/text, santhosh-tekuri/jsonschema | not measured | not measured |
| Owner | `modelcontextprotocol` org; repo description: "Maintained in collaboration with Google." Top committers: findleyr (202), jba (180) | `mark3labs` org; original author Ed Zynda | Metoro | ThinkInAIXYZ |
| Stars / last push | 5,169 / 2026-09-29 | 9,151 / 2026-09-23 | 1,229 / 2026-02-25 | 676 / 2026-08-16 |
| Official tier | Tier 1 ([SDK page source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/docs/2026-07-28/sdk.mdx)) | none (third party) | none | none |

Notes:

- **metoro-io/mcp-golang is stale.** The last `main` commits before the v0.16.1 hotfix date from 2025-09-02. The single 2026 release fixes a map race. The protocol version is hard-coded to 2024-11-05. It has no v1.
- **ThinkInAIXYZ/go-mcp** is maintained but stops at 2025-03-26.
- **mark3labs/mcp-go** is a credible alternative. It supports 2026-07-28, typed handlers (`mcp.NewTypedToolHandler`, `NewStructuredToolHandler`), schema helpers (`WithInputSchema[T]`, `WithOutputSchema[T]`), annotation options (`WithReadOnlyHintAnnotation`, `WithDestructiveHintAnnotation`), `server.ServeStdio`, stateless Streamable HTTP, DNS-rebinding protection, and tasks (module source at v1.1.1). Its README still says it "implements … version 2025-11-25" (README line 209), which is out of date compared with its v1.0.0 release notes. Adopting it would raise this repository's `go` directive from 1.25.0 to at least 1.25.5, and it links `spf13/cast` and a second JSON Schema validator.
- Sources: [go-sdk v1.8.0 go.mod](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/go.mod), [mcp-go v1.1.1 go.mod](https://github.com/mark3labs/mcp-go/blob/v1.1.1/go.mod), [mcp-go v1.0.0 release](https://github.com/mark3labs/mcp-go/releases/tag/v1.0.0), [mcp-golang](https://github.com/metoro-io/mcp-golang), [go-mcp](https://github.com/ThinkInAIXYZ/go-mcp).

### Official SDK in detail (v1.8.0)

The go.mod (`~/go/pkg/mod/cache/download/github.com/modelcontextprotocol/go-sdk/@v/v1.8.0.mod`, module sum `h1:KIvahhYqwtbeniWVPs3TcXEA7b8jEtwfBpOTAI+Urx4=`) is:

```
go 1.25.0
require (
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/go-cmp v0.7.0
	github.com/google/jsonschema-go v0.4.3
	github.com/segmentio/encoding v0.5.4
	github.com/yosida95/uritemplate/v3 v3.0.2
	golang.org/x/oauth2 v0.35.0
	golang.org/x/time v0.15.0
	golang.org/x/tools v0.42.0
)
require (
	github.com/segmentio/asm v1.1.3 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)
```

`go get github.com/modelcontextprotocol/go-sdk/mcp@v1.8.0` in an empty module added 9 module requirements (go-sdk plus jsonschema-go, segmentio/asm, segmentio/encoding, uritemplate, x/oauth2, x/sync, x/sys, x/time). `jwt`, `go-cmp`, and `x/tools` are not needed to build the `mcp` package.

**Stability and compatibility.**

- The v1.0.0 release notes say: "going forward we won't make breaking API changes" ([v1.0.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.0.0)).
- The README version table says v1.7.0+ supports "2026-07-28, 2025-11-25\*, 2025-06-18, 2025-03-26, 2024-11-05". The asterisk marks client-side OAuth as experimental ([README](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/README.md)).
- The v1.8.0 release notes say it "adds no new protocol revision". The release hardens resource limits: JSON nesting depth, an SSE event size cap, and `StdioTransport.MaxLineLength`. It also adds `ServerOptions.SupportedProtocolVersions` and `ServerOptions.SetCacheable` ([v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0)).
- Behaviour changes are gated by `MCPGODEBUG` flags ([docs/mcpgodebug.md](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/docs/mcpgodebug.md)).

**API shape**, from the [README](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/README.md), [docs/server.md](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/docs/server.md), [docs/protocol.md](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/docs/protocol.md), and [mcp/*.go](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0/mcp):

- **Server and tools**:
  - `mcp.NewServer(*Implementation, *ServerOptions)` creates the server.
  - `mcp.AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])`. The handler shape is `func(ctx, *CallToolRequest, In) (*CallToolResult, Out, error)`.
  - `AddTool` infers `InputSchema` from `In` and `OutputSchema` from `Out` (unless `Out` is `any`), using `jsonschema` struct tags for descriptions. It validates arguments and output. It "marshal[s] the Out value into the result's `StructuredContent`, as well as the unstructured `Content`". It turns a returned `error` into `IsError: true`.
  - `AddTool` panics on schema errors (`server.go:603`).
  - For hand-written validation, use `Server.AddTool`.
- **`Tool` struct fields**: `Name`, `Title`, `Description`, `Icons`, `InputSchema`, `OutputSchema`, `Annotations *ToolAnnotations`, `Meta` (`protocol.go:1895`).
- **`ToolAnnotations`** (`protocol.go:1964`):
  - `ReadOnlyHint` and `IdempotentHint` are plain `bool`. `DestructiveHint` and `OpenWorldHint` are `*bool`, because their spec default is true.
  - Since v1.7.0, false-valued plain bools are serialized explicitly. `MCPGODEBUG=hintomitempty=1` restores the old behaviour.
  - `docs/rough_edges.md` notes that all fields "should have all fields typed as `*bool`".
- **Transports**:
  - `mcp.StdioTransport{}` (with `MaxLineLength`) and `server.Run(ctx, transport)`.
  - `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server, *StreamableHTTPOptions)` returns an `http.Handler`.
  - Legacy `mcp.NewSSEHandler`, plus `CommandTransport` and `InMemoryTransport` (clients and tests).
- **Dual era**:
  - "In both models, the SDK exposes the same API."
  - `Server.Connect` registers `server/discover` automatically.
  - Handlers can read `ServerRequest[P].ProtocolVersion()`, `ClientInfo()`, and `ClientCapabilities()`.
- **Stateless HTTP**:
  - `StreamableHTTPOptions.Stateless` means the handler "does not read or set the Mcp-Session-Id header". "In Stateless mode, GET and DELETE requests return 405".
  - "the streamable HTTP transport accepts requests at protocol version `2026-07-28` **only** when `Stateless = true`". A stateful handler answers such requests with `-32022 UnsupportedProtocolVersion` and lists only the legacy versions (`streamable.go:1505-1550`).
  - `JSONResponse: true` returns `application/json` instead of SSE.
- **DNS rebinding and Origin**:
  - By default, "requests arriving via a localhost address (127.0.0.1, [::1]) that have a non-localhost Host header are rejected with 403 Forbidden". Opt out with `DisableLocalhostProtection`.
  - The SDK does **not** check `Origin` by default. `StreamableHTTPOptions.CrossOriginProtection` is deprecated. The doc comment says to wrap the handler with `http.NewCrossOriginProtection().Handler(handler)` instead (`streamable.go:184-195`). `mcpgodebug.md` says: "A nil `StreamableHTTPOptions.CrossOriginProtection` now always means no cross-origin protection."
  - `net/http.CrossOriginProtection` checks `Sec-Fetch-Site`, or compares `Origin` with `Host`. It rejects with 403. Requests without either header "are assumed to be either same-origin or non-browser requests, and are allowed" (`go doc net/http.CrossOriginProtection`, Go 1.27.1).
  - `MaxRequestBodyBytes` defaults to `DefaultMaxRequestBodyBytes`.
- **Auth middleware**:
  - `auth.RequireBearerToken(verifier TokenVerifier, opts *RequireBearerTokenOptions) func(http.Handler) http.Handler`. It checks expiry and `Scopes` and sets `WWW-Authenticate` with `ResourceMetadataURL`.
  - `auth.ProtectedResourceMetadataHandler(*oauthex.ProtectedResourceMetadata)` serves RFC 9728 metadata.
  - Tool handlers read `req.Extra.TokenInfo`.
  - Setting `TokenInfo.UserID` binds sessions to users.
  - Client side: `auth.AuthorizationCodeHandler` (CIMD, pre-registered, DCR, RFC 9207) and `extauth.EnterpriseHandler`.
- **Elicitation and MRTR**:
  - Handlers may return `CallToolResult{InputRequests: mcp.InputRequestMap{"k": &mcp.ElicitParams{…}}, RequestState: "…"}` and read `req.Params.InputResponses` on the retry.
  - For clients older than 2026-07-28, `serverMultiRoundTripMiddleware` fulfils the requests through the legacy `ServerSession.Elicit`/`CreateMessage`/`ListRoots` calls and re-invokes the handler once ([docs/server.md "Multi Round-Trip Requests"](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/docs/server.md)).
  - `ServerSession.Elicit` remains available.
- **Other**:
  - Resources, templates, prompts, completion, and progress (`req.Session.NotifyProgress`).
  - Cancellation through `ctx`.
  - Middleware (`AddReceivingMiddleware`).
  - `SchemaCache` for per-request servers.
  - Default capabilities advertise `logging`. To advertise nothing, set `Capabilities: &ServerCapabilities{}` (`rough_edges.md`).

**Example 1: stdio server with one typed tool.** This is verbatim from the [README](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/README.md):

```go
package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Input struct {
	Name string `json:"name" jsonschema:"the name of the person to greet"`
}

type Output struct {
	Greeting string `json:"greeting" jsonschema:"the greeting to tell to the user"`
}

func SayHi(ctx context.Context, req *mcp.CallToolRequest, input Input) (
	*mcp.CallToolResult,
	Output,
	error,
) {
	return nil, Output{Greeting: "Hi " + input.Name}, nil
}

func main() {
	// Create a server with a single tool.
	server := mcp.NewServer(&mcp.Implementation{Name: "greeter", Version: "v1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"}, SayHi)
	// Run the server over stdin/stdout, until the client disconnects.
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
```

**Example 2: Streamable HTTP wired into `net/http`.** This is verbatim from [examples/http/main.go](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/examples/http/main.go) (`runServer`):

```go
	// Create the streamable HTTP handler.
	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return server
	}, nil)

	log.Printf("MCP server listening on %s", url)
	log.Printf("Available tool: cityTime (cities: nyc, sf, boston)")

	// Start the HTTP server.
	if err := http.ListenAndServe(url, handler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
```

The official example passes `nil` options, so the handler is stateful and cannot serve 2026-07-28 requests. It also has no Origin check. The documented options that change this are shown in the sketch below. It is assembled from the documentation quoted above, **not copied from an official example and not compiled**:

```go
h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
	&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(h))
// listen on 127.0.0.1 only
```

## 6. Client integration facts

| Client | Local stdio | HTTP URL | Notes | Source |
| --- | --- | --- | --- | --- |
| Claude Code | `claude mcp add --transport stdio <name> -- <cmd> [args]`. An entry with no `type` is read as stdio. | `claude mcp add --transport http <name> <url>`. `type` `http` or `streamable-http`. SSE deprecated. | See the Claude Code notes below. | [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp) |
| Claude Desktop | `mcpServers` with `command`/`args` in `~/Library/Application Support/Claude/claude_desktop_config.json` | Custom Connectors are brokered from Anthropic's cloud: "The connection to your MCP server originates from Anthropic's servers". A loopback URL is not reachable. | stderr is written to `~/Library/Logs/Claude/mcp-server-NAME.log` | [connect-local-servers source](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/046fa30efd374370afb87ef830bd788eac5f217e/docs/docs/2026-07-28/develop/connect-local-servers.mdx), [Anthropic help: custom connectors](https://support.anthropic.com/en/articles/11175166-getting-started-with-custom-connectors-using-remote-mcp) |
| Cursor | `mcpServers` with `command`/`args`/`env`/`envFile` in `~/.cursor/mcp.json` or `.cursor/mcp.json` | `url` (+`headers`) for SSE or Streamable HTTP | Lists Tools, Prompts, Resources, Roots, Elicitation, and Apps as supported. The desktop OAuth redirect is fixed at `http://localhost:8787/callback`. | [cursor.com/docs/context/mcp](https://cursor.com/docs/context/mcp) |
| pi | `command`/`args`/`env`/`cwd` in `~/.pi/agent/mcp.json` or `.pi/mcp.json`; `pi mcp add <name> -- <cmd>` | `url` (+`headers`, `oauth`). SSE is rejected. | Tools are named `mcp__<server>__<tool>`. Server log notifications go to `~/.pi/agent/mcp.log`. | [earendil-works/pi docs/mcp.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md) |

Claude Code notes (all from [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp)):

- The "v2 runtime" is built on TypeScript SDK 2.0 and "adds MCP protocol revision 2026-07-28".
- On v2 it "Asks HTTP servers whether they support the newer revision". It asks stdio servers only when `MCP_PROTOCOL_NEGOTIATION=auto`.
- Output limits: it warns above 10,000 tokens and caps output at 25,000 by default (`MAX_MCP_OUTPUT_TOKENS`). A tool can raise its own limit with `_meta["anthropic/maxResultSizeChars"]`.
- `_meta["anthropic/requiresUserInteraction"]: true` forces a permission prompt on every call.
- Elicitation form and URL modes are supported. The `Elicitation` hook can auto-respond to them.

In practice, stdio works in all four clients, and it is the only local transport for Claude Desktop. Legacy-revision clients are the default for stdio in Claude Code, so a server must serve both eras.

## 7. Recommendation for agentmailguard

### Facts that constrain the choice

- The repository's `go.mod` has `go 1.25.0`. go-sdk v1.8.0 requires `go 1.25.0`. mcp-go v1.1.1 requires `go 1.25.5`.
- The official SDK is Tier 1, v1-stable, and supports 2026-07-28 and every legacy revision on one API. Its `mcp` package links 7 third-party modules. Two of them (`golang.org/x/sys`, `golang.org/x/sync`) are `golang.org/x` modules.
- The spec says stdio servers should get credentials "from the environment" and not use OAuth. Security best practices recommend stdio for local servers to "limit access to just the MCP client".
- For HTTP, the spec requires Origin validation (403) and recommends loopback binding and authentication. The SDK blocks non-localhost `Host` on loopback by default, but checks Origin only when you wrap it with `http.CrossOriginProtection`. The SDK serves 2026-07-28 over HTTP only when `Stateless: true`.
- Annotations are hints, and clients must treat them as untrusted. Servers MUST "Sanitize tool outputs". Clients SHOULD keep a human in the loop.
- Form elicitation MUST NOT request secrets. Elicitation answers come from the host, and Claude Code's `Elicitation` hook can auto-respond. MRTR `requestState` is attacker-controlled.
- Roots, Sampling, and Logging are deprecated. The spec suggests stderr instead of Logging.
- Cursor desktop's fixed OAuth callback is `http://localhost:8787/callback`. The guard's default listener is `127.0.0.1:8787`.

### Judgment

1. **Library.** Use `github.com/modelcontextprotocol/go-sdk` at v1.8.0, pinned. It needs no `go` directive bump. It has the smallest linked set of the maintained options. It supports both protocol eras. The official project maintains it. Reject mark3labs/mcp-go for this project because it forces `go 1.25.5`, adds `spf13/cast` and a second schema validator, and has a stale README on protocol support. Reject metoro-io and ThinkInAIXYZ because they are stale or behind on the protocol. Record the dependency approval before adding it, as `AGENTS.md` requires.
2. **Transport.** Add an MCP **stdio** subcommand to the CLI. It is a thin adapter: each MCP tool maps to one existing `/v1/call` operation through `internal/client`.
   - The MCP process holds no provider credentials and no OpenRouter key. Screening stays on the server.
   - Authorization in the spec does not apply to stdio.
   - The attack surface is the same as today's CLI.
   - Do not add Streamable HTTP to the server until client/server authentication exists. If it is added later:
     - mount it at `/mcp` on the existing loopback listener;
     - use `Stateless: true` and `JSONResponse: true`;
     - wrap it in `http.NewCrossOriginProtection().Handler`;
     - keep localhost protection on;
     - set `MaxRequestBodyBytes`;
     - require a bearer token checked by `auth.RequireBearerToken`.
   - The 8787 default conflicts with Cursor's desktop OAuth callback port. This matters only while Cursor runs an OAuth flow, and this research did not verify how long Cursor keeps the port open.
3. **Tool shape.** Use `mcp.AddTool` with typed `In`/`Out` structs, so input and output schemas are inferred and validated.
   - Keep input schemas flat objects without root-level `anyOf`/`oneOf`/`allOf`. Claude Code rewrites or drops such tools.
   - Name tools with `[a-z_]` only (for example `mail_list`, not `mail.list`). The spec allows dots, and this research did not verify that every client accepts them.
   - Set `Title` and a precise `Description` on every tool.
   - Advertise only the capabilities in use: set `Capabilities` explicitly, so the default `logging` capability is not advertised.
4. **Annotations.** Always set every hint explicitly. `DestructiveHint` and `OpenWorldHint` default to true when they are missing.

   | Operation class | readOnlyHint | destructiveHint | idempotentHint | openWorldHint |
   | --- | --- | --- | --- | --- |
   | `mail.list`, `mail.search`, `mail.get`, `mail.link`, `calendar.list`, `calendar.get` (return email-derived content) | true | (n/a) false | (n/a) true | true (content is from arbitrary third parties) |
   | `mail.folders`, `mail.categories`, `rules.list`, `auth.status` | true | false | true | false |
   | `mail.read`, `mail.unread`, `mail.flag`, `mail.categorize` | false | false (reversible metadata) | true | false |
   | `mail.archive`, `mail.move` | false | false (reversible move) | true | false |
   | `mail.trash`, and `mail.organize` when it can trash | false | **true** | true | false |
   | `draft.new`, `draft.reply`, `draft.forward` | false | false (additive) | false | false |
   | `draft.update` | false | true (overwrites draft content) | true | false |

   Annotations do not enforce anything. For trash, also consider Claude Code's `_meta["anthropic/requiresUserInteraction"]: true`. This is Claude Code-specific. Do not expose send or permanent delete.
5. **`outputSchema`.** Use it. Return typed `Out` values so clients get `structuredContent` that the SDK validates against the inferred schema, plus the JSON `TextContent` fallback that the spec asks for. Keep outputs small and use a tool-level cursor argument for lists, because Claude Code caps output at 25k tokens by default.
6. **Untrusted content.**
   - Return a body only when the server's screening verdict allows it. Missing or failed screening is a **tool execution error** (`isError: true`) whose message contains no email-derived text.
   - Put email-derived text only in clearly named fields, separate from guard-authored fields, for example `untrusted_body` next to `screening` and `ids`. Say in the tool description that these fields are untrusted third-party text.
   - The spec's `audience`/`priority` content annotations are available, but no source found here says any client uses them for trust.
   - Use message and folder IDs as the explicit handles the spec's "Stateful Tools" guidance describes, and validate them on every call. Move destinations must still come from `folders`.
   - Do not use sampling, because it would route email to the host's model. Roots, Logging, and Tasks are not needed. Log to stderr, and never log subjects, senders, bodies, or tokens.
   - Do not rely on elicitation as a human-in-the-loop control for destructive operations, because the host can auto-answer it. Use it only for non-secret confirmations, if at all. Integrity-protect any `requestState`.
   - Expose tools only at first. Resources could carry bodies, but the tool path already has one screening chokepoint.
7. **Versioning.** Keep the SDK default of advertising all supported revisions, so Claude Code (legacy on stdio by default), Claude Desktop, Cursor, and pi all connect. Test with the SDK's `InMemoryTransport`/`CommandTransport` clients at both 2025-11-25 and 2026-07-28.

## Limits of this research

- The specification was read as MDX and TypeScript source in the spec repository at commit `046fa30e`, not as the rendered pages on modelcontextprotocol.io. The rendered pages should match, but this was not checked page by page.
- SEP documents were not read in full. Change descriptions come from the changelogs.
- SDK behaviour comes from reading source, docs, and examples at v1.8.0. No SDK code was compiled or run. In particular, this research did not test the stateless-only 2026-07-28 HTTP behaviour, the MRTR legacy fallback, or the annotation serialization.
- Dependency counts come from `go.mod` files and `go list -deps` on one package in a throwaway module. They are not binary-size or license audits. The mark3labs/mcp-go and third-party SDK feature claims come from READMEs, release notes, and targeted source greps, not full reviews.
- Client facts come from vendor docs fetched on 2026-09-29, and these clients change quickly. This research did not verify the following:
  - which protocol revision Cursor, Claude Desktop, or pi negotiates;
  - whether any client auto-approves tools because of `readOnlyHint`;
  - whether any client rejects dots in tool names;
  - how long Cursor holds port 8787.
- Stars and activity figures are GitHub API snapshots and do not measure quality.
- pkg.go.dev pages were not fetched. Module versions come from the Go module proxy (`go list -m -versions`), and API facts come from the downloaded module source.
