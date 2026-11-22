# Phase 5 — Benchmark report

**Date:** 2026-11-23
**Machine:** Fedora Linux 7.3.4, x86_64
**Runtime:** Node v26.13.1, Go 1.27.4
**Method:** 5 runs per implementation, medians reported
**Raw data:** [`bench-results/node-daemon-2026-11-23.json`](bench-results/node-daemon-2026-11-23.json),
[`bench-results/go-front-door-2026-11-23.json`](bench-results/go-front-door-2026-11-23.json)

The Go JSON's `path` field records the pre-`bin/` layout; the binary now lives in
`packages/mcp/bin/browsermcpd`. The later fixes do not change the measured
lifecycle numbers.

## What was measured

Both implementations were built from commit `9658da4` and driven by
`packages/mcp/bench/run.mjs` with a temporary `BROWSER_MCP_HOME` and a random
free port. No extension was connected, so there is no browser workload yet.
Each run measures:

1. cold start (spawn → `/browsermcp/status` 200);
2. RSS and CPU at ready and after 10 s idle;
3. 500 sequential + 50 concurrent `/status` requests;
4. client WebSocket open and `hello` → `welcome`;
5. 200 `tools/list` and 100 `tools/call help` calls, plus 50 concurrent
   `tools/list`.

`tools/call help` needs no browser; it measures engine dispatch, session
locking and JSON framing. The Go front door starts its Node engine worker on
the first client connection, so `wsWelcomeMs` includes that startup.

## Results (medians of 5 runs)

| Metric                            |    Node daemon |                     Go front door | Difference          |
| --------------------------------- | -------------: | --------------------------------: | ------------------- |
| Cold start to `/status`           |        91.3 ms |                       **19.4 ms** | 4.7× faster         |
| Idle RSS at ready                 |        70.6 MB |                       **10.0 MB** | 7× lower            |
| Idle RSS after 10 s               |        70.7 MB |                       **10.0 MB** | 7× lower            |
| RSS after first RPC               |        78.9 MB |              14.9 MB (front door) | —                   |
| RSS after first RPC, process tree |        78.9 MB | **91.0 MB** (front door + worker) | 15% higher          |
| CPU after 10 s idle               |         0.09 s |                            0.00 s | negligible both     |
| `hello` → `welcome`               |        1.44 ms |                           90.2 ms | worker cold start   |
| WS open                           |         1.9 ms |                           0.68 ms | —                   |
| `/status` p50 / p95               | 0.14 / 0.71 ms |                    0.12 / 0.28 ms | comparable          |
| `tools/list` p50 / p95            | 0.07 / 0.21 ms |                    0.11 / 0.21 ms | comparable          |
| `tools/call help` p50 / p95       | 0.10 / 0.25 ms |                    0.15 / 0.28 ms | comparable          |
| 50 concurrent `tools/list`        |         1.3 ms |                            2.0 ms | one extra local hop |

## Interpretation

- **The Go front door is much cheaper when idle.** It starts in ~19 ms and uses
  ~10 MB before any client connects. The Node daemon is ~91 ms and ~71 MB even
  with Playwright lazy-loaded (Phase 2).
- **The win disappears while the Node worker is resident.** The first client
  (or the extension) starts the worker; the process tree then costs ~91 MB,
  about 15% more than the Node daemon alone, and the first `hello` waits ~90 ms.
- **RPC latency is already sub-millisecond on both.** The proxy adds no
  meaningful p50 cost; the 50-concurrent case is 0.7 ms higher, consistent with
  one extra local WebSocket hop and run-to-run noise.
- **Startup numbers are cache-sensitive.** An earlier cold-ish Phase 2 run
  measured 207.6 ms for the Node daemon; repeated warm runs measure 91.3 ms. The
  5-run median above is the like-for-like comparison. The RSS numbers are
  stable across runs.
- **A full Go engine is the only way to keep the memory win during browser
  use.** The hybrid keeps the TypeScript engine, so browser-time memory is
  higher, not lower. That is the trade-off the front door makes.

## Not measured yet

The following require a real or fake extension and must be measured before
claiming a native Go engine is faster:

- browser commands: `goto`, `snapshot -i`, `click`, `type`, `eval`,
  `screenshot`, and a large-page snapshot;
- 1, 4 and 8 concurrent sessions on separate tabs;
- multi-browser (Chrome profile) behaviour and tab-ID collisions;
- reconnect time after an extension/service-worker restart or a daemon kill;
- pairing time and extension frame RTT.

In the hybrid these workloads run through the same TypeScript engine, so any
difference should come only from the proxy hop. A native Go engine is a
different implementation and needs its own conformance and performance runs
against the same fixtures.

## Reproduce

```sh
pnpm install --frozen-lockfile
pnpm build
pnpm --filter @browsermcp/mcp go:build

# Node daemon
node packages/mcp/bench/run.mjs --runs 5 --json /tmp/node.json

# Go front door (starts the Node worker on first client)
node packages/mcp/bench/run.mjs \
  --bin packages/mcp/bin/browsermcpd \
  --runs 5 --json /tmp/go.json
```

Always compare runs from the same machine, Node version and Chromium build.
Do not report a single run as a result.

## Raw summaries

| Metric                     |        Node |          Go |
| -------------------------- | ----------: | ----------: |
| startupMs                  |       91.31 |       19.44 |
| rssAtReadyMB               |        70.6 |        10.0 |
| rssAfter10sMB              |        70.7 |        10.0 |
| rssAfterRpcMB              |        78.9 |        14.9 |
| rssAfterRpcTreeMB          |        79.0 |        91.0 |
| cpuAfter10sSec             |        0.09 |        0.00 |
| httpP50Ms / p95Ms          | 0.14 / 0.71 | 0.12 / 0.28 |
| wsWelcomeMs                |        1.44 |       90.16 |
| toolsListP50Ms / p95Ms     | 0.07 / 0.21 | 0.11 / 0.21 |
| toolsCallHelpP50Ms / p95Ms | 0.10 / 0.25 | 0.15 / 0.28 |
| toolsListConcurrent50Ms    |         1.3 |         2.0 |
