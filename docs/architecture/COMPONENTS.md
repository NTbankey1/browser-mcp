# Components

Everything below `go/` is the native daemon. Source line counts exclude tests.

## Go packages

| Package              | Files                                                                                                       |   LOC | Responsibility                                                                                                        |
| -------------------- | ----------------------------------------------------------------------------------------------------------- | ----: | --------------------------------------------------------------------------------------------------------------------- |
| `internal/pairing`   | `identity.go`                                                                                               |   250 | ECDSA P-256 identity, signature (IEEE P1363), commitment, six-digit code, atomic key creation                         |
| `internal/extproto`  | `frames.go`                                                                                                 |   285 | The extension↔daemon wire types: hello/welcome, requests, responses, events, state                                    |
| `internal/extension` | `client.go`                                                                                                 |   442 | One accepted extension connection: handshake, pairing, request correlation, events, ping, close                       |
| `internal/cdp`       | `tab.go`                                                                                                    |   278 | CDP over the extension: attach, commands, child sessions, event dispatch, detach                                      |
| `internal/browser`   | `registry.go`                                                                                               | 1,194 | Browser slots, identity on reconnect, public tab IDs, readiness, claims, tab actions                                  |
| `internal/engine`    | `engine.go`, `commands.go`, `snapshot.go`, `targets.go`, `actions.go`, `state.go`, `parser.go`, `output.go` | 5,772 | Sessions and locks, 28 commands, accessibility snapshots, locator resolution, actions, per-tab buffers, output policy |
| `internal/client`    | `protocol.go`                                                                                               |   365 | `/client`: hello/welcome, tools/list, tools/call, readFile/writeFile, app naming                                      |
| `internal/relay`     | `server.go`                                                                                                 |   415 | HTTP/WS server, Host/token/Origin checks, proxy or native hand-off                                                    |
| `internal/daemon`    | `daemon.go`                                                                                                 |   247 | Port election, daemon.json, idle exit, signals, engine wiring                                                         |
| `internal/config`    | `config.go`, `version.go`                                                                                   |   176 | Home, daemon.json, tokens, package version                                                                            |
| `internal/protocol`  | `protocol.go`                                                                                               |    47 | Paths, ports, product name, payload cap                                                                               |
| `internal/proxy`     | `proxy.go`                                                                                                  |    55 | Transparent WebSocket piping for the Go front door + Node worker mode                                                 |
| `internal/worker`    | `manager.go`, `idle*.go`, `pdeathsig*.go`                                                                   |   280 | Starts/stops the Node engine worker (front-door mode only)                                                            |
| `cmd/browsermcpd`    | `main.go`                                                                                                   |    76 | Flags, signals, version                                                                                               |

Tests: 1,852 lines across `internal/engine`, `internal/pairing`,
`internal/integration`, `internal/relay`, `internal/proxy`, `internal/config`.

## Engine internals

| File          | Responsibility                                                                                                                                               |
| ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `engine.go`   | Session state, per-session/tab locking, the run loop, output budget, URL notes, retry-after-reattach, idle release, browser choice, readiness                |
| `commands.go` | The 28 command definitions and their implementations; tab/browser commands; PDF, download, upload, console, requests, raw CDP                                |
| `parser.go`   | Line splitting, tokenizer (quoted arguments, balanced brackets, regexes), flags, argument counts                                                             |
| `targets.go`  | Target classification (`e12`, locator, CSS), locator parser, in-page resolver, ref/object resolution, CDP helpers                                            |
| `snapshot.go` | `Accessibility.getFullAXTree` decoding, ref assignment, `-i` filter, renderer, `find`                                                                        |
| `actions.go`  | Actionability (visible/enabled/uncovered), mouse/keyboard input, cursor overlay, fill/type/select/check, scroll, navigation, screenshots, PDF, fetch-in-page |
| `state.go`    | Per-tab console/uncaught buffers, request log, dialogs, refs, raw-CDP bookkeeping                                                                            |
| `output.go`   | Caps, truncation, compact JSON, model-facing errors                                                                                                          |

## TypeScript components retained

| Path                                                      | Role                                                           | Changed by this migration                  |
| --------------------------------------------------------- | -------------------------------------------------------------- | ------------------------------------------ |
| `packages/mcp/src/thin/`                                  | MCP stdio server; starts/replaces the daemon; file brokering   | No                                         |
| `packages/mcp/src/commands/`, `src/relay/`, `src/daemon/` | The default Node + Playwright daemon and the front-door worker | No (user's in-flight perf edits preserved) |
| `packages/protocol/`                                      | Source of truth for the extension contract                     | No                                         |
| `packages/extension-core/`, `apps/extension/`             | Browser control and popup                                      | No                                         |

The Go wire types mirror `@browsermcp/protocol`; the pairing conformance test
keeps them byte-compatible.

## Interfaces

```go
// internal/client
type Engine interface {
    Tools(sessionID string) []engine.Tool
    OpenSession(id string, app extproto.AppInfo, capabilities []string, io engine.SessionIO)
    CloseSession(ctx context.Context, id string)
    CallTool(ctx context.Context, sessionID, name string, args map[string]any) (engine.CallToolResult, error)
}

// internal/engine
type SessionIO interface {
    ReadFile(ctx context.Context, path string) (name, mimeType string, data []byte, err error)
    WriteFile(ctx context.Context, path string, data []byte, exclusive bool) (string, error)
}

// internal/browser
type Sink interface {
    State()
    Released(tabID int, reason string)
    TabAssigned(tabID int, appID *string)
    TabOpened(tabID, openerTabID int, url string)
    Connection(connected bool)
}
```

`internal/relay` takes either an `Engine` (worker mode) or the native
`AcceptExtension` / `AcceptClient` callbacks. It never needs both.

## Data files

| File                                 | Owner                | Contents                                        |
| ------------------------------------ | -------------------- | ----------------------------------------------- |
| `~/.browsermcp/daemon.json` (0600)   | daemon               | port, pid, version, token                       |
| `~/.browsermcp/daemon-<port>.json`   | daemon with `--port` | as above                                        |
| `~/.browsermcp/identity.json` (0600) | pairing              | base64 SPKI public key, PKCS #8 PEM private key |
| `~/.browsermcp/daemon.log`           | daemon               | structured log                                  |
| `~/.browsermcp/dev-extension-ids`    | user                 | extra extension IDs, one per line               |
