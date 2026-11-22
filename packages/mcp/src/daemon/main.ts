import { createCommandEngine } from "@/commands/engine";
import { runDaemon } from "@/daemon/run";
import { VERSION } from "@/version";

// Built to `dist/daemon.js`. The only importer of the command engine, so the rest of the daemon is tested with stub engines
async function main() {
  // BROWSER_MCP_PRELOAD_PLAYWRIGHT=1 trades idle memory for a faster first
  // browser command: the import happens while the daemon starts (usually
  // during the MCP client's initialize), not on the user's first action.
  if (process.env.BROWSER_MCP_PRELOAD_PLAYWRIGHT === "1") {
    await import("playwright-core").catch(() => {});
  }
  await runDaemon({
    argv: process.argv,
    version: VERSION,
    createEngine: createCommandEngine,
  });
}

void main();
