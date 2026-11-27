# Roadmap

What exists, what is missing, and in what order the gaps are worth closing.
Status is what the code does today, not what a command's name suggests.

## Capability audit

| Area                                     | Status                | Evidence                                                                                                                                                    |
| ---------------------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Explicit and condition-based waiting     | **Exists**            | `wait <ms>`, `wait --text`, `wait --gone`, `wait --url`, `wait <target>`, `--timeout`                                                                       |
| URL and navigation verification          | **Exists**            | `get url`, `goto --wait commit\|dom\|load`                                                                                                                  |
| Timeout management                       | **Exists**            | every step has a deadline (30 s by default, `--timeout` per step); no whole-script budget yet                                                               |
| Retry and error recovery                 | **Exists (bounded)**  | a read is re-run after a re-attach or a timeout; a write only when nothing ran (`no-element`), once                                                         |
| Stale element detection                  | **Exists (bounded)**  | the locator resolves again on the retry; the failure says `[no-element]`                                                                                    |
| Dialog handling                          | **Exists**            | `dialog accept`/`dismiss`; a blocked session is bounded by the step's deadline, and the failure names the dialog                                            |
| Cookie and storage management            | **Refused by design** | `cdp-policy.ts` denies the methods; see [security.md](security.md)                                                                                          |
| Download and upload lifecycle            | **Exists**            | page fetch → anonymous fallback; upload staging kept 10 minutes                                                                                             |
| Multi-tab and session isolation          | **Exists**            | `tab list/new/select/close`, one tab group per AI app                                                                                                       |
| Visual automation                        | **Partial**           | `screenshot`, `pdf`; no visual assertions or element finding by image                                                                                       |
| Workflow execution                       | **Exists**            | a call is a script of commands, one per line                                                                                                                |
| Logging and tracing                      | **Partial**           | step lines, `console`, `requests`, `request --body`; no exportable trace                                                                                    |
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

## Still open

7. **A per-script budget.** `--timeout` is per step; a long script can still run
   for minutes. A `--budget <ms>` on the call is the next step.
8. **Trace export.** Each call could append a JSON line (commands, step
   results, timing) under the MCP root, so a failure can be replayed.
9. **Visual assertions.** `screenshot` exists, but nothing compares against a
   baseline.
10. **Session snapshot/restore.** A job that must survive a restart cannot save
    its tabs and cookies (the latter by design).

## Order for the next pass

7 (budget) → 8 (trace) → 9 (visual) → 10 (snapshot). The first two are
mechanical; the last two need a design decision about where state lives.

11. **The suite does not switch engines.** `BROWSER_MCP_DAEMON=go` should make
    the server start `bin/browsermcpd`; the browser suite still reported the
    Node engine's messages. Until that is fixed, the Go engine is covered by
    `go test ./...` rather than end to end.
