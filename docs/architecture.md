# Architecture

Browser MCP lets an AI app drive the user's own Chromium. Three processes are
involved, and the extension is the only one that holds browser privileges.

```
AI app ──stdio MCP──▶ @browsermcp/mcp (thin server)
                          │  local loopback + pairing
                          ▼
                     browsermcpd (Go engine, or the Node daemon)
                          ▲
                          │  WebSocket, paired by key + 6-digit code
                          ▼
                 extension (MV3, chrome.debugger relay)
                          │  CDP
                          ▼
                       the user's tabs
```

## Pieces

| Piece      | Package                                                               | Job                                                                                                          |
| ---------- | --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| Protocol   | `@browsermcp/protocol`                                                | Frame types, ports, close codes, the pairing code, `PRODUCT_NAME`, user-facing messages shared by both sides |
| Extension  | `apps/extension` (built on `@browsermcp/extension-core`)              | Pairs with a daemon, enforces the rules about tabs, relays CDP                                               |
| MCP server | `@browsermcp/mcp`                                                     | The `browser` tool over stdio: parses the command script, calls the engine, prints one line per step         |
| Engine     | `browsermcpd` (`go/`) or the Node daemon in `packages/mcp/src/daemon` | Owns the tab state, resolves targets, drives CDP, writes files                                               |

The thin server never talks to Chromium. It sends one command script to the
engine and prints the result, so the same engine serves every AI app.

## The command model

A call is a script of one command per line; it stops at the first error. Each
step is `observe → act → wait → verify` inside the engine:

- **observe**: resolve the target (snapshot ref, Playwright locator, CSS) to a node.
- **act**: dispatch the CDP input event at the element's box.
- **wait**: for the page to settle (`wait` with `--text`, `--url`, `--gone`, a
  target, or a timeout).
- **verify**: the step's own read-back, or the next line.

Actions print `✓ step N: …`; reads print their output. A failure names the step,
the command and what the page looked like (URL, ready state, visible text).

## Pairing

The extension probes `GET /browsermcp/status` on the relay ports, opens
`ws://127.0.0.1:<port>/extension` and sends `hello` with a random challenge. The
daemon answers `welcome` with its public key and a signature. A key the
extension has not paired with gets no tabs until the user clicks **Allow** in the
popup, which shows a six-digit code computed from both sides' nonces (commit →
reveal, so neither side can steer the code). The extension then stores the key.

## Trust boundaries

| Boundary                                                          | Who may cross it                                                                |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| Browser privileges (`debugger`, `scripting`, `tabs`, `tabGroups`) | the extension only                                                              |
| Which sites a tab may reach                                       | the tab itself; the engine navigates, it does not read other origins            |
| Cookies and storage                                               | never the engine — the extension refuses the CDP methods that would expose them |
| Files                                                             | only under the MCP roots; downloads are capped at 25 MB                         |

## Where things run

- The engine listens on loopback only (`127.0.0.1:<port>`), one port per
  `BROWSER_MCP_HOME` (`~/.browsermcp` by default), recorded in `daemon.json`.
- The extension connects out; it never listens.
- The MCP server speaks stdio to the AI app; nothing leaves the machine except
  the pages the user's own browser loads.

## Security-relevant refusals

`packages/extension-core/src/background/cdp-policy.ts` refuses, whatever the
daemon asks: cookie and cache commands, `Network.loadNetworkResource` (it would
fetch any URL with the user's cookies), target management, and the storage
domains. The refusal is the reason `download` falls back to an anonymous
request instead of a credentialled one — see [security.md](security.md).
