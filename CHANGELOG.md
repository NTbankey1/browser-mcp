# Changelog

All notable changes to this project. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
semantic versioning.

## [Unreleased]

### Fixed

- A native dialog can no longer hang a call. Every step has a deadline (30 s
  unless it names one), the extension request stops with its caller instead of
  waiting on its own five-minute timer, and a step that fails because a dialog
  is open says which dialog instead of "deadline exceeded".
- `download`: a file on another site now downloads. The page's own fetch is
  refused by CORS for another origin, so the engine falls back to a plain
  request that carries none of the browser's cookies. Public files download from
  any site; a file behind a login still needs its own tab. The CDP route that
  would have carried the user's cookies stays refused by the extension.
- `upload`: staged copies now live for ten minutes instead of being removed the
  moment the step returned. A page reads a file when its form is submitted, not
  when the input is set, so the old cleanup made every upload fail with
  `ERR_FILE_NOT_FOUND`.

### Added

- Safe retries: a read re-runs after a re-attach or a timeout, and a write
  retries once — only when the error is `no-element`, so nothing was clicked or
  typed twice.
- Structured errors: every failed step names its class (`[no-element]`,
  `[timeout]`, `[http]`, `[dialog-open]`, `[disconnected]`, `[refused]`).
- `BROWSER_MCP_ALLOW_DOMAINS`: an opt-in allowlist for `goto` and `tab new`.
- `BROWSER_MCP_CONFIRM=1`: opt-in confirmation mode, where the commands that
  change a page report what they would do instead of doing it.
- `request` redacts `Authorization`, `Cookie`, `Set-Cookie`,
  `Proxy-Authorization` and `X-Api-Key`.

### Known

- The browser suite drives the Node engine; `BROWSER_MCP_DAEMON=go` did not
  switch it in this checkout, so the Go engine is covered by `go test ./...`.

- `PRODUCT_NAME` in `@browsermcp/protocol` and `brand.Name` in the Go daemon:
  the product's name has one source per side, and every message, the MCP
  server's name and the on-page cursor label read it.
- A popup design of its own: violet accent, pill buttons, a brand tile and a
  hairline, driven by the tokens in `style.css`.
- `docs/architecture.md`, `docs/security.md`, `docs/testing.md`,
  `docs/roadmap.md`, and the browser test suite under `tests/browser/`.

### Changed

- `wait` timeouts name the page's URL, ready state and visible text, so an agent
  can tell "the text isn't there" from "it is there, in another case or in an
  attribute such as a placeholder".
- `download`'s help says which request it uses and what that means for files
  behind a login.

## [2.2.3]

- Previous release; see the git history.
