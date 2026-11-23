# Rollback

The native engine is opt-in. Rolling back is an environment change; no data
migration is needed.

## Turn the native engine off

Remove `BROWSER_MCP_ENGINE` from the AI app's MCP server configuration (or the
shell that starts it), leaving `BROWSER_MCP_DAEMON` as it was:

```json
{
  "mcpServers": {
    "browsermcp": {
      "command": "npx",
      "args": ["@browsermcp/mcp@latest"]
    }
  }
}
```

Restart the AI app, or wait for the daemon to be replaced: a thin process newer
than the running daemon asks it to exit (`POST /shutdown`) and starts its own.

To go back to the Node daemon entirely, unset both variables:

```sh
unset BROWSER_MCP_DAEMON BROWSER_MCP_ENGINE
```

To stay on the Go front door with the Node engine (the previous hybrid):

```sh
BROWSER_MCP_DAEMON=go
# BROWSER_MCP_ENGINE unset
```

## Verify

```sh
jq -r .version ~/.browsermcp/daemon.json     # the running daemon's version
tail -20 ~/.browsermcp/daemon.log            # "native engine enabled" is absent after rollback
```

In the extension popup, the browser reconnects within seconds. Pairing is kept:
`~/.browsermcp/identity.json` is the same key in every mode (the native engine
reads and writes the same file), so the user does not click **Allow** again.

## What rollback leaves behind

| Item                                                     | Kept | Notes                                                                        |
| -------------------------------------------------------- | ---- | ---------------------------------------------------------------------------- |
| `~/.browsermcp/identity.json`                            | yes  | Same key; deleting it forces re-pairing                                      |
| `~/.browsermcp/daemon.json`, log                         | yes  | Rewritten by whichever daemon runs                                           |
| `packages/mcp/bin/browsermcpd`                           | yes  | Delete it to remove the binary; the Node daemon does not use it              |
| `docs/architecture`, `docs/migration`, `docs/benchmarks` | yes  | Documentation only                                                           |
| Go sources under `go/`                                   | yes  | Not built into the npm package's runtime path unless `BROWSER_MCP_DAEMON=go` |

## If the native engine fails

Symptoms and first checks:

| Symptom                                  | Check                                                                                              |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------- |
| Tool calls say "No browser is connected" | The extension is connected to another daemon or waiting for **Allow**; popup shows the state       |
| `Click Allow …` after a restart          | The extension reconnects within seconds; the tool result names the code the popup shows            |
| Commands work, snapshots are slow        | Known: large-page accessibility decoding; use the Node engine or `-i`/`--depth`/`find` to narrow   |
| A command behaves differently            | Compare with `docs/migration/COMPATIBILITY_MATRIX.md`; switch to the Node engine for that workflow |

The Go daemon logs to `~/.browsermcp/daemon.log` (or `daemon-<port>.log` with
`--port`); the thin process logs to its stderr, shown by the AI app's MCP log.

## Re-enable

```sh
BROWSER_MCP_DAEMON=go BROWSER_MCP_ENGINE=native
```

The next daemon start picks it up; the extension reconnects and stays paired.
