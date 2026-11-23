# Baseline

Reference measurements for the Node + Playwright daemon and the Go front door
with a Node worker, taken on the same machine as the native results.

## Environment

| Item    | Value                                                 |
| ------- | ----------------------------------------------------- |
| Machine | Fedora Linux 7.3.4-300.fc45.x86_64, 8 CPUs, 11 GB RAM |
| Node    | v26.13.1                                              |
| pnpm    | 12.9.1                                                |
| Go      | go1.27.4 linux/amd64                                  |
| Date    | 2026-11-23                                            |

Every run used a temporary `BROWSER_MCP_HOME` and a random free port, never
`~/.browsermcp` or ports 9009–9012. Values are medians of five runs unless
stated.

## Daemon lifecycle and RPC

Reproduce:

```sh
pnpm --filter @browsermcp/mcp build
pnpm --filter @browsermcp/mcp bench -- --runs 5 --json node.json
pnpm --filter @browsermcp/mcp go:build
pnpm --filter @browsermcp/mcp bench -- --bin packages/mcp/bin/browsermcpd \
  --runs 5 --json hybrid.json
```

| Metric                           | Node daemon | Go front door + Node worker |
| -------------------------------- | ----------: | --------------------------: |
| Cold start to `/status` (ms)     |       92.44 |                       15.14 |
| RSS at ready (MB)                |        69.2 |                        10.7 |
| RSS after 10 s idle (MB)         |        69.2 |                        10.7 |
| RSS after first client RPC (MB)  |        83.5 |                        15.5 |
| Process-tree RSS after RPC (MB)  |        83.5 |                        94.6 |
| CPU after 10 s (s)               |        0.10 |                        0.00 |
| HTTP `/status` p50 / p95 (ms)    | 0.16 / 0.37 |                 0.13 / 0.28 |
| WS open (ms)                     |        1.84 |                        0.90 |
| `hello` → `welcome` (ms)         |        1.56 |                      115.90 |
| `tools/list` p50 / p95 (ms)      | 0.08 / 0.16 |                 0.13 / 0.29 |
| `tools/call help` p50 / p95 (ms) | 0.11 / 0.24 |                 0.20 / 0.39 |
| 50 concurrent `tools/list` (ms)  |         1.5 |                         1.8 |

The hybrid pays for two processes the moment a client connects: the front door's
idle win disappears once the Node worker starts.

## Browser commands (Node + Playwright)

Reproduce (builds the extension in development mode, launches headless Chromium,
pairs through the real popup, then runs each workload):

```sh
pnpm bench -- --runs 5 --json node-commands.json
```

| Workload        |   n | p50 ms | p95 ms |
| --------------- | --: | -----: | -----: |
| click           |   5 |  568.9 |  570.7 |
| cold first call |   1 |  504.5 |  504.5 |
| fill            |   5 |  276.9 |  278.1 |
| gotoSmall       |   3 |  103.1 |  103.4 |
| screenshot      |   3 |   81.4 |   84.8 |
| batch5          |   5 |   56.1 |   58.3 |
| snapshotBig     |   3 |   50.3 |   57.7 |
| findBig         |   5 |   48.6 |   52.6 |
| tabList         |   5 |   48.2 |   51.0 |
| getTextBig      |   3 |   48.2 |   48.8 |
| evalTitle       |   5 |   48.0 |   49.0 |
| getTextSmall    |   5 |   47.8 |   88.7 |
| getTitle        |   5 |   44.5 |   45.1 |
| snapshotSmall   |   5 |   43.9 |   46.4 |
| help            |   5 |    6.2 |    6.8 |
| getUrl          |   5 |    1.4 |    2.9 |

The Node table is the comparison baseline; it is not a claim about other
machines.

## Caveats

- One machine, one Chromium build. Do not compare these numbers with runs from
  another host.
- The per-command harness reuses Playwright to drive Chromium from the test, so
  the test-side connection is not what the daemon pays for.
- `cold first call` is one sample per run by design: it includes the first
  Playwright connection in the Node daemon.
