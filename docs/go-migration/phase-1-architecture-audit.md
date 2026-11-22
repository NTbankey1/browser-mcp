# Phase 1 — Architecture Audit and Go Migration Assessment

**Repository:** local working copy of Browser MCP (`BrowserMCP/mcp`, Apache-2.0), now at `NTbankey1/browser-mcp`
**Date:** 2026-11-23
**Status:** Phase 1 deliverable — no production code changed
**Scope:** audit of the current TypeScript daemon, measured baseline, Go feasibility, proposed architecture and a gated migration plan

---

## 1. Executive summary

The daemon is **not** a plain WebSocket relay. It is a browser-automation process whose command engine is built on **Playwright Core**: 28 commands, about **5.2k lines** of source that import `playwright-core`, and about **3.5k lines** of tests covering browser behaviour. The relay, lifecycle and protocol layers around it are comparatively small (about **3.7k lines**).

Measured on this machine (no extension connected, one idle daemon):

| Metric                           | Node daemon (today) | Minimal Go relay prototype | Verdict            |
| -------------------------------- | ------------------: | -------------------------: | ------------------ |
| Cold start to `/status`          |          **352 ms** |                   **4 ms** | Go wins decisively |
| Idle RSS                         |        **118.3 MB** |                 **7.9 MB** | Go wins decisively |
| Idle CPU over 10 s               |              0.01 s |                     0.00 s | Both negligible    |
| HTTP `/status` p50 / p95         |      0.27 / 0.70 ms |             0.18 / 0.45 ms | Already sub-ms     |
| Client WS `tools/list` p50 / p95 |      0.12 / 0.27 ms |      0.04 / 0.07 ms (echo) | Already sub-ms     |
| 50 concurrent RPCs               |        1.4 ms total |        0.9 ms total (echo) | Both fine          |

The Node daemon's footprint is dominated by the runtime and Playwright: bare Node is ~46 MB RSS, and importing `playwright-core` raises it to ~126 MB in ~0.26 s. The Go prototype has no engine, so its numbers are the **relay floor**, not a working Browser MCP daemon.

**Finding:** A Go rewrite of the relay/lifecycle layers would improve startup and idle memory, but those gains disappear if the Go daemon still delegates browser commands to a Node/Playwright engine. A _full_ Go daemon means reimplementing Playwright's automation semantics over raw CDP — a much larger, higher-risk project than moving a message loop.

**Recommendation:** do **not** start a full rewrite. Run a gated two-step programme:

1. Measure and optimise the current TypeScript daemon first (lazy engine import, file-transfer and serialization costs, logging, snapshot CPU). This establishes the real ceiling.
2. Build a Go **relay/lifecycle spike** behind a flag, keeping the Node engine as an internal worker, and measure it. Adopt Go only if it meets explicit targets. A full Go engine is justified only if the product accepts a multi-month port and the conformance suite proves parity.

This follows the task's own rule: report when migration costs more than it returns, rather than forcing it.

---

## 2. Method and evidence

- Read the full source map and the architecture docs: `README.md`, `packages/mcp/README.md`, `packages/mcp/AGENTS.md`, `packages/protocol/src/index.ts`, and the daemon, thin, relay and command layers.
- Counted source and test lines, command definitions and `playwright-core` imports.
- Built the repository (`pnpm install --frozen-lockfile && pnpm build`) without changing source.
- Measured the built daemon with a temporary `BROWSER_MCP_HOME`, port 9411, no extension and no session.
- Built a throwaway Go 1.27 HTTP + WebSocket echo server (stdlib + `github.com/coder/websocket`) at port 9412 as the relay floor.
- All measurements: Fedora Linux 7.3.4, Node v26.13.1, pnpm 12.9.1, Go 1.27.4, 2026-11-23.

**Caveats.** The daemon run was a single sample per metric (import cost was sampled three times: 0.25–0.27 s). The Go prototype implements none of Browser MCP's protocol, pairing, extension handling or engine; it only bounds relay overhead. Browser-command latency, multi-session throughput and reconnect time were **not** measured and must not be claimed. Phase 5 defines the harness that will measure them.

---

## 3. Current architecture

### 3.1 Process and transport map

```
AI app ──stdio (MCP)──▶ thin process (dist/index.js, one per AI app)        src/thin/
                          │ starts/replaces daemon; forwards tools/list, tools/call
                          │ answers daemon file requests (readFile, writeFile)
                          ▼ ws://127.0.0.1:<port>/client?token=…             (JSON-RPC style)
                       daemon (dist/daemon.js, one per user)                  src/daemon/
                          ├─ command engine (28 commands)                     src/commands/, src/tools/
                          │    └─ BrowserBackend interface                    src/contracts.ts
                          └─ RelayBackend: up to 8 browser slots              src/relay/backend.ts
                               ├─ Playwright chromium.connectOverCDP(noDefaults)  src/relay/browser-slot.ts
                               │      ▼ ws://127.0.0.1:<port>/cdp?token=…&browser=<slot>
                               ├─ CdpBridge: emulates Browser.* / Target.*    src/relay/bridge.ts
                               └─ ExtensionConnection                          src/relay/extension-connection.ts
                                      ▲ ws://127.0.0.1:<port>/extension
                               MV3 service worker ── chrome.debugger ──▶ tab
```

There are **three protocol boundaries**:

1. **MCP over stdio** — AI app ↔ thin process, implemented by `@modelcontextprotocol/sdk` (`src/thin/server.ts:29-31`).
2. **Client RPC over WebSocket** — thin process ↔ daemon at `/client`, JSON-RPC-style frames after `hello`/`welcome`; version `CLIENT_PROTOCOL_VERSION = 1` (`src/contracts.ts:19`). Requests in both directions: the daemon calls `readFile`/`writeFile` on the thin process.
3. **Relay protocol over WebSocket** — daemon ↔ extension at `/extension`, protocol version 1, documented in `packages/protocol/src/index.ts:1-16`. Every frame is one JSON object (`ExtensionMessage` / `DaemonMessage`, lines 324-371).

In addition, the daemon opens a **private CDP WebSocket to itself** at `/cdp`; Playwright is the client. The bridge fakes the parts of Chrome that `chrome.debugger` does not expose.

### 3.2 Layer inventory

| Layer            | Location                       | Source LOC | Responsibility                                                                  |
| ---------------- | ------------------------------ | ---------: | ------------------------------------------------------------------------------- |
| Thin process     | `packages/mcp/src/thin/`       |        489 | MCP stdio server, daemon discovery/start/replace, file brokering                |
| Daemon lifecycle | `packages/mcp/src/daemon/`     |        675 | Port election, `daemon.json`, identity, sessions, idle exit, shutdown           |
| Relay / backend  | `packages/mcp/src/relay/`      |      2,526 | HTTP+WS server, extension connection, CDP bridge, browser slots, tab-ID mapping |
| Command engine   | `packages/mcp/src/commands/`   |      5,463 | 28 browser commands, parsing, ARIA snapshots, actions, CDP, files               |
| Tool definition  | `packages/mcp/src/tools/`      |         80 | The `browser` MCP tool                                                          |
| Utilities        | `packages/mcp/src/utils/`      |        168 | RPC peer, logging                                                               |
| Test helpers     | `packages/mcp/src/testing/`    |      1,028 | Fake extension/Chromium, fixture server, stub daemon                            |
| Protocol         | `packages/protocol/src/`       |        374 | Extension↔daemon frames, pairing crypto, ports, close codes                     |
| Extension core   | `packages/extension-core/src/` |      3,445 | Browser control, pairing verification, debugger relay, tab rules                |
| Extension app    | `apps/extension/src/`          |        680 | Popup and WXT configuration                                                     |

Tests: **6,719 lines** of Vitest across 22 files, plus a **1,473-line** end-to-end smoke test and a **722-line** eval harness. This is a strong safety net for a TypeScript refactor and the main asset a Go port must reproduce.

### 3.3 Playwright coupling

`playwright-core` is imported by 14 files, including every major engine and relay file:

| File                      | Lines |
| ------------------------- | ----: |
| `commands/commands.ts`    | 1,329 |
| `commands/engine.ts`      |   994 |
| `relay/backend.ts`        |   883 |
| `commands/actions.ts`     |   365 |
| `commands/save.ts`        |   310 |
| `commands/snapshot.ts`    |   242 |
| `contracts.ts`            |   229 |
| `commands/targets.ts`     |   223 |
| `relay/browser-slot.ts`   |   222 |
| `commands/cdp.ts`         |   170 |
| `commands/tab-state.ts`   |   169 |
| `commands/hidden-tabs.ts` |    61 |

The engine uses high-level Playwright features, not just raw CDP: locators (`getByRole` 73 uses, `getByText` 18), actionability and auto-waiting, `ariaSnapshotJSON`, `keyboard`/`mouse`, `setInputFiles`, downloads/PDF/screenshots, console/network/dialog event streams, child frames and OOPIF sessions.

The bridge exists **only** to make Playwright believe it is connected to Chrome. It emulates `Browser.getVersion`, `Target.*`, target announcement, session attach/detach, child sessions, `Runtime.enable` ordering and input framing (`relay/bridge.ts:75-77`, `:434`, `:572`, `:612`). A Go engine speaking the extension protocol directly could delete the bridge, but would have to absorb its behaviour and Playwright's higher-level semantics.

### 3.4 Lifecycle and concurrency

- The thin process finds a daemon via `daemon.json`, validates it through `/browsermcp/status` and a token check, starts one detached if absent, and replaces an older version through `POST /shutdown` (`thin/daemon-client.ts:88-205`).
- The daemon binds the first free port of 9010–9012 unless `--port` is given, writes `daemon.json` atomically with mode 0600, and exits 10 minutes after the last session (`daemon/daemon.ts:81-272`).
- Each session is one WebSocket. `tools/call` is serialized per session, and per tab through a lock map (`commands/engine.ts:229-255`). The daemon allows up to 8 browsers (`MAX_BROWSERS`), each with its own extension connection, bridge and Playwright connection (`relay/browser-slot.ts`).
- Requests to the extension have 30 s timeouts (5 min for `cdp`), RPC 30 s, `tools/call` 5 min (`relay/extension-connection.ts:59-61`, `utils/rpc.ts:13-16`).
- Multi-browser identity, tab-ID remapping (public IDs above 100000 on collisions), reconnection and slot confirmation are substantial business logic in `RelayBackend` (~880 lines).

### 3.5 Security controls (must be preserved)

| Control                                                                                                                      | Where                                                                 |
| ---------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| Loopback-only bind (`127.0.0.1`)                                                                                             | `relay/constants.ts:2`                                                |
| Host-header check against DNS rebinding                                                                                      | `relay/server.ts:181-187`                                             |
| Token on `/client`, `/cdp`, `/shutdown`, constant-time compare                                                               | `relay/server.ts:83-84, 154-171, 202-206`                             |
| Extension Origin allowlist (Web Store ID, self-built ID, dev IDs)                                                            | `relay/server.ts:189-200`, `protocol/src/index.ts:144-148`            |
| ECDSA P-256 pairing with challenge, commitment, reveal, six-digit code                                                       | `protocol/src/index.ts:44-96`, `daemon/identity.ts`                   |
| Pairing required before tabs/state are exposed                                                                               | `extension-core/background/browser-control.ts:288-302`                |
| Incognito only with user permission; Stop/pause; browser pages, stores, discarded tabs hidden; `file:` only with file access | `extension-core/background/exposure.ts`, `browser-control.ts:400,503` |
| Identity and `daemon.json` modes 0600, home 0700, atomic writes                                                              | `daemon/identity.ts`, `daemon/info.ts`, `daemon/paths.ts`             |
| Max WebSocket payload 256 MB                                                                                                 | `relay/server.ts:40-43`                                               |
| File access is intentionally full user permissions                                                                           | Documented in `packages/mcp/README.md`                                |

**Security requirement for any port:** no weakening of these controls for performance; pairing order and signature byte formats must be identical; the extension must not be modified unnecessarily.

---

## 4. Measured baseline and bottleneck analysis

### 4.1 Daemon baseline (no extension, no session)

| Metric                                       |                 Value |
| -------------------------------------------- | --------------------: |
| Cold start (spawn → HTTP 200)                |                352 ms |
| RSS at ready / after 10 s                    |   118.3 MB / 118.3 MB |
| CPU at ready / after 10 s                    |       0.41 s / 0.42 s |
| `/status` (500 sequential) p50 / p95 / max   | 0.27 / 0.70 / 4.17 ms |
| 50 concurrent `/status`                      |          27.5 ms wall |
| WS connect (`/client`)                       |                5.1 ms |
| `hello` → `welcome`                          |               2.87 ms |
| `tools/list` (200 sequential) p50 / p95      |        0.12 / 0.27 ms |
| `tools/call help` (100 sequential) p50 / p95 |        0.17 / 0.36 ms |
| 50 concurrent `tools/list`                   |           1.4 ms wall |

Interpretation:

- Startup and memory are dominated by loading Node and `playwright-core` (46 MB bare Node; ~80 MB added by Playwright; ~0.26 s import).
- The message-routing path is already sub-millisecond. The daemon is not a throughput bottleneck at realistic agent rates.
- The idle CPU cost is negligible.

### 4.2 Go relay floor (prototype, no protocol or engine)

| Metric                     |           Value |
| -------------------------- | --------------: |
| Cold start (spawn → READY) |            4 ms |
| RSS at ready / after 10 s  | 7.9 MB / 7.9 MB |
| CPU after 10 s             |          0.00 s |
| `/status` p50 / p95        |  0.18 / 0.45 ms |
| 50 concurrent `/status`    |    19.8 ms wall |
| WS echo p50 / p95          |  0.04 / 0.07 ms |
| 50 concurrent echoes       |     0.9 ms wall |

The Go prototype proves the **relay ceiling**: a Go daemon without an engine can be ~4 ms/8 MB. It does not prove that a Go Browser MCP daemon will be that small, because the engine is the dominant cost.

### 4.3 Where time and memory actually go

1. **Playwright import and process baseline** — about 126 MB of the 118 MB daemon RSS is Node + Playwright (the remainder is the built daemon's own modules and buffers). This is the single largest and easiest-to-measure cost.
2. **Browser work** — page loads, rendering, layout, network. Not measurable from the daemon alone; likely dominant for real tool calls, and unaffected by the daemon's language.
3. **Serialization hops for a CDP command** — engine → Playwright CDP WS (`/cdp`) → bridge JSON re-encode → extension WS (`/extension`) → `chrome.debugger`. Two local WebSocket hops per CDP command. A direct Go engine would remove one hop, but each hop is measured in tens of microseconds; the number of CDP commands per page action is the real multiplier and must be measured.
4. **File transfer** — upload/download paths base64-encode data between the thin process, daemon and Playwright buffers, adding ~33% size and copies. A Go daemon could stream bytes, but the thin↔daemon RPC contract would need a binary frame or chunked encoding.
5. **Snapshot / accessibility rendering** — `ariaSnapshotJSON` and tree rendering are CPU work on the Node event loop. Large pages could cause event-loop lag; not yet measured.
6. **Duplicate state** — tabs/state are tracked in `RelayBackend`, `BrowserSlot`, and the engine's `TabState`; a Go rewrite would not automatically reduce this unless the design is simplified.

---

## 5. Go migration options

### Option A — Keep TypeScript, optimise (no Go)

| Aspect           | Assessment                                                                                                                           |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| Effort           | Low–medium (weeks)                                                                                                                   |
| Risk             | Low                                                                                                                                  |
| Startup / memory | Improves only by lazy-loading the engine; idle daemon can approach bare Node (~46 MB) if Playwright is imported on first browser use |
| Latency          | Small gains from removing redundant serialization and logging                                                                        |
| Compatibility    | None affected                                                                                                                        |
| Verdict          | **Do first**; establishes the ceiling and may be sufficient                                                                          |

### Option B — Go relay/lifecycle + Node Playwright engine worker (hybrid)

| Aspect           | Assessment                                                                                                                                                        |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Effort           | Medium–high (2–4 months including packaging)                                                                                                                      |
| Risk             | Medium–high (second process, second IPC hop, engine worker lifecycle)                                                                                             |
| Startup / memory | Relay starts in ~5 ms, but the Node worker returns ~120 MB when browser work starts. Only wins if the worker is lazy and the product tolerates first-call latency |
| Latency          | Adds one IPC hop; must not regress                                                                                                                                |
| Compatibility    | Thin and extension unchanged; daemon-internal split only                                                                                                          |
| Verdict          | **Spike only**, behind a flag, with explicit targets; stop if it cannot beat Option A                                                                             |

### Option C — Full Go daemon + native CDP engine (no Node/Playwright in the daemon)

| Aspect           | Assessment                                                                                                       |
| ---------------- | ---------------------------------------------------------------------------------------------------------------- |
| Effort           | High (several months); replaces ~5.2k lines of Playwright-shaped engine code and ~3.5k lines of tests            |
| Risk             | High (locator/actionability/ARIA parity, multi-browser IDs, pairing, protocol compatibility)                     |
| Startup / memory | Best case ~20–40 MB and <100 ms; no Playwright, no `/cdp` bridge, one fewer WS hop                               |
| Latency          | Potentially better for CDP-heavy operations, but browser work still dominates                                    |
| Compatibility    | Thin and extension unchanged if the extension protocol is preserved; engine semantics must be reproduced exactly |
| Distribution     | New problem: shipping signed binaries for linux/darwin/windows × x64/arm64 through npm                           |
| Verdict          | **Only after B proves relay gains and the product funds an engine port**                                         |

### 5.1 ADR: daemon migration

**Context.** The daemon's relay is fast and small in code; its memory and startup are dominated by Node + Playwright; its value is the Playwright-based engine, not the message loop. The task asks for Go where it provides meaningful advantage, and forbids forcing a migration with no measurable benefit.

**Decision.** Do not rewrite the engine now. First optimise the TypeScript daemon and measure the ceiling (Option A). In parallel, prototype Option B behind `BROWSER_MCP_DAEMON=go` and gate adoption on explicit targets. Keep Option C as a funded follow-on only if the relay spike shows the memory/startup win survives an engine worker and the product accepts the conformance burden.

**Reasoning.** Go's measured advantage is startup and idle memory, both of which are erased by keeping a Node engine. The only way to keep them is an engine port, which is the riskiest and largest part of the system. Optimising first avoids spending months to discover the current daemon was not the bottleneck.

**Trade-offs.** Option A does not deliver a Go daemon; it delivers measurable improvements sooner. Option B adds process complexity and may not beat A. Option C delivers the architectural win but costs months and risks behavioural regressions.

**Revisit when.** (a) Option A's measurements show the daemon contributes >20% of p95 tool-call latency or >5% CPU under concurrency; (b) the product requires a single static binary or a memory budget below ~50 MB; (c) an engine-worker spike demonstrates the idle win is real; (d) a native CDP engine prototype passes the conformance subset with no p95 regression.

---

## 6. Proposed Go + TypeScript architecture (if the gate passes)

Keep the thin process and the extension in TypeScript. Put Go where the measured win is: lifecycle, relay, protocol, pairing, state and (only if funded) the engine.

```
AI app ──stdio──▶ thin process (TypeScript, unchanged) ──ws /client──▶ Go daemon
                                                                        ├─ relay: HTTP + WS, Host/token/Origin checks
                                                                        ├─ identity: ECDSA P-256, SPKI/PKCS#8, IEEE P1363
                                                                        ├─ extension: frames, pairing, ping, request IDs
                                                                        ├─ browsers: slots, identity, public tab IDs
                                                                        ├─ engine: raw CDP automation (Option C only)
                                                                        └─ files: brokered read/write, streaming
                                                                              ▲ ws /extension
                                                                        MV3 extension (unchanged) ── chrome.debugger ──▶ tab
```

Proposed Go layout:

```
cmd/browsermcpd/main.go
internal/config      home, ports, version, flags
internal/relay       net/http + WS server, path router, security checks
internal/identity    key load/create, signing
internal/extension   frame codec, pairing state machine, request correlation, ping
internal/browsers    slot registry, reconnection, public tab-ID mapping
internal/engine      CDP sessions, DOM/input/snapshot/events/files (Option C)
internal/session     client RPC, per-session/tab serialization
internal/files       brokered file access
internal/log         structured logging, rotation
pkg/protocol         generated or hand-maintained mirror of @browsermcp/protocol
```

Technology choices:

- `net/http`, `context`, `slog`, `errgroup`; no web framework.
- WebSocket: `github.com/coder/websocket` (context-aware) or `gorilla/websocket`. Measure both; neither changes the architecture.
- Crypto: standard `crypto/ecdsa`, `crypto/sha256`, `encoding/base64`; verify byte-for-byte compatibility with the existing Node formats.
- Transport: keep TCP loopback for `/extension` (browser requirement) and `/client` (backward compatibility with existing thin processes). Evaluate Unix Domain Sockets only for a future client protocol, with a TCP fallback; UDS cannot serve the extension and shows no measurable benefit for small JSON frames today.
- Concurrency: goroutine per connection; a mutex or actor per tab; `context` cancellation for every extension request; bounded queues; `errgroup` and explicit close paths to avoid leaks.
- Shutdown: cancel contexts, drain in-flight calls up to the existing 60 s budget, close sockets with code 4003, remove `daemon.json` only if it still names this PID.

Protocol ownership: `@browsermcp/protocol` stays the source of truth for the extension contract. Generate Go types from a JSON/schema artifact or maintain a checked mirror with a conformance test that decodes/encodes the same fixtures on both sides.

---

## 7. Migration plan (gated)

### Phase 1 — Audit (this document)

Deliverable: this report. Exit: decision on whether to fund Phase 2.

### Phase 2 — Baseline harness and TypeScript ceiling

- Build `bench/` with reproducible workloads (below).
- Measure the current daemon: lifecycle, RPC, browser commands, concurrency, recovery, resources.
- Apply low-risk TS optimisations and re-measure:
  - lazy-import `playwright-core` so an idle daemon does not pay 80 MB / 260 ms;
  - remove redundant JSON/base64 copies in file transfer;
  - reduce logging allocations on hot paths;
  - measure event-loop lag during snapshots and large evaluations.
- Exit: quantified daemon contribution to p95 latency/CPU, and the best achievable TS footprint.

### Phase 3 — Go relay/lifecycle spike (Option B)

- Implement `cmd/browsermcpd` for ports, `daemon.json`, identity, `/status`, `/client`, `/extension`, pairing, session RPC and shutdown.
- Keep the Node engine as a worker process behind an internal interface.
- Gate targets: startup < 100 ms, idle RSS < 50 MB with no browser, no p95 regression > 5%, 8 concurrent sessions stable, reconnect ≤ baseline, security checks byte-identical.
- Exit: adopt B only if targets are met; otherwise stop and keep A.

### Phase 4 — Native CDP engine MVP (Option C, only if funded)

- Implement attach/detach, target/session routing, tabs, `goto`, `click`, `type`, `eval`, `snapshot` (subset), `screenshot`.
- Run the existing Vitest scenarios as black-box conformance tests against both daemons.
- Exit: no behavioural differences on the conformance subset; measured resource and latency targets.

### Phase 5 — Full parity, benchmarks, packaging

- Remaining commands, multi-browser identity, reconnection, downloads/PDF, dialogs, hidden tabs, `--caps cdp`.
- Race detector, fuzzing of protocol frames, leak tests, 8-session soak.
- Cross-platform packaging and rollback.

### Phase 6 — Documentation and rollback

- Installation, development, architecture, configuration, debugging, rollback.
- Keep the TypeScript daemon behind an environment flag for at least one release.

---

## 8. Reproducible benchmark harness (required before Phase 3)

Create `bench/` with scripts that emit JSON and a `BENCHMARKS.md` summary. Use the same fixture pages, the same Chromium build and a temporary `BROWSER_MCP_HOME` on a random port.

| Workload         | Measure                                                                                    |
| ---------------- | ------------------------------------------------------------------------------------------ |
| Cold start       | spawn → `/status` 200, 5 runs, median/p95                                                  |
| Idle             | RSS and CPU at 1, 10, 60 s, no extension                                                   |
| RPC              | `tools/list`, `tools/call help`, 500 sequential and 50 concurrent, p50/p95                 |
| Browser commands | `goto`, `snapshot -i`, `click`, `type`, `eval`, `screenshot`, 100 iterations each, p50/p95 |
| Large snapshot   | a 5k-node fixture page, p50/p95 and event-loop lag                                         |
| Concurrency      | 1, 4, 8 sessions on separate tabs; throughput and p95                                      |
| Multi-browser    | 2 fake browsers, tab-ID collisions and reconnection                                        |
| Protocol         | extension frame RTT, raw `cdp` command RTT, pairing time                                   |
| Recovery         | kill extension/service worker, time to usable; kill daemon, time to reconnect              |
| Resources        | RSS, CPU, file descriptors, goroutines (Go) / event-loop lag (Node)                        |

Do not compare numbers from different machines or Chromium builds. Do not report a single run as a result. Do not claim Go is faster for tool calls until the browser workloads above are measured against the same fixtures.

---

## 9. Risks and mitigations

| Risk                                                            | Impact                           | Mitigation                                                                          |
| --------------------------------------------------------------- | -------------------------------- | ----------------------------------------------------------------------------------- |
| Playwright semantic parity (locators, actionability, ARIA refs) | High — agents break subtly       | Conformance suite from existing Vitest scenarios; feature-by-feature gate           |
| Multi-browser tab-ID and slot identity                          | High — wrong tabs                | Port the existing state machine and tests; add collision/reconnect tests            |
| Pairing byte-format mismatch                                    | High — extensions cannot connect | Cross-language fixtures for keys, signatures, commitments, codes                    |
| Protocol drift with deployed extensions                         | High                             | Keep `@browsermcp/protocol` as source of truth; version negotiation tests           |
| Security regression                                             | Critical                         | Port checks first; constant-time token compare; Host/Origin tests; no port exposure |
| Packaging and signing                                           | Medium                           | Decide distribution before Phase 4; npm optional binaries or download-on-install    |
| Two-language maintenance                                        | Medium                           | Keep the TS daemon as rollback; shared conformance suite                            |
| Test rewrite cost                                               | Medium                           | Black-box tests against both implementations; do not port tests line by line        |
| Scope creep                                                     | High                             | Gate each phase on numeric targets; stop when not met                               |
| License/attribution                                             | Medium                           | Preserve `LICENSE` and `NOTICE`; changes are Apache-2.0-compatible                  |

---

## 10. Definition-of-done mapping

| Requirement                                | Status                                                 |
| ------------------------------------------ | ------------------------------------------------------ |
| Go daemon works independently              | Not started — requires Phase 3/4                       |
| TypeScript MCP server communicates with it | Not started                                            |
| Chrome extension continues to work         | Constraint defined; unchanged                          |
| Existing browser tools remain functional   | Conformance suite required                             |
| MCP client compatibility verified          | Requires black-box tests                               |
| Tests actually executed                    | Only build and baseline measured so far                |
| Concurrency and race conditions tested     | Planned (race detector, soak)                          |
| Security controls intact                   | Inventory complete; tests required                     |
| Performance comparisons documented         | Baseline partially measured; browser workloads pending |
| Installable from documented instructions   | Existing instructions verified by build                |

---

## 11. Decision requested

Choose one:

1. **Proceed with Phase 2** — baseline harness + TypeScript optimisations, then re-evaluate Go. (Recommended: lowest risk, measures the real ceiling first.)
2. **Proceed directly with Phase 3** — Go relay/lifecycle spike behind a flag, with the Node engine as a worker; accept the extra process hop and packaging work.
3. **Commit to Phase 4 now** — full Go engine port; requires accepting a multi-month effort and a high parity risk.

No production code should change until this decision is made.

---

## Appendix A — Key code anchors

- Thin server: `packages/mcp/src/thin/server.ts:29-31`; daemon discovery/start/replace: `thin/daemon-client.ts:88-205`.
- Daemon lifecycle: `daemon/daemon.ts:81-272`; wiring: `daemon/main.ts`.
- Relay HTTP/WS: `relay/server.ts:36-177`; security checks: `:181-206`.
- CDP bridge purpose and fake target handling: `relay/bridge.ts:75-77`, `:434-566`, `:572-611`, `:612-730`.
- Browser slot / Playwright connect: `relay/browser-slot.ts:109-136`.
- Backend page/cdp/release: `relay/backend.ts:623-670`.
- Engine call/locking: `commands/engine.ts:183-255`; step context: `:492-540`.
- Command interface: `commands/commands.ts:44-112`; 28 command definitions: `:418-1196`.
- Hidden-tab handling: `commands/hidden-tabs.ts:1-61`.
- Pairing crypto: `protocol/src/index.ts:44-96`; identity: `daemon/identity.ts`.
- Extension exposure rules: `extension-core/background/exposure.ts`; incognito/Stop: `browser-control.ts:400,503`.

## Appendix B — Measurement commands

```sh
# Build (no source changes)
pnpm install --frozen-lockfile && pnpm build

# Node daemon baseline: temp home, random port, no extension
BROWSER_MCP_HOME=$(mktemp -d) node packages/mcp/dist/daemon.js --port 9411

# Go relay prototype (throwaway, /tmp only)
mkdir -p /tmp/gobench && cd /tmp/gobench
go mod init gobench && go get github.com/coder/websocket
go build -o gobench .
```

The benchmark scripts used for this report are throwaway and live outside the repository. Phase 2 moves them into `bench/` with fixed workloads and JSON output.

---

## Appendix C — Phase 2 result (2026-11-23)

Phase 2 landed the first optimisation: `playwright-core` is now loaded on first browser use
(`relay/browser-slot.ts`), so an idle daemon no longer pays its import. Medians of three
benchmark runs on the same machine:

| Metric                      | Phase 1 baseline |                Phase 2 |
| --------------------------- | ---------------: | ---------------------: |
| Cold start to `/status`     | 352 ms (one run) | 207.6 ms (median of 3) |
| Idle RSS after 10 s         |         118.3 MB |                70.7 MB |
| CPU after 10 s              |           0.42 s |                 0.22 s |
| `tools/list` p50 / p95      |   0.12 / 0.27 ms |         0.15 / 0.36 ms |
| `tools/call help` p50 / p95 |   0.17 / 0.36 ms |         0.21 / 0.74 ms |

The sub-millisecond differences are within run-to-run noise; the startup and memory gains
are the real result. Checks run and passed: `pnpm lint`, `pnpm format`, `pnpm typecheck`,
`pnpm test` (192 MCP tests, 101 extension-core tests, 1 extension test; 7 skipped), and
`pnpm build`.

The daemon still loads the engine and its dependencies at startup. What remains above bare
Node is mostly the daemon's own modules and WebSocket/MCP dependencies, not Playwright.

---

## Appendix D — Phase 3/4 result

The opt-in Go front door is implemented and tested: see
`docs/go-migration/phase-3-go-front-door.md`. It owns the public loopback port,
`daemon.json`, token/Host/Origin checks and idle exit, and proxies `/client` and
`/extension` to the TypeScript engine worker. All 28 browser commands still run
through that worker; the engine itself remains TypeScript, and the measured
memory trade-off is documented in the Phase 3 report.
