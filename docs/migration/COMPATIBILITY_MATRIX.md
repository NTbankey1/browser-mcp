# Compatibility matrix

Comparison target: the TypeScript + Playwright daemon in this repository.
"Smoke" is the repository's real-extension end-to-end test; "Go test" is the
native integration test with a fake extension.

Legend: ✅ implemented and exercised, ⚠️ implemented with a documented
difference, ❌ not implemented.

## Commands

| Command                               | Native | Evidence                                 | Notes                                                                                                                                                                                                                                               |
| ------------------------------------- | ------ | ---------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `goto`                                | ✅     | smoke (form, lazy, reads, upload, files) | New tab created when none; load wait 10 s, `readyState` fallback                                                                                                                                                                                    |
| `back`                                | ✅     | Go test                                  | `Page.getNavigationHistory` + `navigateToHistoryEntry`                                                                                                                                                                                              |
| `forward`                             | ✅     | Go test                                  | as above                                                                                                                                                                                                                                            |
| `reload`                              | ✅     | Go test                                  | `Page.reload`                                                                                                                                                                                                                                       |
| `click`                               | ✅     | smoke (form, zoom, lazy, counter)        | Actionability + `Input.dispatchMouseEvent`; double/right/middle/modifiers                                                                                                                                                                           |
| `hover`                               | ✅     | Go test                                  | `mouseMoved` with cursor overlay                                                                                                                                                                                                                    |
| `drag`                                | ✅     | Go test                                  | mouse down/move×8/up                                                                                                                                                                                                                                |
| `scroll`                              | ✅     | smoke (lazy list, `scroll bottom`)       | page or largest scroll area; `--in` scrolls a container                                                                                                                                                                                             |
| `fill`                                | ✅     | smoke (form)                             | native value setter + input/change; `--submit`                                                                                                                                                                                                      |
| `type`                                | ✅     | Go test                                  | key-by-key up to 200 chars, then `Input.insertText`; `--slowly`, `--paste`                                                                                                                                                                          |
| `press`                               | ✅     | Go test                                  | documented keys + single characters; combinations                                                                                                                                                                                                   |
| `select`                              | ✅     | Go test                                  | by value/label/text, multiple                                                                                                                                                                                                                       |
| `check` / `uncheck`                   | ✅     | smoke (form checks)                      | clicks only when the state differs                                                                                                                                                                                                                  |
| `upload`                              | ⚠️     | smoke                                    | Reads through the thin process (MCP roots), copies to a temp directory, `DOM.setFileInputFiles`. File inputs only; a file chooser opened by a click is not intercepted                                                                              |
| `wait`                                | ✅     | smoke (`--text`)                         | ms, `--text`, `--gone`, `--url`, target visible                                                                                                                                                                                                     |
| `dialog`                              | ✅     | Go test                                  | `Page.handleJavaScriptDialog`; a dialog stops the batch                                                                                                                                                                                             |
| `tab list/new/select/close`           | ✅     | smoke (tabs, second profile)             | `new` opens in the session's browser and notes it when several are connected                                                                                                                                                                        |
| `browser list/use`                    | ✅     | smoke (second profile)                   | IDs `b1`…, labels, default marker                                                                                                                                                                                                                   |
| `snapshot`                            | ⚠️     | smoke (reads)                            | `-i`, `--depth`, `--max`, scoped target; see snapshot deviations                                                                                                                                                                                    |
| `find`                                | ✅     | smoke (reads)                            | case-insensitive, ancestor path                                                                                                                                                                                                                     |
| `find --image` / `click --image`      | ✅     | smoke (test 25)                          | Template matching (`internal/vision`): grayscale NCC pyramid, multi-scale, PNG alpha mask, `--in <target>` region; `[image-not-found]`, `[image-ambiguous]`. **The TypeScript daemon has no equivalent**, so this works only with the native engine |
| `get text/value/count/attr/url/title` | ✅     | smoke (reads, form, upload)              | `--max` cap                                                                                                                                                                                                                                         |
| `screenshot`                          | ✅     | smoke (reads, upload paths)              | viewport/element/full; JPEG ≤1568 px longest side; `--path`                                                                                                                                                                                         |
| `pdf`                                 | ✅     | smoke (files)                            | `Page.printToPDF`, print backgrounds, CSS page size                                                                                                                                                                                                 |
| `download`                            | ✅     | smoke (files)                            | fetch in the page (cookies, CORS), `Content-Disposition` and `download` names, 25 MB cap                                                                                                                                                            |
| `eval`                                | ✅     | smoke (reads, zoom)                      | function or expression; element argument with a target                                                                                                                                                                                              |
| `console`                             | ✅     | smoke (reads)                            | console + uncaught errors, 500 each, `--errors`                                                                                                                                                                                                     |
| `requests` / `request`                | ✅     | smoke (reads)                            | 500 requests, `--static`, `--filter`, headers, body                                                                                                                                                                                                 |
| `cdp`                                 | ⚠️     | Go test                                  | Only with `--caps cdp`. Shares the tab's debugger session; see deviations                                                                                                                                                                           |
| `help`                                | ✅     | smoke (form/bench)                       | Text is close to, not byte-identical with, the TypeScript help                                                                                                                                                                                      |

## Snapshot and locators

| Area                                          | Native                                                                         | Difference                                                                                                                                                      |
| --------------------------------------------- | ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Accessibility source                          | `Accessibility.getFullAXTree`                                                  | Chrome's roles and names, same as Playwright's source                                                                                                           |
| Ref format                                    | `e1`, `e2`… (main frame)                                                       | No `fN` frame prefixes; refs from a frame render as `eN` too                                                                                                    |
| Root node                                     | Omitted                                                                        | Playwright starts at the body as well                                                                                                                           |
| `-i` filter                                   | Interactive roles, landmarks, headings, cells, ancestors                       | Uses the same role sets as the TypeScript filter                                                                                                                |
| `cursor=pointer`                              | Not emitted                                                                    | Cosmetic                                                                                                                                                        |
| `getByRole`                                   | Whitelisted in-page resolver                                                   | Implicit roles cover button, link, inputs, textarea, select, img, headings, lists, nav/main/header/footer/form/table/row/cell, dialog. ARIA role attribute wins |
| Accessible name                               | aria-label/labelledby, labels, alt, title, placeholder, text for a set of tags | Playwright's full accname algorithm (including CSS generated content) is not reproduced                                                                         |
| `getByText`                                   | `innerText` contains/exact                                                     | Closest-element semantics may differ for deeply nested text                                                                                                     |
| `getByLabel/Placeholder/AltText/Title/TestId` | Implemented                                                                    | `getByTestId` accepts `data-testid`, `data-test-id`, `data-test`                                                                                                |
| `locator(css)`                                | Implemented                                                                    | CSS via `querySelectorAll`; no shadow-DOM piercing                                                                                                              |
| `filter({hasText, hasNotText, visible})`      | Implemented                                                                    | —                                                                                                                                                               |
| `first/last/nth`                              | Implemented                                                                    | —                                                                                                                                                               |
| `frameLocator/contentFrame`                   | Same-origin frames only                                                        | Cross-origin (OOPIF) frames cannot be reached from the page's JavaScript                                                                                        |
| `includeHidden`                               | Ignored                                                                        | Hidden elements are included                                                                                                                                    |
| Locator safety                                | Parsed, literals only, whitelisted methods                                     | No model-supplied code runs as a locator                                                                                                                        |

## Engine behaviour

| Behaviour                  | Native                                          | Difference                                                                      |
| -------------------------- | ----------------------------------------------- | ------------------------------------------------------------------------------- |
| Multi-line batch           | Parse all lines first; stop at first error      | Same                                                                            |
| Status line                | `✓ <echo>` + notes; `✗ step N: …`               | Same policy                                                                     |
| Response cap               | 80,000 chars, 2,000 reserved for status/trailer | Same                                                                            |
| Read retry after re-attach | Up to 3 times, 150 ms apart, reads only         | Playwright's bridge re-attaches transparently and the engine repeats reads once |
| Tab locking                | Per session and per tab                         | Same                                                                            |
| Idle release               | 10 minutes                                      | Same                                                                            |
| Hidden tabs                | Focus emulation on claim and before input       | Same, with the same model message                                               |
| Dialogs                    | Block later steps; batch stops                  | Same                                                                            |
| Tab opened by a page       | Noted on the step (`opened tab N "…"`)          | Same                                                                            |
| Multi-browser              | Public IDs, per-browser selection, reconnect    | Same scheme                                                                     |

## Raw CDP (`--caps cdp`)

| Aspect         | TypeScript                                                                                          | Native                                                                                                 |
| -------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Session        | Separate CDP session of the agent's own                                                             | The tab's debugger session, shared with the engine                                                     |
| Domain enables | Counted per session                                                                                 | Tracked in the engine; disabled on release                                                             |
| Overrides      | `Fetch`, `Emulation.*`, `Network.*`, `Page.setBypassCSP`, debugger breakpoints are reset on release | Debugger resumed, enabled domains disabled; individual Emulation/Network/Fetch resets are not replayed |
| Events         | Buffered (500), `events`, `wait`                                                                    | Same                                                                                                   |

The shared session is the main parity gap for `cdp`; a tab has one debugger
attachment and `chrome.debugger` does not expose a second one. A future version
can create an isolated session by using a second debugger client in the
extension (a protocol change).

## Security parity

| Control                               | TypeScript daemon         | Native daemon        |
| ------------------------------------- | ------------------------- | -------------------- |
| Loopback bind                         | `127.0.0.1`               | `127.0.0.1`          |
| Host check                            | yes                       | yes                  |
| Token, constant time                  | yes                       | yes                  |
| Origin allowlist + dev IDs            | yes                       | yes                  |
| ECDSA P-256 pairing, commitment, code | yes                       | yes, byte-compatible |
| Pairing before tabs                   | extension-enforced        | extension-enforced   |
| Incognito / Stop / browser pages      | extension-enforced        | extension-enforced   |
| `/cdp` exposure                       | token-protected, loopback | not exposed          |
| Payload cap                           | 256 MB                    | 256 MB               |
| File modes                            | 0600/0700, atomic         | 0600/0700, atomic    |
| File access scope                     | full user permissions     | same (documented)    |

## Test evidence

- Real extension, real Chromium: `pnpm smoke` with the native engine — **20/20
  sections**, including pairing, Stop/Resume, service-worker restart, daemon
  restart mid-session, two AI apps, two Chrome profiles, iframes, uploads, PDF
  and download.
- Go unit and integration: `go test -race ./...` — all packages pass.
- TypeScript regression: `pnpm typecheck`, `pnpm test`, `pnpm lint`,
  `pnpm format` — all pass.
