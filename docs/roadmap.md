# Roadmap

What exists, what is missing, and in what order the gaps are worth closing.
Status is what the code does today, not what a command's name suggests.

## Capability audit

| Area                                     | Status                | Evidence                                                                                                                                                    |
| ---------------------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Explicit and condition-based waiting     | **Exists**            | `wait <ms>`, `wait --text`, `wait --gone`, `wait --url`, `wait <target>`, `--timeout`                                                                       |
| URL and navigation verification          | **Exists**            | `get url`, `goto --wait commit\|dom\|load`                                                                                                                  |
| Timeout management                       | **Exists**            | every step has a deadline (30 s by default, `--timeout` per step), and `BROWSER_MCP_CALL_BUDGET_MS` bounds the whole call                                   |
| Retry and error recovery                 | **Exists (bounded)**  | a read is re-run after a re-attach or a timeout; a write only when nothing ran (`no-element`), once                                                         |
| Stale element detection                  | **Exists (bounded)**  | the locator resolves again on the retry; the failure says `[no-element]`                                                                                    |
| Dialog handling                          | **Exists**            | `dialog accept`/`dismiss`; a blocked session is bounded by the step's deadline, and the failure names the dialog                                            |
| Cookie and storage management            | **Refused by design** | `cdp-policy.ts` denies the methods; see [security.md](security.md)                                                                                          |
| Download and upload lifecycle            | **Exists**            | page fetch → anonymous fallback; upload staging kept 10 minutes                                                                                             |
| Multi-tab and session isolation          | **Exists**            | `tab list/new/select/close`, one tab group per AI app                                                                                                       |
| Visual automation                        | **Exists**            | `screenshot --expect` compares against a baseline; `pdf`; no element finding by image                                                                       |
| Workflow execution                       | **Exists**            | a call is a script of commands, one per line; `session save`/`restore` keeps an app's tabs across calls                                                     |
| Logging and tracing                      | **Exists**            | step lines, `console`, `requests`, `request --body`, and `BROWSER_MCP_TRACE_DIR` for a per-call JSONL trace                                                 |
| Security and prompt-injection protection | **Exists (opt-in)**   | page text is data; `BROWSER_MCP_ALLOW_DOMAINS` limits navigation; `BROWSER_MCP_CONFIRM=1` reports writes instead of doing them; credential headers redacted |

## Done in this pass

1. **Retry that is safe by construction.** Reads: re-attach and timeout, up to
   three attempts. Writes: retried once, only when the error is `no-element` —
   the action never reached the page, so repeating it cannot submit twice.
2. **Stale-element recovery.** The retry re-resolves the locator, so a control
   that was replaced between calls is found again.
3. **Structured errors.** Every failing step carries a class: `[no-element]`,
   `[timeout]`, `[http]`, `[dialog-open]`, `[disconnected]`, `[refused]`.
4. **Domain allowlist (opt-in).** `BROWSER_MCP_ALLOW_DOMAINS=example.com,.test`
   checks `goto` and `tab new`; a bare domain also allows its subdomains.
5. **Confirmation mode (opt-in).** `BROWSER_MCP_CONFIRM=1` turns `click`,
   `fill`, `type`, `press`, `select`, `check`, `uncheck`, `upload`, `drag` and
   `dialog` into a report of what would run, leaving the page untouched.
6. **Credential redaction.** `request` prints `Authorization`, `Cookie`,
   `Set-Cookie`, `Proxy-Authorization` and `X-Api-Key` as `[redacted]`.
7. **A per-call budget.** `BROWSER_MCP_CALL_BUDGET_MS` bounds the whole script;
   when it ends, the call says so and the remaining lines are named as not run.
8. **Trace export.** `BROWSER_MCP_TRACE_DIR` appends one JSON line per call —
   commands, the lines the tool returned, failure flag and duration — to
   `calls.jsonl`. The file holds page text, so it is `0600` under a `0700`
   directory.
9. **Visual assertions.** `screenshot --expect <file>` writes the baseline on
   the first run and fails with `[visual-diff]` when a later screenshot differs
   from it byte for byte.
10. **Session snapshot.** `session save <file>` writes the app's tabs (URLs and
    which one was current); `session restore <file>` opens them again and makes
    the saved current tab current. Restore honours the allowlist.

## Still open

11. **Element finding by image**, once visual assertions have a baseline story.
    Restoring cookies stays refused by design (see [security.md](security.md)).

## Order for the next pass

- Element finding by image.

## Settled: the suite switches engines

`BROWSER_MCP_DAEMON=go` alone runs the **Go front door**, which hands calls to
the Node engine, so test 15's error-class check saw no `[no-element]`. With
`BROWSER_MCP_ENGINE=native` as well, the suite runs the native Go engine and
passes 32/32; the daemon log says `native engine enabled`. The test now asserts
the error class only under the native engine. See [testing.md](testing.md).
