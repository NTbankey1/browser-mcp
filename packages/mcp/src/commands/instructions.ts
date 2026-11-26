/** The MCP server's `instructions`: when to reach for the browser and the workflow. The thin process sends it in `initialize`, before it knows how many browsers are connected, so it says nothing about several: agents learn of them from `tab list` (which names each tab's browser while several are connected) and `help` (which then lists `browser`) */
export const INSTRUCTIONS = `Browser MCP controls the user's browser through the \`browser\` tool.
Use it when the task needs the user's real browser: a site they are signed in to, a page that needs its JavaScript, or a UI to click, type in or read. For a static public page, the app's own web fetch is cheaper. If \`browser list\` says no browser is connected, ask the user to open their browser (the extension connects when it runs), then run the command again.

1. Act in batches: put every step you can predict in one call (targets like getByLabel('Email') work without a snapshot).
2. Look with \`snapshot -i\` or \`find "<text>"\`; read a whole page with \`get text --max all\`.
3. Re-snapshot after navigating: refs from an older page fail.`;
