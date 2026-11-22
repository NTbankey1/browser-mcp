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

**Current recommendation:** the hybrid is functionally complete and tested, but
it costs more memory than the Node daemon while the engine worker is resident.
For daily use, the Node daemon is still the better default. The native Go engine
(replacing Playwright) is the only way to keep the Go memory win during browser
use; it is a separate, larger project and is not implemented.
