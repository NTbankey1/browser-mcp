#!/usr/bin/env node
import { program } from "commander";
import { fileURLToPath } from "node:url";

import type { Capability } from "@/contracts";
import { runThin } from "@/thin/server";
import { VERSION } from "@/version";

const CAPABILITIES: Capability[] = ["cdp"];

program
  .name("mcp-server-browsermcp")
  .version(VERSION)
  .option(
    "--caps <list>",
    `optional capabilities, comma-separated: ${CAPABILITIES.join(", ")}`,
  )
  .option(
    "--port <port>",
    "use the daemon on this port only (set the same port in the extension)",
  )
  .action(async (options: { caps?: string; port?: string }) => {
    const requested = (options.caps ?? "")
      .split(",")
      .map((cap) => cap.trim())
      .filter(Boolean);
    const unknown = requested.filter(
      (cap) => !CAPABILITIES.includes(cap as Capability),
    );
    if (unknown.length > 0) {
      console.error(
        `Unknown capabilities: ${unknown.join(", ")}. Available: ${CAPABILITIES.join(", ")}`,
      );
      process.exit(1);
    }
    const port = options.port ? Number.parseInt(options.port, 10) : undefined;
    if (port !== undefined && !(port > 0 && port < 65536)) {
      console.error(`Invalid port: ${options.port}`);
      process.exit(1);
    }
    // BROWSER_MCP_DAEMON=go starts the Go front door instead of this package's
    // Node daemon; BROWSER_MCP_GO_DAEMON overrides where the binary is.
    const daemonCommand =
      process.env.BROWSER_MCP_DAEMON === "go"
        ? [
            process.env.BROWSER_MCP_GO_DAEMON ??
              fileURLToPath(
                new URL(
                  process.platform === "win32"
                    ? "../bin/browsermcpd.exe"
                    : "../bin/browsermcpd",
                  import.meta.url,
                ),
              ),
          ]
        : undefined;
    await runThin({
      capabilities: requested as Capability[],
      port,
      daemonCommand,
    });
  });

program.parse(process.argv);
