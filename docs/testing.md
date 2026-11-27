# Testing

Two layers, both run against a real Chromium and the real extension.

## 1. The browser suite (automated)

```sh
pnpm test:browser      # or: tests/browser/run.sh
```

It builds the development extension and the server, launches headless Chromium
with the extension, starts the daemon in a temporary `BROWSER_MCP_HOME`, and
drives the `browser` tool over stdio as an AI app does. Every check prints one
line; screenshots, daemon logs and `results.json` land in
`apps/extension/.output/smoke/`.

**Last run: 33/33 passed** on the native Go engine
(`BROWSER_MCP_DAEMON=go BROWSER_MCP_ENGINE=native`). On the default Node engine
32 pass and test 25 is reported as skipped: image matching is native-only.

| ID    | Check                                                                                             | Evidence                                                                                                                                                                       |
| ----- | ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1     | An old 0.1.x server gets the update message                                                       | popup + tool text                                                                                                                                                              |
| 2     | No extension: `NO_EXTENSION_MESSAGE`                                                              | tool text                                                                                                                                                                      |
| 3     | Pairing: the tool asks for **Allow** with the popup's code                                        | both codes match                                                                                                                                                               |
| 4     | The form flow in one call, under 600 characters                                                   | order posted                                                                                                                                                                   |
| 5     | Agents' tabs: a background tab in the app's named, coloured group                                 | tab list + badge                                                                                                                                                               |
| 6     | Reads: `snapshot -i`, `find`, `get`, `eval`, `screenshot`, `console`, `requests`                  | outputs                                                                                                                                                                        |
| 7     | Tabs: list, new, select, close                                                                    | tab list                                                                                                                                                                       |
| 8     | A click at 125% zoom                                                                              | the right button answered                                                                                                                                                      |
| 9     | Pages that open tabs: `target=_blank` and `window.open`                                           | tab list                                                                                                                                                                       |
| 10–12 | Another extension's iframe, a closed shadow root, a frame that comes back                         | attach succeeds                                                                                                                                                                |
| 13    | **Wait for an element that appears later** (`wait --text` on a lazy list)                         | 40 items, no fixed sleep                                                                                                                                                       |
| 14    | **A slow navigation is waited for** (1.2 s response)                                              | title read after load                                                                                                                                                          |
| 15    | **A stale element fails loudly** (button removes itself after the first click)                    | a "no element"-class error, not a crash                                                                                                                                        |
| 16    | **Multi-page navigation, back and forward**                                                       | `/form` → `/second` → back → forward                                                                                                                                           |
| 17    | **A dialog is reported, not hung on**                                                             | click returns within the budget; the page ends `accepted`/`dismissed`                                                                                                          |
| 18    | **A 2 MB download**                                                                               | file on disk is exactly 2 MiB                                                                                                                                                  |
| 19    | **A file is uploaded**                                                                            | page reads `notes.txt 26`                                                                                                                                                      |
| 20    | **A session survives a service worker restart**                                                   | next call still reaches the tab                                                                                                                                                |
| 21    | **Five concurrent tabs**                                                                          | all five in `tab list`, all closed                                                                                                                                             |
| 22    | **The browser reconnects after a daemon restart**                                                 | `browser list` again                                                                                                                                                           |
| 23    | **Injected page text is data, not a command**                                                     | the text is returned; the URL is unchanged                                                                                                                                     |
| 24    | **A twenty-step workflow**                                                                        | order submitted with the four fields set                                                                                                                                       |
| 25    | **A control found by its picture** (a canvas button at 125% zoom; not found; two identical twins) | the canvas reports a click at its center; `[image-not-found]`; `[image-ambiguous]`, then `--nth 1` clicks the left twin and `--in "#right"` the right one (native engine only) |

## 2. The real-browser checklist (manual, on the user's own Chrome)

Run against live sites, not fixtures — this is what catches "works in the
harness, not on the web". Last run (Wikipedia, Wikimedia):

| Step                                                                                        | Result                                                                       |
| ------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| Open a page and read its title                                                              | ✅                                                                           |
| `snapshot -i`, open a collapsed search box, `fill`, pick a suggestion                       | ✅                                                                           |
| `get text` of the article (38 255 characters)                                               | ✅                                                                           |
| `find` a link, `eval` a paragraph and an image URL                                          | ✅                                                                           |
| `download` an image from another origin (`thumb.wikimedia.org` while on `en.wikipedia.org`) | ✅ 33.8 KB, valid PNG                                                        |
| `screenshot`                                                                                | ✅                                                                           |
| `requests --filter`, `console --errors`                                                     | ✅ (no console errors)                                                       |
| `pdf` (15 pages)                                                                            | ✅                                                                           |
| `screenshot img[alt='Wikipedia'] --path …`, then `find --image`, `click --image`            | ✅ score 1.00 on the wordmark's `<img>`; the click reached `/wiki/Main_Page` |
| `tab list`, `tab close`                                                                     | ✅                                                                           |

## Reliability and safety checks

| Check                                                                                                                                                            | Where                       |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------- |
| A read is retried after a timeout or a re-attach                                                                                                                 | `engine` unit tests + suite |
| A write is retried once, and only on `no-element`                                                                                                                | test 15                     |
| A failing step carries a class (`[no-element]`, `[timeout]`, `[http]`, `[dialog-open]`, `[disconnected]`, `[refused]`, `[image-not-found]`, `[image-ambiguous]`) | tests 15 and 25             |
| `BROWSER_MCP_ALLOW_DOMAINS` refuses another host, allows subdomains                                                                                              | `policy_test.go`            |
| `BROWSER_MCP_CONFIRM=1` reports a write instead of doing it                                                                                                      | `policy_test.go`            |
| Credential headers print as `[redacted]`                                                                                                                         | `policy_test.go`            |

## What the tests found

- **`screenshot <target>` never worked in the native engine** (and nothing
  exercised it): the element's box came back as zero because the reply was read
  one level too shallow, so every element screenshot failed with "no visible
  box". Test 25 captures a canvas to use as a template and found it; fixed.
- **Uploads failed for everyone** (`ERR_FILE_NOT_FOUND`): the staged copies were
  deleted the moment the step returned. Fixed — they live ten minutes.
- **Downloads from another site failed** (CORS on the page's own fetch). Fixed —
  an anonymous request from the daemon, no cookies.
- **A native dialog could hang a call for five minutes**: the CDP request waited
  on its own timer and ignored the step's deadline. Fixed — every step has a
  deadline (30 s by default) and the request now stops with its caller; a step
  that fails because a dialog is open names the dialog instead of "deadline
  exceeded".

## Known limits (by design or still open)

- A file behind a login on **another** origin cannot be downloaded; open it in a
  tab first.
- **Image matching** (`--image`, native engine) searches the viewport, so a
  control below the fold needs `scroll` first. The score tolerates a change of
  brightness or contrast but not of hue, and not an inverted icon (a dark-mode
  variant needs its own template). A template captured at one zoom is found at
  the others; a mid-animation frame may not be.
- A dialog's resolution is not deterministic: Chrome may answer it as the
  blocked command ends, or leave it for `dialog accept`/`dismiss`. The suite
  asserts the property that matters — the call returns, and the page ends in a
  resolved state.
- **Engine coverage**: the suite runs the Node engine by default.
  `BROWSER_MCP_DAEMON=go` runs the Go daemon, but that daemon hands calls to the
  Node engine; `BROWSER_MCP_ENGINE=native` runs the Go engine itself. The suite
  passes 32/32 on the Node engine and 33/33 on the native one (test 25 is
  skipped visibly, not counted as passed, where `--image` doesn't exist), and
  the daemon log says `native engine enabled` when the Go engine answers. Test
  15's error-class assertion only applies with the native engine, because the
  Node engine reports the message without the class.
