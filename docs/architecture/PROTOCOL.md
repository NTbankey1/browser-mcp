# Protocol

Three boundaries, all loopback, all JSON. The native engine reproduces the
existing bytes; it does not define a new contract.

## 1. MCP over stdio

Unchanged: the TypeScript thin process serves MCP (`initialize`, `tools/list`,
`tools/call`) and sends the same `browser` tool definition and `instructions`.

## 2. Thin process ↔ daemon (`ws://127.0.0.1:<port>/client?token=…`)

The thin process sends `hello`:

```json
{
  "type": "hello",
  "protocol": 1,
  "version": "2.x",
  "client": { "name": "claude-code" },
  "capabilities": [],
  "pid": 1234,
  "extensionIds": ["…"]
}
```

The daemon answers:

```json
{ "type": "welcome", "protocol": 1, "version": "2.x", "sessionId": "…" }
```

After that both sides send `{id, method, params}` requests and
`{id, result}` / `{id, error:{message}}` responses.

| Direction       | Method       | Params → result                            |
| --------------- | ------------ | ------------------------------------------ |
| client → daemon | `tools/list` | `{}` → `{tools}`                           |
| client → daemon | `tools/call` | `{name, arguments}` → MCP `CallToolResult` |
| daemon → client | `readFile`   | `{path}` → `{name, mimeType, base64}`      |
| daemon → client | `writeFile`  | `{path, base64, exclusive?}` → `{path}`    |

`readFile`/`writeFile` run where the AI app runs, so `~` and relative paths
resolve against its MCP roots. The engine asks for them only for `upload`,
`screenshot --path`, `pdf` and `download`.

A client newer than protocol 1 is closed with code 4001 and reason
`update-server`; the thin process then replaces the daemon.

## 3. Daemon ↔ extension (`ws://127.0.0.1:<port>/extension`)

Origin must be `chrome-extension://<32 chars>` with an allowlisted ID.

The extension sends `hello` with a random base64 32-byte `challenge` and its
state. The daemon answers `welcome`:

```json
{
  "type": "welcome",
  "protocol": 1,
  "daemonVersion": "2.x",
  "capabilities": [],
  "publicKey": "<base64 SPKI>",
  "pairingCommitment": "<base64 SHA-256 of nonce>",
  "signature": "<base64 IEEE P1363>"
}
```

The signature covers the UTF-8 of:

```
browsermcp-pair-v1:<port>:<challenge>:<pairingCommitment>
```

where `<port>` is the port the extension dialed. A program on another relay port
cannot relay the challenge and pass the signature back.

For a key the extension has not paired with, it sends
`{type:"pairingNonce", nonce}`; the daemon reveals its nonce in
`{type:"pairingReveal", nonce}`. Both compute:

```
SHA-256("browsermcp-pair-code-v1:<publicKey>:<extensionNonce>:<daemonNonce>")
→ first 4 bytes big-endian mod 1,000,000, zero-padded 6 → "482 913"
```

Only then does the extension expose tabs (`state.pairing: "paired"`).

### Requests

| Method                     | Params                                 | Result                                    |
| -------------------------- | -------------------------------------- | ----------------------------------------- |
| `attach`                   | `{tabId}`                              | `{targetInfo}` (Chrome's CDP target info) |
| `detach`                   | `{tabId}`                              | —                                         |
| `cdp`                      | `{tabId, sessionId?, method, params?}` | Chrome's CDP result                       |
| `createTab`                | `{url?, app}`                          | `{tabId}`                                 |
| `closeTab` / `activateTab` | `{tabId}`                              | —                                         |
| `groupTab`                 | `{tabId, app \| null}`                 | —                                         |
| `activity`                 | `{appId}`                              | —                                         |

### Events and liveness

`cdpEvent` (`{tabId, sessionId?, method, params}`), `detached`
(`{tabId, reason}`), `state` (full tab list, paused, pairing, label,
lastFocusedAt), `tabAssigned`, `tabOpened`. The daemon sends `ping` every 20 s;
the extension answers `pong`; ten seconds without one closes the connection.

Close codes: 4001 unsupported protocol (reason `update-extension` or
`update-server`), 4002 too many browsers, 4003 daemon exiting.

## CDP over the extension

`chrome.debugger.sendCommand` already returns the command's result, so the
extension's request/response pair is the correlation; the native engine does not
invent CDP command ids.

- The engine claims a tab with `attach`; `targetInfo.targetId` is Chrome's
  target ID.
- Commands go to the tab's main session with no `sessionId`.
- Out-of-process iframe events carry a `sessionId`; commands for those sessions
  send that `sessionId` through the same tunnel.
- `Target.setAutoAttach` is requested best-effort on claim.
- `Input.*` commands are serialized by the extension per tab.

## Snapshot refs

`Accessibility.getFullAXTree` is decoded into a tree; nodes with a
`backendDOMNodeId` get refs `e1`, `e2`, … in document order. The tab keeps the
ref → backend node map; a main-frame navigation clears it, so a ref from an
older page fails instead of naming a different element. Resolution is
`DOM.resolveNode {backendNodeId}`.

## Security-relevant invariants

| Invariant                                   | Enforced by                                                    |
| ------------------------------------------- | -------------------------------------------------------------- |
| Loopback only                               | `net.Listen("tcp", "127.0.0.1:…")`                             |
| No DNS rebinding                            | Host must be `127.0.0.1`, `localhost` or `::1`                 |
| Token required on `/client` and `/shutdown` | constant-time compare; no `Origin` header allowed              |
| Extension origin allowlist                  | `chrome-extension://<a-p>{32}` plus IDs                        |
| Pairing before tabs                         | extension withholds state until the key is paired              |
| Stop / Cancel                               | extension detaches and pauses; engine reports `PAUSED_MESSAGE` |
| Incognito, browser pages, stores            | extension exposure rules (unchanged)                           |
| Payload cap                                 | 256 MB per message                                             |
| File permissions                            | home 0700, `daemon.json`/`identity.json` 0600, atomic writes   |
