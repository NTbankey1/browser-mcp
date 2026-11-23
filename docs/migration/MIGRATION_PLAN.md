# Migration plan

Status as of 2026-11-23. The native engine is implemented and tested but is
**opt-in**; the Node + Playwright daemon remains the default and the rollback
path.

## Phase 0 — Architecture and contract audit

**Status:** done (this branch, plus the existing `docs/go-migration/phase-1-architecture-audit.md`).

Delivered:

- dependency map: thin process, daemon lifecycle, relay/backend, command engine,
  tool definition, protocol, extension core;
- command inventory: 28 commands, their flags, echo, read/act and navigation
  semantics;
- protocol inventory: MCP stdio, client RPC, extension frames and pairing;
- Playwright coupling analysis and the bridge's purpose;
- security control inventory;
- risk analysis and ADRs (see `docs/architecture/ARCHITECTURE.md`).

No production behaviour changed.

## Phase 1 — Stabilise Go infrastructure

**Status:** done.

Reused rather than duplicated: the Go front door's port election, `daemon.json`,
token, Host/Origin checks, payload cap, idle exit and worker lifecycle. The
native engine plugs into the same `relay.Server` through new
`AcceptExtension`/`AcceptClient` hooks; the existing proxy path is untouched.

## Phase 2 — Native CDP infrastructure

**Status:** done.

- `internal/extension`: hello/welcome, signature, nonce reveal, request
  correlation, events, ping, close; byte-compatible with Node (tested against
  Node both ways).
- `internal/cdp`: attach/detach, commands, child-session ids, event dispatch,
  "not attached" recovery.
- `internal/browser`: slots, reconnect identity, public tab IDs, claims,
  readiness, disconnect handling.
- Tested against the real extension by the repository's end-to-end smoke test.

## Phase 3 — Native browser primitives

**Status:** done for the primitives the commands use.

- Navigation: `Page.navigate`, history, reload, load-event wait with
  `readyState` fallback.
- DOM/accessibility: `Accessibility.getFullAXTree`, ref assignment, `-i`
  filtering, `find`, ref resolution and stale-ref detection.
- Locators: ref, CSS, `getByRole`, `getByText`, `getByLabel`,
  `getByPlaceholder`, `getByAltText`, `getByTitle`, `getByTestId`, `locator`,
  `filter`, `first`, `last`, `nth`, `frameLocator`, `contentFrame`.
- Actions: actionability (visible, enabled, uncovered, label retargeting),
  click/double/right/middle with modifiers, hover, drag, scroll, fill, type,
  press, select, check/uncheck, upload.
- Output: screenshot (viewport/element/full, ≤1568 px), PDF, download,
  console, network log, dialogs, `eval`, raw `cdp`.

Conformance is the end-to-end smoke test (20/20) plus the compatibility matrix.

## Phase 4 — Port browser commands

**Status:** done; all 28 commands are implemented natively.

The `browser` tool's schema, commands, quoting rules, status lines, response cap
and error wording follow the TypeScript engine. The TypeScript implementation is
still present and is the default, so nothing was deleted.

## Phase 5 — Agent workflow and output optimisation

**Status:** partly done.

Implemented:

- workflow execution is the existing multi-line `commands` call: parse all lines
  first, run in order, stop at the first error, per-step timeouts, cancellation
  through `context`, one compact status line per action, a `Page:` trailer;
- compact/detailed output via `--max` and the 80,000-character response cap;
- snapshot `-i`, `--depth`, scoped snapshots and `find`;
- stale-ref detection and safe read retry after a re-attach;
- per-session and per-tab isolation.

Not implemented (tracked as follow-up):

- a dedicated `workflow` tool (the existing batched `browser` call already
  provides the behaviour without adding a second tool surface);
- snapshot caching / incremental diffs beyond the per-tab ref table;
- a measured like-for-like token benchmark of one call vs N calls (the
  output-size analysis in `docs/benchmarks/RESULTS.md` is measured from real
  responses, with the separate-call figure calculated from measured parts).

## Phase 6 — Native Go production mode

**Status:** opt-in, not default.

- Selected with `BROWSER_MCP_DAEMON=go` and `BROWSER_MCP_ENGINE=native` (or
  `browsermcpd --native-engine`).
- Functional parity is demonstrated by the 20/20 real-extension smoke test and
  the Go/integration suites.
- Default remains Node until the compatibility matrix's known deviations are
  closed or accepted and the performance review is signed off.

## Phase 7 — Remove the Node production dependency

**Status:** not started, by design.

The Node engine must remain as the rollback path until the native engine has
shipped for a release. Removing it would also require:

- packaging signed binaries for every platform and architecture;
- rewiring the thin process to start only the Go daemon;
- retiring the Playwright-based tests or keeping them for conformance;
- updating installation docs and the npm package contents.

## Remaining work (ordered)

1. Close or accept the compatibility deviations in
   `docs/migration/COMPATIBILITY_MATRIX.md` (cross-origin `frameLocator`, exact
   `getByRole` implicit roles, raw-CDP session isolation).
2. Optimise large-page snapshot decoding (see `docs/benchmarks/RESULTS.md`).
3. Add snapshot caching/diffs if measurements show it pays.
4. Cross-platform packaging and a release gate.
5. Only then consider making native the default and removing the worker.
