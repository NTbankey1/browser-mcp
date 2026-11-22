# Real end-to-end smoke test report

**Date:** 2026-11-23
**Machine:** Fedora Linux 7.3.4, x86_64
**Runtime:** Node v26.13.1, Go 1.27.4, Playwright 1.65.0, Chromium 1254
**Harness:** `apps/extension/scripts/smoke-browser-control.ts`
**Logs:** [Node daemon](e2e-logs/smoke-node-2026-11-23.log),
[Go front door](e2e-logs/smoke-go-front-door-2026-11-23.log)

## What was run

The repository's real smoke test, not a mock:

- builds the extension in development mode and loads it in **headless Chromium**
  (Chrome for Testing 1254, launched without Playwright's anti-throttling flags
  so background tabs are hidden as in the user's Chrome);
- runs the built MCP server (`packages/mcp/dist/index.js`) over **stdio** with
  the MCP SDK client, as an AI app does;
- lets the extension find the daemon on a random port and **clicks Allow in the
  real popup**, checking the six-digit pairing code;
- drives real pages, real tabs, real files and real popups;
- uses a temporary `BROWSER_MCP_HOME` and random ports, and never touches the
  user's `~/.browsermcp` or ports 9009–9012.

Two runs on the same build:

| Run                   | Command                                                                        | Result           |
| --------------------- | ------------------------------------------------------------------------------ | ---------------- |
| Node daemon (default) | `pnpm --filter @browsermcp/extension smoke`                                    | **20/20 passed** |
| Go front door         | `BROWSER_MCP_DAEMON=go … node apps/extension/scripts/smoke-browser-control.ts` | **20/20 passed** |

Both runs exited `0`. No `browsermcpd` or `dist/daemon.js` process was left
behind, and ports 9009–9012 stayed free.

## Scenarios covered

Every section passed in both runs:

- legacy 0.1.x server gets the update message;
- no extension: `NO_EXTENSION_MESSAGE`;
- pairing: the tool asks for **Allow** with the popup's code;
- a form flow in one MCP call (fill, check, click, read) under 600 characters;
- agents' tabs: a background tab in the app's named, colored group, and the
  badge;
- reads: `snapshot -i`, `find`, `get`, `eval`, `screenshot`, `console`,
  `requests`;
- `tabs`: list, new, select, close;
- a click at 125% zoom;
- pages that open tabs (`target=_blank`, `window.open`);
- another extension's iframe on the page, in a closed shadow root, and one that
  comes back when removed;
- uploads and screenshot paths relative to the MCP root and absolute;
- a service worker restart;
- `pdf` and `download`, saved under the MCP root;
- two AI apps, each with its own tab group and color;
- the daemon killed mid-session, then the next call restarting it;
- Stop and Resume in the popup;
- `tab list`;
- a second Chrome profile: both browsers in `tab list`, `tab select` across
  them;
- a different daemon key asking for **Allow** again with a different code.

## What the Go run proves

Because the same 20 sections pass with `BROWSER_MCP_DAEMON=go`, the Go front
door really works end to end, not only in unit tests:

- `/client` transparency: the thin process, MCP framing, reverse file requests,
  errors and timeouts all pass through unchanged;
- `/extension` transparency: the real extension connects, pairs and stays
  connected through the proxy;
- **advertised-port pairing**: the extension dials the front door's port, the
  worker signs that port, and the popup's code matches;
- service-worker restart and daemon restart mid-session: the front door starts
  a new worker, the extension reconnects, and the paired key survives;
- multi-browser and multi-app tab groups;
- Stop/Resume, iframes, uploads, downloads, PDF and screenshots.

## Fix found by this test

The smoke test kills the daemon with `SIGKILL` in the "daemon restarting
mid-session" section. Without a fix, the Node worker started by the Go front
door would be orphaned, because it was started with `--no-idle-exit`.

Fixed:

- Linux: the worker is spawned with `Pdeathsig = SIGTERM`, so the kernel
  terminates it when the front door dies, including on `SIGKILL`;
- macOS/Windows: the worker keeps its own 10-minute idle exit as a fallback
  (`--no-idle-exit` is only passed on Linux), so an abruptly killed front door
  cannot leave it running forever.

Both changes are covered by the two end-to-end runs above and by
`go test -race ./...`; the package cross-compiles for Linux, macOS and Windows.

## Not covered by this test

- **Headed Chrome.** The smoke uses headless Chromium for Testing. The Cancel
  reason on Chrome's debugging bar needs headed Chrome and is documented as not
  covered by the harness; it stays covered by extension unit tests and a manual
  check.
- **The user's daily Chrome profile.** The test uses a fresh, temporary profile
  and the development extension build with the fixed self-built ID, not the
  Chrome Web Store build.
- **macOS and Windows.** The Go package cross-compiles, but the smoke test was
  run on Linux only.
- **Long soak / heavy load.** The smoke is a functional pass, not a 24-hour
  soak or a benchmark under many concurrent sessions.
- **Native Go engine.** These runs exercise the Go front door with the existing
  TypeScript engine worker. The Playwright engine itself is unchanged.

## Reproduce

```sh
pnpm install --frozen-lockfile
pnpm build

# Node daemon
pnpm --filter @browsermcp/extension smoke

# Go front door (the binary is built to bin/, so pnpm build does not remove it)
pnpm --filter @browsermcp/mcp go:build
BROWSER_MCP_DAEMON=go \
BROWSER_MCP_GO_DAEMON="$PWD/packages/mcp/bin/browsermcpd" \
  node apps/extension/scripts/smoke-browser-control.ts --out /tmp/smoke-go

# Process hygiene
pgrep -af 'browsermcpd|dist/daemon.js'
```

Screenshots: `apps/extension/.output/smoke/` (Node run) and `/tmp/smoke-go/`
(earlier Go run); the final Go run wrote to `/tmp/smoke-go-final/`. They are
not committed.

## Post-report fixes (same day)

- The Go front door now adds extension IDs from each client's `hello`, matching
  the Node daemon (`TestClientHelloExtensionIDsAreAllowed`). Before this, a
  dev/custom ID that only arrived in a running client's hello could be rejected
  by the front door before the worker saw it.
- The worker checks the front door's `/status` every 15 s and exits after two
  misses; Linux also uses `Pdeathsig`. An abruptly killed front door cannot leave
  it running.
- The Go binary now builds into `bin/`, which `tsup` does not delete; it reads
  the package version at startup and finds `dist/daemon.js` automatically.
- `--max` in `find`, `get` and `snapshot` help now says it caps characters, not
  matches.
- A screenshot timeout now suggests expanding a collapsed tab group or bringing
  the tab forward.

## Real Brave profile demo

Beyond the headless smoke, the final `bin/browsermcpd` was driven against the
running Brave with the unpacked self-built extension:

- local fixture: fill, upload (`upload.txt`, one file), click, `"Hello, Bao"`;
- screenshot **1568×903 JPEG** (18,139 bytes), PDF **1 page** (104,639 bytes),
  CSV download **26 bytes**;
- Hacker News: `snapshot -i`, `find "Show HN"`, `requests --static`
  (HTML/CSS/JS/SVG);
- heavy call 3.1 s with a warm worker; HN 1.65 s.

A first attempt hit the 5 s screenshot timeout; a retry passed. The likely cause
is a hidden or collapsed tab group (the page was in the app's background group).
The timeout message now says to expand the group or bring the tab forward.

**Environment note:** another Brave extension
(`mjdbhokoopacimoekfgkcoogikbfgngb`) injects `eduser.css`/`edreader.css` and
modifies `document.title`, so titles read `13. example.com`. This is not a
Browser MCP bug; use a clean profile for automation or read the content/URL
instead of the title.
