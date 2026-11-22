# Phase 3–4 — Go front door and MCP integration

**Status:** implemented, tested, opt-in
**Default:** unchanged (the Node daemon)
**Enable:** `BROWSER_MCP_DAEMON=go`

## What was built

`packages/mcp/bin/browsermcpd` is a Go front door for Browser MCP. It owns:

- the public loopback port (9010–9012, or `--port`);
- `daemon.json`, the token, the log and the idle-exit timer;
- the HTTP/WebSocket server: `/browsermcp/status`, `/client`, `/extension`,
  `/shutdown`;
- the security checks: Host header (DNS rebinding), token (constant-time) on
  `/client` and `/shutdown`, extension Origin allowlist (`chrome-extension://`
  - Web Store ID, self-built ID, `BROWSER_MCP_EXTENSION_IDS`, dev IDs file),
    loopback-only bind, 256 MB payload cap, and no `/cdp` exposure.

It proxies `/client` and `/extension` **without parsing frames** to the existing
TypeScript daemon, which it starts lazily as an internal engine worker:

```
AI app ──stdio──▶ thin process (TypeScript) ──ws /client──▶ Go front door
                                                              ├─ lifecycle, daemon.json, token, Host/Origin checks
                                                              ├─ ws /extension ──proxy──▶ Node engine worker
                                                              └─ ws /client    ──proxy──▶ Node engine worker
                                                                                              ├─ RelayBackend
                                                                                              ├─ Playwright / CDP bridge
                                                                                              └─ all 28 browser commands
```

The worker listens on a random internal loopback port. The front door passes
`--advertised-port <public port>` so the extension's pairing signature names
the port the extension actually dialed, not the internal one. The worker's
token stays internal; the thin process only sees the front door's token.

Because the proxy is transparent, every existing protocol detail keeps working:
the client hello/welcome, JSON-RPC framing, reverse `readFile`/`writeFile`
requests, extension pairing, ping/pong, close codes and all browser commands.

## Measured result

Same machine (Fedora, Node 26.13.1, Go 1.27.4), medians of three runs:

| Metric                      | Node daemon (Phase 2) |                                            Go front door |
| --------------------------- | --------------------: | -------------------------------------------------------: |
| Cold start to `/status`     |              207.6 ms |                                              **21.2 ms** |
| Idle RSS, no client         |               70.7 MB |                                               **9.9 MB** |
| RSS after first RPC         |               70.7 MB | 14.5 MB (front door) / **90.7 MB** (front door + worker) |
| First `hello` → `welcome`   |                5.5 ms |                              88.9 ms (starts the worker) |
| `tools/list` p50 / p95      |        0.15 / 0.36 ms |                                           0.12 / 0.23 ms |
| `tools/call help` p50 / p95 |        0.21 / 0.74 ms |                                           0.15 / 0.28 ms |
| 50 concurrent `tools/list`  |                1.8 ms |                                                   1.4 ms |

**Interpretation.** The front door is much cheaper when nothing is using the
browser. The moment a client (or the extension) connects, the Node worker
starts and the process tree costs more than the Node daemon alone (~91 MB vs
~71 MB) because both processes are resident. The Go front door therefore buys
startup and idle cost, not browser-time memory. It also adds ~85 ms to the
first client connection while the worker starts.

A full Go engine (no Node worker) is the only way to keep the memory win while
the browser is in use. That is the Phase 4+ engine port described in the Phase 1
audit: it requires replacing Playwright's locator/actionability/ARIA semantics
over raw CDP and passing the existing conformance suite. It is **not** part of
this delivery.

## Usage

```sh
# Build the Go binary to bin/; dist/daemon.js is one level up
pnpm --filter @browsermcp/mcp go:build

# Run the thin process with the Go front door
BROWSER_MCP_DAEMON=go node packages/mcp/dist/index.js

# Override the binary location
BROWSER_MCP_DAEMON=go \
BROWSER_MCP_GO_DAEMON=/path/to/browsermcpd \
  node packages/mcp/dist/index.js
```

In an AI app's MCP config, set the same environment variables on the command
that runs `@browsermcp/mcp`.

Run a front door by hand:

```sh
BROWSER_MCP_HOME=$(mktemp -d) \
  packages/mcp/bin/browsermcpd --port 9411 --stderr --engine "node packages/mcp/dist/daemon.js"
```

## Tests

Go (config, proxy, relay security, and two end-to-end tests):

```sh
pnpm --filter @browsermcp/mcp go:test   # go test -race ./...
```

A real browser/extension smoke test passes 20/20 with both daemons; see
[`e2e-smoke-report.md`](e2e-smoke-report.md).

The end-to-end tests require built artifacts:

1. `TestFrontDoorWithNodeWorker` starts the Go front door in-process, which
   starts the real Node worker, then speaks the client protocol over `/client`:
   hello/welcome, `tools/list`, `tools/call help`.
2. `TestThinProcessUsesGoFrontDoor` runs the built thin process with
   `BROWSER_MCP_DAEMON=go` and speaks MCP over stdio: `initialize`,
   `tools/list`, `tools/call help`. This is the full
   AI-client → thin → Go → Node worker path.

TypeScript: the existing suite still passes, including three new daemon tests:
the pairing signature names the advertised port when a front door proxies the
daemon, `idleExitMs: 0` never schedules an idle exit, and the worker stops when
the front door disappears. The Go relay suite also covers an extension ID that
only arrives in a client `hello` (`TestClientHelloExtensionIDsAreAllowed`).

Benchmark:

```sh
pnpm --filter @browsermcp/mcp bench -- --bin packages/mcp/bin/browsermcpd
```

## Security review

| Control                     | Front door                                                    | Worker                                                                      |
| --------------------------- | ------------------------------------------------------------- | --------------------------------------------------------------------------- |
| Loopback bind               | `127.0.0.1` only                                              | `127.0.0.1` only                                                            |
| Host header / DNS rebinding | checked before every request                                  | checked                                                                     |
| Token on `/client`          | constant-time compare                                         | worker token, internal only                                                 |
| Token on `/shutdown`        | constant-time compare, no Origin                              | `POST /shutdown`                                                            |
| Extension Origin            | `chrome-extension://` + allowlist                             | its own allowlist + dev IDs                                                 |
| `/cdp`                      | **404** (not exposed)                                         | internal Playwright connection only                                         |
| Payload cap                 | 256 MB                                                        | 256 MB                                                                      |
| Idle exit                   | front door owns it; Linux gets `Pdeathsig` + `--no-idle-exit` | the worker also checks the front door every 15 s and exits after two misses |

The front door does not weaken any extension-enforced rule: pairing, incognito,
Stop, browser pages, file access and tab restrictions are still enforced by the
extension and the worker.

## Limitations and rollback

- **Abrupt front-door death:** on Linux the worker gets `Pdeathsig`; all
  platforms also check the front door every 15 s and exit after two misses, so
  the worker cannot outlive it.
- The engine is still TypeScript/Playwright. A full Go engine is a separate,
  larger project; until then, browser-time memory is higher with the hybrid.
- The front door adds one local WebSocket hop; measured RPC latency remains
  sub-millisecond.
- Packaging the binary for npm (platform matrix, signing) is not done.
- Rollback: unset `BROWSER_MCP_DAEMON` (or remove it from the MCP config). The
  Node daemon is still the default and needs no changes. Delete
  `packages/mcp/bin/browsermcpd` to remove the artifact.
