# Architecture

Browser MCP drives the user's own Chrome through an extension that relays
`chrome.debugger`. This document describes the **native Go engine**: the daemon
and browser automation implemented in Go, with no Node.js or Playwright in the
production path when it is enabled. The TypeScript daemon remains the default
and the rollback path (see [`docs/migration/ROLLBACK.md`](../migration/ROLLBACK.md)).

## Goals and constraints

1. Lower runtime memory and startup cost than the Node + Playwright daemon.
2. Keep the Chrome extension unchanged: same protocol, pairing, permission
   controls, Stop/Resume and tab rules.
3. Keep the thin MCP process unchanged: it still speaks MCP over stdio and
   forwards to a daemon on a loopback port.
4. Preserve observable command behaviour and error semantics.
5. Do not weaken any security control.

Non-goals: replacing the extension, changing the MCP tool schema, or making the
Go engine the default before parity is demonstrated.

## Processes

```
AI app ──stdio (MCP)──▶ thin process (TypeScript, one per AI app, unchanged)
                          │ tools/list, tools/call over ws /client?token=…
                          ▼
                 native Go daemon (browsermcpd --native-engine)
                   ├─ relay: loopback HTTP + WebSocket, Host/token/Origin checks
                   ├─ client protocol: hello/welcome, RPC, readFile/writeFile
                   ├─ extension connection: pairing, requests, events, ping
                   ├─ CDP transport: attach/detach, commands, child sessions
                   ├─ browser registry: slots, public tab IDs, claims
                   └─ command engine: parser, accessibility snapshots,
                      locators, actions, output policy
                          ▲ ws /extension
                 MV3 extension (unchanged) ── chrome.debugger ──▶ tab
```

The daemon is still one process per user, still bound to `127.0.0.1`, and still
owns `~/.browsermcp/daemon.json`, `identity.json` and the idle-exit timer.

Two other modes remain:

| Mode                                                          | Daemon                        | Browser engine          |
| ------------------------------------------------------------- | ----------------------------- | ----------------------- |
| Default                                                       | Node (`dist/daemon.js`)       | TypeScript + Playwright |
| Go front door (`BROWSER_MCP_DAEMON=go`)                       | Go, proxying to a Node worker | TypeScript + Playwright |
| Native (`BROWSER_MCP_DAEMON=go`, `BROWSER_MCP_ENGINE=native`) | Go                            | Go                      |

## Why a modular monolith

The daemon's work is one user's browser: a handful of connections, at most eight
Chrome profiles, one command at a time per tab. Splitting it into services would
add IPC and operational cost without buying throughput. The Go binary is divided
into packages with explicit interfaces (`internal/...`), not processes.

## Layer responsibilities

### Relay (`go/internal/relay`)

Owns the loopback listener and the security boundary:

- binds `127.0.0.1` only;
- checks the `Host` header against DNS rebinding;
- requires the token on `/client` and `/shutdown`, compared in constant time;
- allowlists extension origins (Web Store ID, self-built ID,
  `BROWSER_MCP_EXTENSION_IDS`, `dev-extension-ids`);
- caps WebSocket payloads at 256 MB;
- serves `/browsermcp/status` with CORS for `chrome-extension://`.

For the native engine it hands accepted sockets to the engine instead of
proxying them. `/cdp` is not exposed.

### Client protocol (`go/internal/client`)

Speaks the existing thin-process contract: `hello` → `welcome`, then
JSON-RPC-style `tools/list` and `tools/call`, plus the reverse `readFile` /
`writeFile` requests the engine uses for uploads, screenshots, PDFs and
downloads. A client newer than protocol 1 is closed with `update-server`, as the
Node daemon does.

### Extension connection (`go/internal/extension`)

One accepted WebSocket per Chrome profile:

- reads `hello`, checks the protocol, sends `welcome` with the daemon's public
  key, a commitment to a fresh nonce, and an ECDSA P-256 signature over
  `browsermcp-pair-v1:<port>:<challenge>:<commitment>`;
- answers `pairingNonce` with `pairingReveal` and computes the six-digit code;
- correlates requests and responses, dispatches events, and pings every 20 s;
- closes a connection that stops answering, and rejects a second connection for
  a browser that is still answering.

Byte formats are verified against Node in
`go/internal/pairing/identity_test.go`: Node verifies a Go signature and derives
the same SPKI, commitment and pairing code, and Go signs with a Node-made key.

### CDP transport (`go/internal/cdp`)

Each claimed tab is a `cdp.Tab`: `attach` gives Chrome's target info, then every
command is one extension `cdp` request carrying `{tabId, sessionId?, method,
params}`. Because the extension's request/response already pairs a command with
its answer, there is no second CDP id space and no bridge process. Child sessions
(out-of-process iframes) arrive as events with a `sessionId` and are addressed
with the same `sessionId` on later commands.

`Target.setAutoAttach` is requested best-effort on claim; targets that refuse it
still work. Chrome reporting "not attached" marks the tab detached so the next
use claims it again.

### Browser registry (`go/internal/browser`)

The native replacement for `RelayBackend` + `BrowserSlot`:

- one slot per Chrome profile, identified by `browserId` or, for older
  extensions, by user agent and window IDs;
- public tab IDs unique across browsers (Chrome's own when free, otherwise a
  spare above 100000), retired rather than reused when Chrome renumbers tabs;
- a claim cache: claiming attaches the debugger and returns a CDP session;
  releasing detaches it; a detach, close or disconnect drops the claim;
- `unavailableReason()`, readiness (a just-connected browser is given a short
  grace period to report its paired state), Stop, and browser selection.

Only the source-of-truth state (`state` events) is authoritative for the tab
list; a new tab is assigned a public ID immediately so `goto` can use it before
the extension reports it.

### Command engine (`go/internal/engine`)

Sessions (one per thin process), per-session serialization, a lock per tab, and
the tool's output policy:

- every line is parsed before the first runs, so a bad line runs none of the
  call;
- one `✓` line per action, read output below it, `✗ step N` on the first error,
  `Steps N–M not run`, and a `Page:` trailer;
- a response cap of 80,000 characters with a budget for read output;
- reads are retried (up to three times, 150 ms apart) when the debugger
  re-attached; actions are never repeated;
- dialogs block later steps until answered;
- idle tabs are released after 10 minutes.

Accessibility snapshots come from `Accessibility.getFullAXTree`; refs are
assigned in document order and held per tab. Locators are resolved by a
whitelisted in-page evaluator (no code from the model is executed as a locator)
and actions use `Input.dispatchMouseEvent` / `dispatchKeyEvent` after an
actionability check that ports Playwright's visible/enabled/uncovered logic.

### Daemon and lifecycle (`go/internal/daemon`, `go/internal/config`)

Port election, `daemon.json` (0600), `identity.json` (0600, created atomically so
two daemons agree on one key), the log, idle exit, `/shutdown`, signals, and
either the native engine or the Node worker.

## Concurrency

- One goroutine per connection; one author per WebSocket.
- `context` cancellation for every request; CDP commands have a five-minute cap.
- Requests are bounded by the caller's step timeout.
- A session's calls run one at a time; a tab lock prevents two sessions from
  interleaving on the same tab.
- The registry lock is always taken before a browser lock; event callbacks run
  without the registry lock.
- Claims are single-flight: concurrent uses of one tab share one attach.

## Security

The native engine preserves the controls listed in the Phase 1 audit: loopback
binding, Host/token/Origin checks, constant-time token comparison, ECDSA P-256
pairing with commitment, pairing before tabs are exposed, extension-enforced
incognito/Stop/browser-page/file rules, 0600/0700 files, and full user file
permissions for uploads and downloads (documented, unchanged).

The engine adds no new listening socket and runs no code from pages or models
as privileged Go code: locator expressions are parsed against a whitelist and
evaluated as literals in the page.

## Decisions

| #   | Decision                                                       | Reason                                                                     | Revisit when                                                    |
| --- | -------------------------------------------------------------- | -------------------------------------------------------------------------- | --------------------------------------------------------------- |
| 1   | Native engine lives in the existing Go daemon behind env/flags | Reuses the tested relay, lifecycle and security checks                     | An engine needs a process boundary                              |
| 2   | Keep the TypeScript thin process and extension                 | The extension is the security boundary; the thin process is the MCP client | MCP over stdio is implemented in Go                             |
| 3   | No Playwright, no CDP bridge in native mode                    | The bridge exists only to make Playwright believe it is Chrome             | Never for this engine                                           |
| 4   | Speak CDP directly over the extension's `cdp` request          | One fewer hop, no emulated `Browser.*`/`Target.*`                          | A needed command is missing from `chrome.debugger`              |
| 5   | Public tab IDs in the daemon, as the Node daemon does          | Chrome tab IDs collide across profiles                                     | Extensions expose stable global IDs                             |
| 6   | Locators in an in-page evaluator                               | Playwright's locator engine is not a dependency                            | A locator needs cross-origin frame access                       |
| 7   | Opt-in until parity is complete                                | The Node engine remains the rollback path                                  | The compatibility matrix is fully green and benchmarks reviewed |
