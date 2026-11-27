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

**Last run: 32/32 passed.**

| ID    | Check                                                                            | Evidence                                                              |
| ----- | -------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| 1     | An old 0.1.x server gets the update message                                      | popup + tool text                                                     |
| 2     | No extension: `NO_EXTENSION_MESSAGE`                                             | tool text                                                             |
| 3     | Pairing: the tool asks for **Allow** with the popup's code                       | both codes match                                                      |
| 4     | The form flow in one call, under 600 characters                                  | order posted                                                          |
| 5     | Agents' tabs: a background tab in the app's named, coloured group                | tab list + badge                                                      |
| 6     | Reads: `snapshot -i`, `find`, `get`, `eval`, `screenshot`, `console`, `requests` | outputs                                                               |
| 7     | Tabs: list, new, select, close                                                   | tab list                                                              |
| 8     | A click at 125% zoom                                                             | the right button answered                                             |
| 9     | Pages that open tabs: `target=_blank` and `window.open`                          | tab list                                                              |
| 10–12 | Another extension's iframe, a closed shadow root, a frame that comes back        | attach succeeds                                                       |
| 13    | **Wait for an element that appears later** (`wait --text` on a lazy list)        | 40 items, no fixed sleep                                              |
| 14    | **A slow navigation is waited for** (1.2 s response)                             | title read after load                                                 |
| 15    | **A stale element fails loudly** (button removes itself after the first click)   | a "no element"-class error, not a crash                               |
| 16    | **Multi-page navigation, back and forward**                                      | `/form` → `/second` → back → forward                                  |
| 17    | **A dialog is reported, not hung on**                                            | click returns within the budget; the page ends `accepted`/`dismissed` |
| 18    | **A 2 MB download**                                                              | file on disk is exactly 2 MiB                                         |
| 19    | **A file is uploaded**                                                           | page reads `notes.txt 26`                                             |
| 20    | **A session survives a service worker restart**                                  | next call still reaches the tab                                       |
| 21    | **Five concurrent tabs**                                                         | all five in `tab list`, all closed                                    |
| 22    | **The browser reconnects after a daemon restart**                                | `browser list` again                                                  |
| 23    | **Injected page text is data, not a command**                                    | the text is returned; the URL is unchanged                            |
| 24    | **A twenty-step workflow**                                                       | order submitted with the four fields set                              |

## 2. The real-browser checklist (manual, on the user's own Chrome)

Run against live sites, not fixtures — this is what catches "works in the
harness, not on the web". Last run (Wikipedia, Wikimedia):

| Step                                                                                        | Result                 |
| ------------------------------------------------------------------------------------------- | ---------------------- |
| Open a page and read its title                                                              | ✅                     |
| `snapshot -i`, open a collapsed search box, `fill`, pick a suggestion                       | ✅                     |
| `get text` of the article (38 255 characters)                                               | ✅                     |
| `find` a link, `eval` a paragraph and an image URL                                          | ✅                     |
| `download` an image from another origin (`thumb.wikimedia.org` while on `en.wikipedia.org`) | ✅ 33.8 KB, valid PNG  |
| `screenshot`                                                                                | ✅                     |
| `requests --filter`, `console --errors`                                                     | ✅ (no console errors) |
| `pdf` (15 pages)                                                                            | ✅                     |
| `tab list`, `tab close`                                                                     | ✅                     |

## What the tests found

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
- A dialog's resolution is not deterministic: Chrome may answer it as the
  blocked command ends, or leave it for `dialog accept`/`dismiss`. The suite
  asserts the property that matters — the call returns, and the page ends in a
  resolved state.
- No retry layer yet; see [roadmap.md](roadmap.md).
