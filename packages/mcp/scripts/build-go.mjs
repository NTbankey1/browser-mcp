import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const packageRoot = path.resolve(import.meta.dirname, "..");
const repoRoot = path.resolve(packageRoot, "../..");
const packageJson = JSON.parse(
  fs.readFileSync(path.join(packageRoot, "package.json"), "utf8"),
);
const binary = process.platform === "win32" ? "browsermcpd.exe" : "browsermcpd";
const output = path.join(packageRoot, "dist", binary);

fs.mkdirSync(path.dirname(output), { recursive: true });
execFileSync(
  "go",
  [
    "build",
    "-trimpath",
    "-ldflags",
    `-s -w -X main.version=${packageJson.version}`,
    "-o",
    output,
    "./cmd/browsermcpd",
  ],
  { cwd: path.join(repoRoot, "go"), stdio: "inherit" },
);
console.log(`Built ${output}`);
