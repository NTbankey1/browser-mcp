# Daemon benchmarks

`run.mjs` measures the built daemon (`dist/daemon.js`) reproducibly. It uses a
temporary `BROWSER_MCP_HOME` and a random free port, so it never touches
`~/.browsermcp` or ports 9009–9012.

## Usage

```sh
pnpm --filter @browsermcp/mcp build
pnpm --filter @browsermcp/mcp bench                 # 3 runs
pnpm --filter @browsermcp/mcp bench -- --runs 1     # quick sample
pnpm --filter @browsermcp/mcp bench -- --json bench-results.json
```

The last line of stdout is a JSON document with per-run results and medians.

## What it measures

| Workload         | Metric                                                                        |
| ---------------- | ----------------------------------------------------------------------------- |
| Cold start       | spawn → `/browsermcp/status` 200                                              |
| Idle             | RSS at ready and after 10 s, CPU at ready and after 10 s                      |
| HTTP             | 500 sequential `/status` requests (p50/p95/max), 50 concurrent                |
| Client WebSocket | open time, `hello` → `welcome` time                                           |
| Client RPC       | 200 `tools/list`, 100 `tools/call help` (p50/p95), 50 concurrent `tools/list` |

`tools/call help` needs no browser, so it measures engine dispatch, session
locking and JSON framing without Chromium.

## What it does not measure

- Browser commands (`goto`, `snapshot`, `click`, …), which require a real
  extension or the fake-extension harness.
- Multi-session or multi-browser throughput.
- Reconnect time after a killed extension or daemon.

Add those in `packages/mcp/src/testing/` driven workloads before claiming a Go
engine is faster. Results depend on the machine and Node version; compare runs
from the same environment and report the median of several runs, as this script
does.

## Measuring the Go front door

```sh
pnpm --filter @browsermcp/mcp go:build
pnpm --filter @browsermcp/mcp bench -- --bin packages/mcp/dist/browsermcpd --runs 3
```

`--bin` accepts the built Go front door as well as the Node daemon. With the Go
front door, `rssAtReadyMB` and `rssAfter10sMB` measure the Go process only;
`rssAfterRpcMB` is that process, and `rssAfterRpcTreeMB` adds its Node engine
worker, which starts on the first client connection.
