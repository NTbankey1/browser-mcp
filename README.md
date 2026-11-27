# Browser MCP

**Your own browser, driven by AI apps.** Claude Code, Cursor, VS Code, Codex and Claude Desktop use your signed-in sites, your profile and real tabs in Chrome, Edge, Brave, Arc or another Chromium browser — while everything stays on your computer.

[**Website**](https://browsermcp.io) · [**Docs**](https://browsermcp.io/docs) · [**Extension**](https://chromewebstore.google.com/detail/bjfgambnhccakkhmkepdoekmckoijdlc) · [**npm**](https://www.npmjs.com/package/@browsermcp/mcp) · [**Apache-2.0**](LICENSE)

> **Local by design.** The MCP server and daemon listen on `127.0.0.1` only. The extension talks to no one else, requires your **Allow** once per computer, and keeps its own rules whatever the daemon asks.

## Contents

- [What it is](#what-it-is)
- [What it can do](#what-it-can-do)
- [How it works](#how-it-works)
- [Install](#install)
- [The `browser` tool](#the-browser-tool)
- [Several browsers and tabs](#several-browsers-and-tabs)
- [Pairing, permissions and safety](#pairing-permissions-and-safety)
- [Engines](#engines)
- [Native Go engine](#native-go-engine)
- [Project structure](#project-structure)
- [Development](#development)
- [Troubleshooting](#troubleshooting)
- [FAQ](#faq)
- [Contributing and security](#contributing-and-security)
- [License](#license)

## What it is

Browser MCP is a bridge between an AI app and **your** browser. The app gets one tool, `browser`, and uses it to open pages, click, type, read the accessibility tree, inspect network traffic, take screenshots and save files — in your normal profile, with your logins, without a bundled or headless browser.

It is split into small local processes so that nothing is shared between apps except the browser itself:

| Piece                      | What it gives you                                                                                                                  |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| **Your session**           | The agent works in your profile: signed-in sites, cookies, extensions and the tabs you already have.                               |
| **Real tabs**              | Agents open background tabs in a group named and colored per app. A toolbar badge shows which app is acting. Your tabs stay yours. |
| **One small tool**         | `browser` takes many command lines in one call, so an agent can plan a whole workflow: navigate, fill, click, wait, verify.        |
| **Local and permissioned** | Loopback only, six-digit pairing, **Stop**/Resume, no incognito unless you allow it, no browser or extension pages.                |
| **Chromium browsers**      | Chrome, Edge, Brave, Arc, Opera, Vivaldi and similar, through the browser's own extension and `debugger` API.                      |

## What it can do

| Area             | The agent can…                                                                                                                | Commands                                                        |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| **Navigate**     | Open a URL, go back or forward, reload, wait for the page.                                                                    | `goto`, `back`, `forward`, `reload`                             |
| **Interact**     | Click, double-click, right-click, hover, drag, scroll — or click a control by its picture.                                    | `click`, `hover`, `drag`, `scroll`, `click --image`             |
| **Forms**        | Fill fields, type key by key for autocomplete, press keys, select options, check boxes, upload files.                         | `fill`, `type`, `press`, `select`, `check`, `uncheck`, `upload` |
| **Read**         | Print the accessibility tree with refs, search it, read text, values, attributes, URL and title, or find a picture on screen. | `snapshot`, `find`, `find --image`, `get`                       |
| **Run code**     | Evaluate JavaScript in the page and get the result as JSON.                                                                   | `eval`                                                          |
| **Capture**      | Take a JPEG screenshot of the viewport, an element or the full page; print a PDF; download a file.                            | `screenshot`, `pdf`, `download`                                 |
| **Diagnose**     | Read console messages, uncaught errors, requests and request details.                                                         | `console`, `requests`, `request`                                |
| **Tabs**         | List tabs, open a background tab, switch to any tab, close a tab it opened.                                                   | `tab list/new/select/close`                                     |
| **Profiles**     | List connected browsers and choose where new tabs open.                                                                       | `browser list/use`                                              |
| **Dialogs**      | Answer alert, confirm, prompt and beforeunload dialogs.                                                                       | `dialog accept/dismiss`                                         |
| **Raw protocol** | Send any Chrome DevTools Protocol command, when the app starts with `--caps cdp`.                                             | `cdp`                                                           |

A whole workflow fits in one call:

```text
goto https://shop.example.com
fill getByLabel('Email') "me@example.com"
fill getByLabel('Password') "correct horse battery staple"
click getByRole('button', { name: 'Sign in' })
wait --text "Welcome"
snapshot -i
```

Actions print one `✓` line; reads print their output below it. A failed step names the line and what to do next.

## How it works

```mermaid
flowchart LR
  A["AI app<br/>Claude Code, Cursor, VS Code…"] -->|"stdio · MCP"| B["MCP server<br/>one per app"]
  B -->|"ws · 127.0.0.1"| C["Daemon<br/>one per user"]
  C -->|"ws · 127.0.0.1"| D["Browser extension<br/>chrome.debugger relay"]
  D --> E["Tab<br/>your profile, signed in"]
```

| Process                          | Lifetime                                                 | Job                                                                                                                                         |
| -------------------------------- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| **MCP server** (`dist/index.js`) | one per AI app                                           | Speaks MCP over stdio, starts or replaces the daemon, forwards `tools/list` and `tools/call`, and reads or writes files where the app runs. |
| **Daemon**                       | one per user, exits 10 minutes after the last app leaves | Owns the loopback port, sessions, browser slots, pairing and the command engine.                                                            |
| **Extension**                    | your browser                                             | Relays `chrome.debugger`, enforces the tab rules, pairs with the daemon, shows the popup and badge.                                         |

Ports: the daemon takes the first free of `9010`, `9011`, `9012` (or `--port <n>`, set the same in the extension's settings). State lives in `~/.browsermcp/` (0700): `daemon.json`, `identity.json`, `daemon.log`.

## Install

### From npm

1. Install the [Browser MCP extension](https://chromewebstore.google.com/detail/bjfgambnhccakkhmkepdoekmckoijdlc) and pin it to the toolbar.
2. Add the server to your AI app and restart it:

   ```sh
   # Claude Code
   claude mcp add --scope user browsermcp -- npx @browsermcp/mcp@latest
   ```

   ```json
   // Most other apps (Cursor, Claude Desktop…)
   {
     "mcpServers": {
       "browsermcp": {
         "command": "npx",
         "args": ["@browsermcp/mcp@latest"]
       }
     }
   }
   ```

3. Ask the agent to open a page. The first time, it tells you to click **Allow** in the extension's popup: check that the popup's six-digit code matches the one the agent shows.

### From source

You need Node.js 24+, pnpm (`corepack enable`) and a Chromium browser. Firefox and Safari have no equivalent `debugger` API, so Browser MCP does not work there. The native Go engine additionally needs Go 1.24+.

```sh
git clone https://github.com/NTbankey1/browser-mcp.git browsermcp
cd browsermcp
pnpm install
pnpm build
```

1. Open your browser's extensions page (`chrome://extensions`, `edge://extensions`, `brave://extensions`), turn on **Developer mode**, click **Load unpacked**, and pick `apps/extension/.output/chrome-mv3`. It is called **"Browser MCP (self-built)"**.
2. Run the server you built in your AI app:

   ```json
   {
     "mcpServers": {
       "browsermcp": {
         "command": "node",
         "args": ["/path/to/browsermcp/packages/mcp/dist/index.js"]
       }
     }
   }
   ```

3. Restart the AI app and click **Allow** when the popup asks, matching the code.

> **Two Browser MCPs in one profile?** Turn off the Chrome Web Store extension if you load the self-built one: both would connect as two browsers. Other profiles may run either build; agents can use tabs in each.

Every build of this extension has the same ID (`imlplcjlhgpajopdbpbngiadkcmdbgob`, from the `key` in `apps/extension/extension-key.ts`), which the server accepts besides the Chrome Web Store's. A build with another key needs its ID in `BROWSER_MCP_EXTENSION_IDS` (comma-separated) or on a line of `~/.browsermcp/dev-extension-ids`.

## The `browser` tool

One tool, one `commands` string, **one command per line**. The engine parses every line first (a typo runs none of it), then runs them in order, stopping at the first error.

### Actions

| Command                                                                                     | What it does                                                                                                                                                                                                                                                                                                                                            |
| ------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `goto <url> [--wait commit\|dom\|load]`                                                     | Opens a URL in the agent's tab, creating one on the first call. Waits for the load event; `--wait dom` stops at DOMContentLoaded and `--wait commit` once the page is received (its resources keep loading), the fastest for heavy pages.                                                                                                               |
| `back` · `forward` · `reload`                                                               | History and reload, waiting for the page.                                                                                                                                                                                                                                                                                                               |
| `click <target>` · `click --image <file>`                                                   | Scrolls into view, checks it is visible, enabled and uncovered, then clicks. `--double`, `--button right\|middle`, `--modifiers Shift,Control`. `--image` finds a picture (PNG or JPEG) in the viewport and clicks its center, for a control no selector can name — a canvas, a closed shadow root, a cross-origin frame; `--min-score` (0.9), `--nth`. |
| `fill <target> "<text>"`                                                                    | Replaces a field's value at once. `--submit` presses Enter.                                                                                                                                                                                                                                                                                             |
| `type "<text>"`                                                                             | Types key by key into the focused field (autocomplete, search-as-you-type). `--slowly`, `--paste`, `--submit`.                                                                                                                                                                                                                                          |
| `press <key>`                                                                               | `Enter`, `Tab`, `Escape`, `ArrowDown`, `Control+a`, `Meta+Shift+K`…                                                                                                                                                                                                                                                                                     |
| `select <target> "<value>"…`                                                                | Selects options by value or label.                                                                                                                                                                                                                                                                                                                      |
| `check <target>` · `uncheck <target>`                                                       | Checkboxes, radios and switches.                                                                                                                                                                                                                                                                                                                        |
| `hover <target>` · `drag <source> <target>`                                                 | Pointer movement.                                                                                                                                                                                                                                                                                                                                       |
| `scroll down\|up [<px>]` · `scroll top\|bottom` · `scroll <target>`                         | Page or scroll-area scrolling; `--in <target>` targets a container.                                                                                                                                                                                                                                                                                     |
| `wait <ms>` · `wait --text "…"` · `wait --gone "…"` · `wait --url <glob>` · `wait <target>` | Event-driven waiting instead of fixed sleeps.                                                                                                                                                                                                                                                                                                           |
| `upload <target> <path>…`                                                                   | Sets files on a file input.                                                                                                                                                                                                                                                                                                                             |
| `download <url>\|<target>`                                                                  | Fetches a file as the page would (its cookies, CORS applies).                                                                                                                                                                                                                                                                                           |
| `dialog accept ["<text>"]` · `dialog dismiss`                                               | Answers alert, confirm, prompt and beforeunload.                                                                                                                                                                                                                                                                                                        |

### Reading and diagnostics

| Command                                                                                    | What it does                                                                                                                      |
| ------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| `snapshot [<target>] [-i] [--depth N] [--max <chars>\|all]`                                | Accessibility tree with refs (`e12`). `-i` keeps interactive elements, landmarks, headings and cells.                             |
| `find "<text>"`                                                                            | Snapshot lines whose name, text or URL matches, with their ancestors.                                                             |
| `find --image <file>`                                                                      | Where a picture is on screen, its score and the element under it; `[image-not-found]`, `[image-ambiguous]` (answer with `--nth`). |
| `get text\|value\|count [<target>]` · `get attr <name> <target>` · `get url` · `get title` | Read the page or an element. `--max` caps output.                                                                                 |
| `screenshot [<target>] [--full] [--path <file>]`                                           | JPEG, longest side ≤ 1568 px.                                                                                                     |
| `console [--errors]` · `requests [--static] [--filter "…"]` · `request <id> [--body]`      | Console messages, network log and request details.                                                                                |
| `eval "<function>" [<target>]`                                                             | Runs JavaScript in the page and prints the result as JSON.                                                                        |
| `pdf [<path>] [--landscape]`                                                               | Prints the page to a PDF.                                                                                                         |
| `cdp …`                                                                                    | Raw Chrome DevTools Protocol, only with `--caps cdp`.                                                                             |

### Tabs and browsers

| Command                                       | What it does                                                                                          |
| --------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `tab list`                                    | Every tab, `*` marking the agent's current one; with several browsers, each tab names its browser.    |
| `tab new [<url>]`                             | Opens a background tab of the agent's own, in its app group. `--browser "<name>"` opens it elsewhere. |
| `tab select <id>`                             | Switches to any tab, including the user's.                                                            |
| `tab close [<id>]`                            | Closes a tab the agent opened.                                                                        |
| `browser list` · `browser use "<name>"\|<id>` | Lists connected browsers (profiles) and chooses where new tabs open.                                  |

### Targets

A target is any of:

- a **ref** from `snapshot`, such as `e12`;
- a **locator**: `getByRole('button', { name: 'Submit' })`, `getByLabel('Email')`, `getByText('…')`, `getByPlaceholder('…')`, `getByTestId('…')`, `locator('css')`, with `.first()`, `.nth(i)`, `.filter({ hasText: '…' })`;
- a **CSS selector** (quote it if it has spaces).

> **Tip — fewer round trips.** Act in batches. Targets like `getByLabel('Email')` work without a snapshot, so a form can be filled in one call. Look with `snapshot -i` or `find "<text>"`; re-snapshot after navigating, because refs from an older page fail.

## Several browsers and tabs

- The daemon connects up to **8 browser profiles** at once and names them after the browser (`Chrome`, `Edge`, `Brave`…, numbered when there are several), or by the label you set in the popup.
- Browser tab IDs only mean something inside one profile, so the daemon gives every tab a **public ID** that works across all of them.
- New tabs open in the browser of the tab the session last used, or the one the user focused last. `browser use "<name>"` changes that; `tab new --browser "<name>"` changes it for one tab.
- Pairing, Stop, activity and tab releases are **per browser**: one profile waiting for **Allow** does not stop agents in another.

## Pairing, permissions and safety

| Control               | What it means                                                                                                                                                                                                                                            |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Loopback only**     | The server binds `127.0.0.1`; the Host header is checked against DNS rebinding.                                                                                                                                                                          |
| **Six-digit pairing** | The daemon's ECDSA P-256 key signs a challenge bound to the port; a commitment and reveal produce a code both sides show. Click **Allow** once per computer.                                                                                             |
| **Token**             | `/client` and `/shutdown` require a random token from `daemon.json`, compared in constant time.                                                                                                                                                          |
| **Extension rules**   | No incognito tabs unless you allow the extension in incognito; no browser pages (`chrome://`, `edge://`…), extension pages or the Web Store; nothing while **Stop** is pressed.                                                                          |
| **Stop / Resume**     | The popup (or Cancel on the browser's debugging bar) freezes agent actions; Resume restores them.                                                                                                                                                        |
| **Tab ownership**     | Agents open their own tabs and may close only those; your tabs stay unless they select one.                                                                                                                                                              |
| **Files**             | Uploads and downloads use the AI app's permissions and MCP roots. Relative paths resolve against the app's root; `~` is your home directory. Nothing limits which files a page-driven agent could read or overwrite, so treat browsed pages accordingly. |
| **Local state**       | `~/.browsermcp/` is 0700; `daemon.json` and `identity.json` are 0600 and written atomically.                                                                                                                                                             |

Nothing is sent to a Browser MCP server: there is no hosted component. What the agent reads from a page is, of course, visible to the AI app you gave it to.

## Engines

The daemon has two implementations. Both speak the same extension protocol, pair the same way, and expose the same `browser` tool; the extension does not know which one is running.

| Mode              | What runs                                            | Enable                                              |
| ----------------- | ---------------------------------------------------- | --------------------------------------------------- |
| **Default**       | Node daemon with the TypeScript + Playwright engine  | nothing                                             |
| **Go front door** | Go daemon handing the connections to the Node engine | `BROWSER_MCP_DAEMON=go`                             |
| **Native Go**     | Go daemon with the Go browser engine                 | `BROWSER_MCP_DAEMON=go` `BROWSER_MCP_ENGINE=native` |

Where the work happens:

```text
MCP server ──▶ daemon ──┬── command engine ──▶ browser slots ──▶ extension ──▶ tab
                        ├── sessions and tab locks
                        ├── pairing and identity
                        └── file requests to the MCP server
```

## Native Go engine

Build it beside the server:

```sh
pnpm go:build        # builds packages/mcp/bin/browsermcpd
```

Then set both variables in the AI app's MCP config (or the shell that starts it):

```json
{
  "mcpServers": {
    "browsermcp": {
      "command": "node",
      "args": ["/path/to/browsermcp/packages/mcp/dist/index.js"],
      "env": {
        "BROWSER_MCP_DAEMON": "go",
        "BROWSER_MCP_ENGINE": "native"
      }
    }
  }
}
```

The MCP server starts the Go daemon on first use. The extension connects exactly as before and stays paired: both engines use the same `~/.browsermcp/identity.json`. Remove `BROWSER_MCP_ENGINE` to return to the Node engine; see [ROLLBACK.md](docs/migration/ROLLBACK.md).

The native engine covers the same commands, pairing, tab IDs, reconnects and multi-browser behaviour. Its internals are described in [ARCHITECTURE.md](docs/architecture/ARCHITECTURE.md) and [PROTOCOL.md](docs/architecture/PROTOCOL.md).

## Project structure

```text
browser-mcp/
├── apps/extension/            the Chromium extension: background, popup, manifest
├── packages/
│   ├── mcp/                   the MCP server and the daemon (TypeScript)
│   │   ├── src/thin/          stdio MCP server, one per AI app
│   │   ├── src/daemon/        ports, sessions, identity, idle exit
│   │   ├── src/relay/         HTTP/WebSocket server and extension connection
│   │   └── src/commands/      the browser tool's command engine
│   ├── protocol/              extension↔daemon frames and pairing
│   └── extension-core/        browser control shared by extensions
├── go/                        the Go daemon and native engine
│   ├── cmd/browsermcpd/       the daemon binary
│   └── internal/
│       ├── relay/             loopback HTTP/WebSocket and security checks
│       ├── client/            thin-process protocol, tools and file I/O
│       ├── extension/         extension connection, pairing, ping
│       ├── cdp/               Chrome DevTools Protocol transport
│       ├── browser/           slots, public tab IDs, claims
│       ├── engine/            commands, snapshots, locators, actions, output
│       ├── pairing/           ECDSA identity and the six-digit code
│       └── daemon/            lifecycle, ports, daemon.json
└── docs/                      architecture, protocol, migration, benchmarks
```

Each layer has one job:

| Layer                    | Responsibility                                                                                 |
| ------------------------ | ---------------------------------------------------------------------------------------------- |
| **MCP server**           | One per AI app. Translates MCP over stdio into a loopback session and serves the app's files.  |
| **Relay**                | Owns the loopback port and the security boundary: Host, token, extension origins, payload cap. |
| **Extension connection** | One per browser profile: handshake, pairing code, requests, events, liveness.                  |
| **CDP transport**        | Claims a tab, sends DevTools commands, passes child-frame events.                              |
| **Browser registry**     | Which profiles are connected, which tabs exist, their public IDs, and which are claimed.       |
| **Command engine**       | Parses command lines, runs them in order, keeps sessions and tab locks, and shapes the output. |

## Development

| Command                                          | What it checks                                                                                                                                                                                                                                                                     |
| ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pnpm lint` · `pnpm format` (`format:fix` fixes) | Oxlint and Oxfmt.                                                                                                                                                                                                                                                                  |
| `pnpm typecheck` · `pnpm test`                   | TypeScript and Vitest for every package. Install the test browser once: `pnpm --filter @browsermcp/mcp exec playwright-core install chromium`.                                                                                                                                     |
| `pnpm go:build` · `pnpm go:test`                 | Build the Go daemon; run its tests with the race detector. `go test ./...` and `go vet ./...` work in `go/` too.                                                                                                                                                                   |
| `pnpm smoke`                                     | Builds a development extension and the server, then drives the real extension in headless Chromium as an AI app would. Random ports and a temporary `BROWSER_MCP_HOME`, so it never touches your own. Add `BROWSER_MCP_DAEMON=go BROWSER_MCP_ENGINE=native` for the native engine. |
| `pnpm bench`                                     | Per-command latency in headless Chromium (`--runs N`, `--json FILE`).                                                                                                                                                                                                              |
| `pnpm --filter @browsermcp/extension build:dev`  | A development build in `apps/extension/.output/chrome-mv3-dev`.                                                                                                                                                                                                                    |

The smoke test writes screenshots and daemon logs to `apps/extension/.output/smoke/` (`--out <dir>` elsewhere). A failed section names the step or check that failed, its error, the last call's output, and screenshots the open pages.

## Troubleshooting

| Symptom                                                           | Fix                                                                                                                                                                                          |
| ----------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| “No browser is connected”                                         | Install or update the extension; open the popup and check it connected. A 0.1.x extension only probes port 9009 — update it.                                                                 |
| “Click Allow in the Browser MCP extension's popup. The code is …” | Click **Allow** and check that its code matches the agent's six digits.                                                                                                                      |
| “Nothing matches …”                                               | Run `snapshot -i` or `find "<text>"`, then use a ref or a locator.                                                                                                                           |
| “The browser disconnected …”                                      | The service worker restarted; it reconnects within seconds. Run the call again.                                                                                                              |
| Ports 9010–9012 are taken                                         | Start with `--port <n>` on the server and set the same port in the extension's settings.                                                                                                     |
| “The extension is outdated” / “newer than this server”            | Update both sides: reload the extension and use `@browsermcp/mcp@latest`.                                                                                                                    |
| A stale daemon or odd state                                       | Look at `~/.browsermcp/daemon.log`; quitting the daemon (it exits after 10 idle minutes) or removing `daemon.json` while nothing runs is safe. `identity.json` is your pairing key: keep it. |
| Two browsers connected unexpectedly                               | Two extensions (store + self-built) are enabled in one profile. Turn one off.                                                                                                                |

## FAQ

<details>
<summary><b>Does the AI see my passwords and cookies?</b></summary>

It sees what it can read from the tabs it browses, using your session. It cannot read your browser's password manager, and Browser MCP sends nothing to a third party. Treat a page you let an agent browse as readable by that agent, and its files as writable (uploads and downloads run with your permissions).
</details>

<details>
<summary><b>Which browsers work?</b></summary>

Chromium browsers with the `debugger` API: Chrome, Edge, Brave, Arc, Opera, Vivaldi and similar. Firefox and Safari have no equivalent API, so Browser MCP does not work there. The extension is a standard Chromium (Manifest V3) extension; in Edge or Brave, install it from the Chrome Web Store where the browser allows that, or load the build from source.
</details>

<details>
<summary><b>Can I use several browser profiles?</b></summary>

Yes — up to 8 at once. Each connects as its own browser and pairs separately. Tabs keep public IDs across all of them, and `tab list` names each tab's browser.
</details>

<details>
<summary><b>Does it use my existing tabs?</b></summary>

Yes, if the agent selects one. By default it opens background tabs in a group named after the AI app, and may close only tabs it opened.
</details>

<details>
<summary><b>Can I stop it?</b></summary>

Press **Stop** in the popup (or Cancel on the browser's debugging bar). Agent calls fail until you press **Resume**.
</details>

<details>
<summary><b>Why one tool?</b></summary>

A small, stable interface keeps the agent's context focused, and one call can carry many command lines, so a workflow does not need a round trip per step. Reads print only what was asked for.
</details>

<details>
<summary><b>Where does state live?</b></summary>

`~/.browsermcp/` (0700): `daemon.json` (port, pid, token; 0600), `identity.json` (pairing key; 0600), `daemon.log`, and optionally `dev-extension-ids`. The extension keeps its own pairing decision in the browser's extension storage.
</details>

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to report bugs and send changes, and [SECURITY.md](SECURITY.md) to report a vulnerability privately.

## License

[Apache-2.0](LICENSE). Building it for yourself or your team is fine, and so is saying that your tool is compatible with Browser MCP or built on its code. If you publish a modified extension or server (to a browser extension store, npm or anywhere else others install it from), please give it another name and icon, and don't present it as the official Browser MCP or as endorsed by it, so people can tell the two apart. Keep the LICENSE and say what you changed.
