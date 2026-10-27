// Runs test/vscode/suite.cjs inside a real VS Code with a throwaway profile, against
// a copy of the fixture. Set VSCODE_PATH to use an installed VS Code instead of a download.
import { cpSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { runTests } from "@vscode/test-electron";

const here = path.dirname(fileURLToPath(import.meta.url));
const ext = path.resolve(here, "..");
const tmp = mkdtempSync(path.join(tmpdir(), "dbscope-vscode-"));
const workspace = path.join(tmp, "workspace");
cpSync(path.join(here, "fixture"), workspace, { recursive: true });
mkdirSync(path.join(workspace, ".vscode"));
writeFileSync(
  path.join(workspace, ".vscode/settings.json"),
  JSON.stringify({ "dbscope.path": path.resolve(ext, "../bin/dbscope"), "dbscope.schemaDump": "db/schema.sql" }),
);
delete process.env.DBSCOPE_DSN;

await runTests({
  vscodeExecutablePath: process.env.VSCODE_PATH,
  extensionDevelopmentPath: ext,
  extensionTestsPath: path.join(here, "vscode/suite.cjs"),
  launchArgs: [
    workspace,
    "--disable-extensions",
    "--disable-workspace-trust",
    `--user-data-dir=${path.join(tmp, "user")}`,
    `--extensions-dir=${path.join(tmp, "extensions")}`,
  ],
});
