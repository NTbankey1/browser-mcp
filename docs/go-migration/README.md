# Go migration

The migration from the TypeScript daemon to an opt-in Go front door is
documented phase by phase:

| Document                                                         | Contents                                                                                             |
| ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| [`phase-1-architecture-audit.md`](phase-1-architecture-audit.md) | Current architecture, measured baseline, Playwright coupling, Go/no-Go assessment and the gated plan |
| [`phase-3-go-front-door.md`](phase-3-go-front-door.md)           | The Go front door, the Node engine worker, security controls, usage and rollback                     |
| [`phase-5-benchmarks.md`](phase-5-benchmarks.md)                 | Reproducible benchmark report (5-run medians) and raw JSON                                           |
| [`phase-6-operations.md`](phase-6-operations.md)                 | Installation, configuration, logs, debugging, rollback and limitations                               |
| [`e2e-smoke-report.md`](e2e-smoke-report.md)                     | Real extension/Chromium smoke test (20/20), real Brave demo, post-report fixes, uncovered cases      |

Benchmarks live in [`bench-results/`](bench-results/); smoke logs in
[`e2e-logs/`](e2e-logs/).

The native Go engine (no Node worker, no Playwright) is documented separately:
[`docs/architecture/`](../architecture/), [`docs/migration/`](../migration/) and
[`docs/benchmarks/`](../benchmarks/). It is opt-in with
`BROWSER_MCP_DAEMON=go BROWSER_MCP_ENGINE=native`; the Node daemon remains the
default and the rollback path.

**Current recommendation:** the native Go engine is implemented and passes the
real-extension smoke test 20/20, with ~17 MB vs ~84 MB RSS during use and equal
or better latency for most commands. It remains opt-in while the compatibility
matrix's deviations (raw `cdp` session isolation, cross-origin `frameLocator`,
large-page snapshot decoding) are open; the Node daemon is still the default.
The hybrid front door is no longer the recommended way to get Go's idle win:
the native engine keeps that win during browser use as well.
