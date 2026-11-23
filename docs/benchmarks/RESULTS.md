# Results

Native Go engine (`browsermcpd --native-engine`), measured on the machine and
date recorded in [`BASELINE.md`](BASELINE.md). Medians of five runs unless
stated. Raw JSON: reproduce with the commands below.

```sh
pnpm --filter @browsermcp/mcp go:build
BROWSER_MCP_ENGINE=native pnpm --filter @browsermcp/mcp bench \
  -- --bin packages/mcp/bin/browsermcpd --runs 5 --json native.json
BROWSER_MCP_DAEMON=go BROWSER_MCP_ENGINE=native pnpm bench \
  -- --runs 5 --json native-commands.json
```

## Daemon lifecycle and RPC

| Metric                           | Node daemon | Go + Node worker |       Native Go |      vs Node |
| -------------------------------- | ----------: | ---------------: | --------------: | -----------: |
| Cold start to `/status` (ms)     |       92.44 |            15.14 |       **15.03** |         −84% |
| RSS at ready (MB)                |        69.2 |             10.7 |        **11.2** |         −84% |
| RSS after 10 s idle (MB)         |        69.2 |             10.7 |        **11.2** |         −84% |
| RSS after first client RPC (MB)  |        83.5 |             15.5 |        **17.4** |         −79% |
| Process-tree RSS after RPC (MB)  |        83.5 |             94.6 |        **17.4** |         −79% |
| CPU after 10 s (s)               |        0.10 |             0.00 |        **0.00** |            — |
| HTTP `/status` p50 / p95 (ms)    | 0.16 / 0.37 |      0.13 / 0.28 | **0.13 / 0.29** |        equal |
| WS open (ms)                     |        1.84 |             0.90 |            1.98 |        equal |
| `hello` → `welcome` (ms)         |        1.56 |           115.90 |        **0.51** |    3× faster |
| `tools/list` p50 / p95 (ms)      | 0.08 / 0.16 |      0.13 / 0.29 | **0.08 / 0.18** |        equal |
| `tools/call help` p50 / p95 (ms) | 0.11 / 0.24 |      0.20 / 0.39 | **0.07 / 0.12** | ~1.6× faster |
| 50 concurrent `tools/list` (ms)  |         1.5 |              1.8 |             1.5 |        equal |

The native daemon keeps the idle and startup win **while a client is
connected**: no Playwright import, no worker process. That is the difference
between this and the Go front door, whose process tree is larger than the Node
daemon's once the worker starts.

## Browser commands (real extension, headless Chromium)

| Workload         | Node + Playwright p50 / p95 (ms) | Native p50 / p95 (ms) | Result                           |
| ---------------- | -------------------------------: | --------------------: | -------------------------------- |
| cold first call  |                    504.5 / 504.5 |     **175.1** / 175.1 | 2.9× faster (no Playwright load) |
| click            |                    568.9 / 570.7 |     **217.1 / 221.6** | 2.6× faster                      |
| fill             |                    276.9 / 278.1 |       **10.0 / 13.0** | 27× faster                       |
| gotoSmall        |                    103.1 / 103.4 |       **50.8 / 52.0** | 2.0× faster                      |
| screenshot       |                      81.4 / 84.8 |       **47.2 / 57.4** | 1.7× faster                      |
| batch5 (5 reads) |                      56.1 / 58.3 |       **34.1 / 60.2** | 1.6× faster p50                  |
| tabList          |                      48.2 / 51.0 |         **7.6 / 7.7** | 6× faster                        |
| getTextBig       |                      48.2 / 48.8 |         **8.0 / 8.1** | 6× faster                        |
| evalTitle        |                      48.0 / 49.0 |         **6.9 / 8.1** | 7× faster                        |
| getTitle         |                      44.5 / 45.1 |         **4.4 / 5.8** | 10× faster                       |
| snapshotSmall    |                      43.9 / 46.4 |        **8.0 / 46.5** | 5.5× faster p50                  |
| getUrl           |                        1.4 / 2.9 |             4.3 / 5.6 | 3× slower (still <6 ms)          |
| snapshotBig      |                      50.3 / 57.7 |     **213.2 / 224.7** | **4.2× slower**                  |
| findBig          |                      48.6 / 52.6 |     **181.7 / 242.5** | **3.7× slower**                  |

Interpretation:

- **No Playwright** removes its fixed per-call cost: `tabList`, `getTitle`,
  `eval` and `getText` are dominated by one CDP round trip in the native engine,
  while Playwright adds tens of milliseconds per call.
- **Actionability and input** are cheaper: `click` and `fill` no longer wait on
  Playwright's stability checks.
- **Large-page snapshots are a real regression.** Playwright parses the
  accessibility JSON in V8; Go's `encoding/json` into the engine's node structs
  costs ~160 ms more for the 300-row fixture. `find` pays the same cost because
  it takes a snapshot first. This is the main target for optimisation
  (streaming/filtering decode, or a cached tree), and it is why the native engine
  is not the default yet.
- `getUrl` is slower because it is a CDP `Runtime.evaluate` round trip, while
  Playwright answers from its cached URL. Sub-6 ms is not a practical
  regression.

## Agent efficiency

Measured from a real 10-step form workflow (`goto`, three fills, three checks, a
fill, a click, `get text --max 1500`) in **one** MCP call with the native
engine: **581 characters** of tool output, including the `Page:` trailer.

A like-for-like separate-call run was not executed. From the measured parts, one
call per step would keep the same 10 status lines and add one `Page:` trailer
(52 characters) per acting call: about **1,058 characters**, i.e. **45% less
text** for the batch, before counting per-call MCP JSON framing (arguments and
result envelopes), which the batch avoids nine times. The tool definition is
unchanged in shape, so the batching win is the main lever; adding a second
"workflow" tool would cost tokens per turn rather than save them.

## Correctness and conformance

| Check                                                             | Result                                                      |
| ----------------------------------------------------------------- | ----------------------------------------------------------- |
| Real extension, real Chromium smoke (`pnpm smoke`, native engine) | **20/20**                                                   |
| Go tests including race detector (`go test -race ./...`)          | all packages pass                                           |
| Go vet / gofmt                                                    | clean                                                       |
| TypeScript tests (`pnpm test`)                                    | 196 MCP + 102 extension-core + 1 extension pass (7 skipped) |
| Typecheck, lint, format                                           | pass                                                        |

The smoke covers pairing (including a different daemon key), the form flow,
snapshot/find/get/eval/screenshot/console/requests, tabs, clicking at 125% zoom,
pages that open tabs, extension iframes (including one that comes back after
removal), uploads and screenshot paths, a service-worker restart, PDF and
download, two AI apps with separate groups and badges, a daemon restart
mid-session, Stop/Resume, a second Chrome profile with cross-profile tab
selection, and re-pairing.

## Caveats

- Results are from one machine and one Chromium build; they are not portable
  claims.
- The per-command harness uses Playwright to drive the test browser in both
  cases, so it measures the daemon's behaviour, not the test's.
- The fake-extension integration test proves the wiring and protocol, not
  Chrome semantics; the smoke test is the conformance evidence.
- The native engine is opt-in. The Node engine remains the default and the
  rollback path.
