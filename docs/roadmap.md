# Roadmap

What exists, what is missing, and in what order the gaps are worth closing.
Status is what the code does today, not what a command's name suggests.

## Capability audit

| Area                                     | Status                | Evidence                                                                                                                                          |
| ---------------------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| Explicit and condition-based waiting     | **Exists**            | `wait <ms>`, `wait --text`, `wait --gone`, `wait --url`, `wait <target>`, `--timeout`                                                             |
| URL and navigation verification          | **Exists**            | `get url`, `goto --wait commit\|dom\|load`                                                                                                        |
| Timeout management                       | **Partial**           | per-command `--timeout`; no global budget for a whole script                                                                                      |
| Retry and error recovery                 | **Missing**           | the engine never retries; a failed step stops the script                                                                                          |
| Stale element detection                  | **Partial**           | locators re-resolve each step; a removed element fails as "no element"                                                                            |
| Dialog handling                          | **Exists**            | `dialog accept ["<text>"]` / `dialog dismiss`; a dialog ends the call that opened it, and a blocked CDP session is bounded by the step's deadline |
| Cookie and storage management            | **Refused by design** | `cdp-policy.ts` denies the methods; see [security.md](security.md)                                                                                |
| Download and upload lifecycle            | **Exists**            | page fetch → anonymous fallback; upload staging kept 10 minutes                                                                                   |
| Multi-tab and session isolation          | **Exists**            | `tab list/new/select/close`, one tab group per AI app                                                                                             |
| Visual automation                        | **Partial**           | `screenshot`, `pdf`; no visual assertions or element finding by image                                                                             |
| Workflow execution                       | **Exists**            | a call is a script of commands, one per line                                                                                                      |
| Logging and tracing                      | **Partial**           | step lines, `console`, `requests`, `request --body`; no exportable trace                                                                          |
| Security and prompt-injection protection | **Partial**           | page text is data, cookies unreachable; no allowlist or confirmation step                                                                         |

## Phase 1 — reliability (highest value)

1. **A retry policy that is safe by construction.** Retry only read commands
   (`get`, `find`, `snapshot`, `eval`, `wait`) and only on timeouts. Writes
   (`click`, `fill`, `press`, `upload`) are never retried blindly: after a
   failure the engine re-reads the page and reports what it sees.
2. **Stale-element recovery.** When a click's target detaches between resolve and
   dispatch, re-resolve once by the same locator and report which instance was
   used.
3. **A per-call budget.** `--timeout` today is per command; add a whole-script
   budget so a long workflow cannot hang forever.

## Phase 2 — safety controls

4. **Domain allowlist.** `--allow-domain` (repeatable) that `goto` and `tab new`
   check before navigating; off by default, so existing behaviour is unchanged.
5. **Confirmation for sensitive actions.** An `--confirm` mode where a click on
   a submit control, or an upload, returns a one-line summary instead of acting.
6. **Header redaction.** `request --body` and `requests` hide `Authorization`,
   `Cookie` and `Set-Cookie` values by default.

## Phase 3 — observability

7. **Trace export.** Write each call (commands, step results, timing) to a file
   under the MCP root, so a failure can be replayed.
8. **Structured errors.** A machine-readable error code per failure
   (`no-element`, `timeout`, `http-403`, `dialog-open`, `disconnected`).

## Phase 4 — breadth

9. Visual assertions (screenshot diff against a baseline).
10. Session snapshot/restore for multi-step jobs that must survive a restart.
