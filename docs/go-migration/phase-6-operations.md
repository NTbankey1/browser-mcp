# Phase 6 — Operations, debugging and rollback

This page covers installing, running, debugging and rolling back the Go front
door. Architecture and measurements: Phases 1, 3 and 5.

## Installation

Requirements:

- Node.js 24 or later (thin process and engine worker);
- pnpm (`corepack enable`);
- Go 1.24 or later (only to build the front door);
- Chrome, Edge, Brave or another Chromium browser.

```sh
pnpm install --frozen-lockfile
pnpm build                         # thin + Node daemon (tsup cleans dist)
pnpm --filter @browsermcp/mcp go:build   # dist/browsermcpd (must run after pnpm build)
```

`pnpm build` runs `tsup` with `clean: true`, so it removes `dist/browsermcpd`.
Run `go:build` after every build, or add it to your local build script.

Enable the front door in the MCP config (or the shell that launches the server):

```json
{
  "mcpServers": {
    "browsermcp": {
      "command": "node",
      "args": ["/absolute/path/to/packages/mcp/dist/index.js"],
      "env": {
        "BROWSER_MCP_DAEMON": "go",
        "BROWSER_MCP_GO_DAEMON": "/absolute/path/to/packages/mcp/dist/browsermcpd"
      }
    }
  }
}
```

Without `BROWSER_MCP_DAEMON=go`, the thin process starts the Node daemon exactly
as before. `BROWSER_MCP_GO_DAEMON` is optional when the binary sits next to
`index.js` (the default path).

## Configuration

| Setting                     | Where               | Default                | Meaning                                           |
| --------------------------- | ------------------- | ---------------------- | ------------------------------------------------- |
| `BROWSER_MCP_HOME`          | env                 | `~/.browsermcp`        | daemon files, identity, logs                      |
| `BROWSER_MCP_DAEMON`        | env                 | unset                  | `go` starts the Go front door                     |
| `BROWSER_MCP_GO_DAEMON`     | env                 | `dist/browsermcpd`     | front-door binary path                            |
| `BROWSER_MCP_EXTENSION_IDS` | env                 | empty                  | extra extension IDs allowed on `/extension`       |
| `--port <n>`                | thin / front door   | 9010–9012              | one fixed port (set the same in the extension)    |
| `--idle-exit <duration>`    | front door          | `10m`                  | exit after this long with no client; `0` disables |
| `--engine <command>`        | front door          | `node <dir>/daemon.js` | engine worker command                             |
| `--advertised-port <n>`     | worker              | listening port         | port the extension dialed (set by the front door) |
| `--no-idle-exit`            | worker              | off                    | the front door owns idle exit                     |
| `--stderr`                  | front door / worker | off                    | also log to stderr                                |

## Development workflow

```sh
pnpm install
pnpm typecheck && pnpm lint && pnpm format
pnpm test                    # TypeScript: 194 passed, 7 skipped
pnpm build
pnpm --filter @browsermcp/mcp go:build
pnpm go:test                 # Go: go test -race ./...
pnpm --filter @browsermcp/extension smoke
pnpm --filter @browsermcp/mcp bench -- --runs 5
pnpm --filter @browsermcp/mcp bench -- --bin packages/mcp/dist/browsermcpd --runs 5
```

The Go integration tests skip when `dist/index.js` or `dist/browsermcpd` is
missing; build first for full coverage.

## Files and logs

With `BROWSER_MCP_HOME=~/.browsermcp` (or the temp directory you chose):

| Path                                           | Written by                  | Contents                   |
| ---------------------------------------------- | --------------------------- | -------------------------- |
| `daemon.json` / `daemon-<port>.json`           | front door (or Node daemon) | port, pid, version, token  |
| `daemon.log` / `daemon-<port>.log`             | front door (or Node daemon) | front-door log             |
| `engine-<publicPort>/daemon-<workerPort>.json` | engine worker               | internal worker port/token |
| `engine-<publicPort>/daemon-<workerPort>.log`  | engine worker               | engine log                 |
| `engine-<publicPort>/identity.json`            | engine worker               | pairing key                |
| `identity.json`                                | Node daemon (default mode)  | pairing key                |

## Debugging

1. **Status:** `curl http://127.0.0.1:<port>/browsermcp/status`. It returns
   `product: browsermcp`, the version, protocol and the front door's PID.
2. **Process tree:** `ps -eo pid,ppid,rss,args | grep -E 'browsermcpd|daemon.js'`.
   You should see the Go front door and, after the first client, its Node
   worker child.
3. **Front-door log:** read `daemon.log`; run the binary with `--stderr` to see
   it live.
4. **Worker log:** read `engine-<port>/daemon-<workerPort>.log`. The worker's
   start line names its internal port.
5. **Token:** `daemon-<port>.json` holds the front-door token; the worker token
   stays in `engine-<port>/daemon-<workerPort>.json`.

Common failures:

| Symptom                                           | Cause                                 | Fix                                                                                                            |
| ------------------------------------------------- | ------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `spawn ... browsermcpd ENOENT`                    | binary missing or path wrong          | run `pnpm --filter @browsermcp/mcp go:build`, or set `BROWSER_MCP_GO_DAEMON`                                   |
| `Another Browser MCP daemon is already listening` | port already held                     | use the running daemon, or `--port <free>` for both thin and extension                                         |
| `engine worker did not become ready`              | Node worker failed to start           | check the worker log; run the engine command by hand with `--stderr`; confirm `dist/daemon.js` exists          |
| First call is slow (~90 ms extra)                 | worker cold start                     | expected; keep the worker running (do not use `--no-idle-exit` unless the front door owns it)                  |
| Extension connection rejected                     | Origin not allowlisted                | add the ID to `BROWSER_MCP_EXTENSION_IDS` or `dev-extension-ids`; use the Web Store or self-built ID           |
| Pairing codes differ                              | extension dialed a different port     | make the extension's port match the front door's port; the front door passes `--advertised-port` automatically |
| `No browser is connected`                         | extension not connected or not paired | open the popup, click **Allow**; check the code                                                                |
| Tools work with Node but not Go                   | front door or worker not running      | check both logs; verify `BROWSER_MCP_DAEMON=go` and the binary path                                            |

## Rollback

The Go front door is opt-in and does not migrate data.

1. Remove `BROWSER_MCP_DAEMON` and `BROWSER_MCP_GO_DAEMON` from the MCP config
   (or unset them), then restart the AI app.
2. The thin process starts the Node daemon again. `daemon.json` is rewritten by
   whichever daemon runs.
3. Optionally delete `packages/mcp/dist/browsermcpd` to remove the artifact.
4. Verify: `curl http://127.0.0.1:<port>/browsermcp/status` and a `browser`
   tool call (`help`, then `goto https://example.com`).

No extension changes are needed; the extension protocol is unchanged.

## Known limitations

- **Hybrid memory:** while the Node worker is resident, the process tree costs
  more than the Node daemon alone (~91 MB vs ~71 MB in the benchmark). The Go
  front door wins startup and idle cost, not browser-time memory.
- **First-call latency:** the worker starts on the first client or extension
  connection (~90 ms in the benchmark).
- **Packaging:** the binary is built locally. Shipping signed binaries for
  Linux/macOS/Windows and x64/arm64 through npm is not implemented.
- **Engine language:** the engine remains TypeScript/Playwright. A native Go
  engine is a separate project; it must pass the conformance suite before it
  can replace the worker.
- **Transport:** the extension must use TCP loopback WebSockets; Unix domain
  sockets are not available to browser extensions. The front door keeps TCP for
  client compatibility.
