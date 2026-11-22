#!/usr/bin/env node
/**
 * Reproducible benchmark for the Browser MCP daemon.
 *
 * Measures the built daemon (`dist/daemon.js`) with a temporary `BROWSER_MCP_HOME` and a
 * random free port: cold start, idle RSS/CPU, HTTP `/status`, client WebSocket hello, and
 * `tools/list` / `tools/call help` RPC (sequential and concurrent). It never uses ports
 * 9009–9012 and never touches `~/.browsermcp`.
 *
 * Usage:
 *   pnpm --filter @browsermcp/mcp bench            # 3 runs
 *   pnpm --filter @browsermcp/mcp bench -- --runs 1
 *   pnpm --filter @browsermcp/mcp bench -- --json bench-results.json
 *
 * Results depend on the machine and Chromium build; compare like for like.
 */
import { spawn } from "node:child_process";
import fs from "node:fs";
import { createRequire } from "node:module";
import net from "node:net";
import os from "node:os";
import path from "node:path";

const require = createRequire(import.meta.url);
const WebSocket = require("ws");

const packageRoot = path.resolve(import.meta.dirname, "..");
const repoRoot = path.resolve(packageRoot, "../..");
const daemonJs = path.join(packageRoot, "dist/daemon.js");

const option = (name, fallback) => {
  const index = process.argv.indexOf(name);
  return index === -1 ? fallback : process.argv[index + 1];
};
const runs = Number(option("--runs", "3"));
const jsonPath = option("--json", null);

const round = (value, digits = 2) =>
  typeof value === "number" && Number.isFinite(value)
    ? Number(value.toFixed(digits))
    : null;
const mean = (values) =>
  values.reduce((total, value) => total + value, 0) / values.length;
const median = (values) => {
  const sorted = [...values]
    .filter((value) => value !== null)
    .sort((a, b) => a - b);
  if (!sorted.length) return null;
  return sorted[Math.floor(sorted.length / 2)];
};
const stats = (values) => {
  const sorted = [...values]
    .filter((value) => value !== null)
    .sort((a, b) => a - b);
  if (!sorted.length) return null;
  const quantile = (p) =>
    sorted[Math.min(sorted.length - 1, Math.floor(p * sorted.length))];
  return {
    n: sorted.length,
    avg: round(mean(sorted)),
    p50: round(quantile(0.5)),
    p95: round(quantile(0.95)),
    max: round(sorted[sorted.length - 1]),
  };
};
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

function rssMB(pid) {
  try {
    const status = fs.readFileSync(`/proc/${pid}/status`, "utf8");
    return Number(status.match(/VmRSS:\s+(\d+)/)?.[1] ?? NaN) / 1024;
  } catch {
    return null;
  }
}

function cpuSec(pid) {
  try {
    const fields = fs.readFileSync(`/proc/${pid}/stat`, "utf8").split(" ");
    return (Number(fields[13]) + Number(fields[14])) / 100;
  } catch {
    return null;
  }
}

async function waitForStatus(url, deadline) {
  for (;;) {
    const response = await fetch(url).catch(() => null);
    if (response?.ok) return response.json();
    if (performance.now() > deadline) return null;
    await wait(10);
  }
}

function onceExit(child) {
  return new Promise((resolve) => child.once("exit", resolve));
}

async function runOnce(index) {
  const port = await freePort();
  const home = fs.mkdtempSync(path.join(os.tmpdir(), "browsermcp-bench-"));
  const statusUrl = `http://127.0.0.1:${port}/browsermcp/status`;
  const started = performance.now();
  const child = spawn(process.execPath, [daemonJs, "--port", String(port)], {
    cwd: repoRoot,
    env: { ...process.env, BROWSER_MCP_HOME: home },
    stdio: ["ignore", "ignore", "pipe"],
  });
  let stderr = "";
  child.stderr.on("data", (chunk) => (stderr += chunk));
  const exited = onceExit(child);

  try {
    const status = await waitForStatus(statusUrl, started + 15_000);
    if (!status) {
      throw new Error(
        `daemon ${index} did not start${stderr ? `: ${stderr.trim().split("\n").slice(-3).join(" | ")}` : ""}`,
      );
    }
    const startupMs = performance.now() - started;
    const ready = {
      rssMB: rssMB(child.pid),
      cpuSec: cpuSec(child.pid),
    };

    await wait(10_000);
    const idle = {
      rssMB: rssMB(child.pid),
      cpuSec: cpuSec(child.pid),
    };

    const httpTimes = [];
    for (let i = 0; i < 500; i++) {
      const begin = performance.now();
      await fetch(statusUrl);
      httpTimes.push(performance.now() - begin);
    }
    const httpConcurrentStart = performance.now();
    await Promise.all(Array.from({ length: 50 }, () => fetch(statusUrl)));
    const httpConcurrentMs = performance.now() - httpConcurrentStart;

    const info = JSON.parse(
      fs.readFileSync(path.join(home, `daemon-${port}.json`), "utf8"),
    );
    const ws = new WebSocket(
      `ws://127.0.0.1:${port}/client?token=${encodeURIComponent(info.token)}`,
      { perMessageDeflate: false },
    );
    const wsStart = performance.now();
    await new Promise((resolve, reject) => {
      ws.once("open", resolve);
      ws.once("error", reject);
    });
    const wsOpenMs = performance.now() - wsStart;

    const pending = new Map();
    let nextId = 1;
    ws.on("message", (data) => {
      let message;
      try {
        message = JSON.parse(data.toString());
      } catch {
        return;
      }
      const resolve = pending.get(message.id);
      if (resolve) {
        pending.delete(message.id);
        resolve(message);
      }
    });
    const rpc = (method, params) =>
      new Promise((resolve, reject) => {
        const id = nextId++;
        const begin = performance.now();
        pending.set(id, (message) =>
          resolve({ ...message, ms: performance.now() - begin }),
        );
        ws.send(JSON.stringify({ id, method, params }));
        setTimeout(() => {
          if (pending.delete(id)) reject(new Error(`timeout: ${method}`));
        }, 10_000);
      });

    const welcomeStart = performance.now();
    ws.send(
      JSON.stringify({
        type: "hello",
        protocol: 1,
        version: "0.0.0-bench",
        client: { name: "bench", title: "Benchmark", version: "1" },
        capabilities: [],
        pid: process.pid,
      }),
    );
    await new Promise((resolve) => {
      const onMessage = (data) => {
        if (JSON.parse(data.toString()).type === "welcome") {
          ws.off("message", onMessage);
          resolve();
        }
      };
      ws.on("message", onMessage);
    });
    const welcomeMs = performance.now() - welcomeStart;

    const listTimes = [];
    for (let i = 0; i < 200; i++) {
      listTimes.push((await rpc("tools/list", {})).ms);
    }
    const helpTimes = [];
    for (let i = 0; i < 100; i++) {
      helpTimes.push(
        (
          await rpc("tools/call", {
            name: "browser",
            arguments: { commands: "help" },
          })
        ).ms,
      );
    }
    const rpcConcurrentStart = performance.now();
    await Promise.all(Array.from({ length: 50 }, () => rpc("tools/list", {})));
    const rpcConcurrentMs = performance.now() - rpcConcurrentStart;

    ws.close();
    return {
      startupMs: round(startupMs),
      rssAtReadyMB: round(ready.rssMB, 1),
      rssAfter10sMB: round(idle.rssMB, 1),
      cpuAtReadySec: round(ready.cpuSec, 3),
      cpuAfter10sSec: round(idle.cpuSec, 3),
      http: {
        status: stats(httpTimes),
        concurrent50Ms: round(httpConcurrentMs, 1),
      },
      ws: { openMs: round(wsOpenMs), welcomeMs: round(welcomeMs) },
      rpc: {
        toolsList: stats(listTimes),
        toolsCallHelp: stats(helpTimes),
        toolsListConcurrent50Ms: round(rpcConcurrentMs, 1),
      },
    };
  } finally {
    if (child.exitCode === null) child.kill("SIGTERM");
    await Promise.race([exited, wait(5_000)]);
    if (child.exitCode === null) child.kill("SIGKILL");
    fs.rmSync(home, { recursive: true, force: true });
  }
}

const version = JSON.parse(
  fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"),
).version;
let commit = null;
try {
  commit = require("node:child_process")
    .execSync("git rev-parse --short HEAD", { cwd: repoRoot, encoding: "utf8" })
    .trim();
} catch {
  // Not a git checkout: fine
}

const results = [];
for (let i = 0; i < runs; i++) {
  process.stderr.write(`run ${i + 1}/${runs}...\n`);
  results.push(await runOnce(i));
}

const summary = {
  startupMs: median(results.map((run) => run.startupMs)),
  rssAtReadyMB: median(results.map((run) => run.rssAtReadyMB)),
  rssAfter10sMB: median(results.map((run) => run.rssAfter10sMB)),
  cpuAfter10sSec: median(results.map((run) => run.cpuAfter10sSec)),
  httpP50Ms: median(results.map((run) => run.http.status?.p50)),
  httpP95Ms: median(results.map((run) => run.http.status?.p95)),
  wsOpenMs: median(results.map((run) => run.ws.openMs)),
  wsWelcomeMs: median(results.map((run) => run.ws.welcomeMs)),
  toolsListP50Ms: median(results.map((run) => run.rpc.toolsList?.p50)),
  toolsListP95Ms: median(results.map((run) => run.rpc.toolsList?.p95)),
  toolsCallHelpP50Ms: median(results.map((run) => run.rpc.toolsCallHelp?.p50)),
  toolsCallHelpP95Ms: median(results.map((run) => run.rpc.toolsCallHelp?.p95)),
  toolsListConcurrent50Ms: median(
    results.map((run) => run.rpc.toolsListConcurrent50Ms),
  ),
};

const output = {
  generatedAt: new Date().toISOString(),
  daemon: { version, commit },
  node: process.version,
  platform: `${process.platform} ${process.arch}`,
  runs,
  summary,
  results,
};

const text = JSON.stringify(output, null, 2);
process.stdout.write(`${text}\n`);
if (jsonPath) {
  fs.writeFileSync(jsonPath, `${text}\n`);
}
