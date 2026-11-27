# Security model

The engine is trusted to drive the browser, not to read the user's secrets. Every
rule below is enforced in the extension, because the extension is the side that
holds the browser's privileges.

## Page content is data, never instructions

What a page says is returned to the model as text. Nothing in the engine treats
page text as a command: the command script comes from the AI app, one line at a
time, and a page cannot add a line to it. A page that says _"ignore your
instructions"_ is a string in `get text`, and the tab's URL is unchanged — that
is what `test 23` asserts.

Two things follow for anyone building on top:

- Read untrusted pages with `get text`/`find`, and act only on what you decided
  to do, not on what the page asked for.
- `eval` runs JavaScript in the page's context; the page can see the arguments
  you pass. Do not put credentials in an `eval` expression.

## What the engine cannot reach

`cdp-policy.ts` refuses these from any daemon, including raw `cdp` calls:

| Refused                                                                | Why                                                   |
| ---------------------------------------------------------------------- | ----------------------------------------------------- |
| `Network.getAllCookies`, `getCookies`, `setCookie(s)`, `deleteCookies` | every site's cookies                                  |
| `Network.clearBrowserCookies`, `clearBrowserCache`                     | destructive, cross-site                               |
| `Network.loadNetworkResource`                                          | fetches any URL with the user's cookies, without CORS |
| `Target.*` (attach, create, close, expose…)                            | other tabs and the browser itself                     |
| `Storage.*`, `IndexedDB.*`, `CacheStorage.*`, `DOMStorage.*`           | other origins' storage                                |

The extension also enforces its own rules: a tab is only handed to a paired
daemon, browser pages (`chrome://…`) are not controlled, incognito tabs are
allowed only if the user allowed the extension in incognito, and **Stop** in the
popup cuts every agent off.

## Downloads and uploads

- **Download** first asks the page (its cookies, same origin). When that is
  refused — a file on another site — it falls back to a plain request from the
  engine, which carries **no cookies**. Public files download from any site; a
  file behind a login does not, and needs its own tab.
- **Upload** stages a copy of the file the AI app named in the engine's temp
  directory, hands the path to the page's file input, and keeps the copy for ten
  minutes so the page can read it when its form is submitted.
- Files are read and written only under the MCP roots. Downloads are capped at
  25 MB per file.

## Prompt injection, practically

The tool's instructions already tell the model to treat page content as data and
to stop when a page asks the user to sign in. The engine backs that with three
properties: the command script never comes from the page, `get`/`find` return
text only, and nothing in the engine can send a file anywhere (the only network
call it makes is the anonymous download the user asked for).

## Optional controls (implemented, off by default)

- **Domain allowlist**: `BROWSER_MCP_ALLOW_DOMAINS=example.com,.test` makes `goto`
  and `tab new` refuse any other host, subdomains included. A refused navigation
  fails with `[refused]`.
- **Confirmation mode**: `BROWSER_MCP_CONFIRM=1` turns the commands that change a
  page — `click`, `fill`, `type`, `press`, `select`, `check`, `uncheck`,
  `upload`, `drag`, `dialog` — into a `confirm: would run …` line, leaving the
  page untouched, so a supervisor can approve one step at a time.
- **Credential redaction**: `request` prints `Authorization`, `Cookie`,
  `Set-Cookie`, `Proxy-Authorization` and `X-Api-Key` as `[redacted]`, so a
  token is never copied into a transcript.
- **Bounded failures**: every step has a deadline (30 s by default), so a page
  that blocks a CDP call cannot hang a call for minutes.

Not yet: a per-script budget, an exportable trace, and visual assertions; see
[roadmap.md](roadmap.md).

## Reporting

A vulnerability in the extension, the protocol or the engine: open a private
security advisory on the repository rather than a public issue.
