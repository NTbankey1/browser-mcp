import { execFileSync } from "node:child_process";
import path from "node:path";

const packageRoot = path.resolve(import.meta.dirname, "..");
const repoRoot = path.resolve(packageRoot, "../..");

execFileSync("go", ["test", "-race", "./..."], {
  cwd: path.join(repoRoot, "go"),
  stdio: "inherit",
});
