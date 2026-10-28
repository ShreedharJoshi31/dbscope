import path from "node:path";
import * as vscode from "vscode";
import { check } from "./check";
import { problems, type Severity } from "./report";

const SEVERITY: Record<Severity, vscode.DiagnosticSeverity> = {
  error: vscode.DiagnosticSeverity.Error,
  warning: vscode.DiagnosticSeverity.Warning,
  information: vscode.DiagnosticSeverity.Information,
};

export function activate(context: vscode.ExtensionContext) {
  const diagnostics = vscode.languages.createDiagnosticCollection("dbscope");
  const log = vscode.window.createOutputChannel("dbscope");
  /** The latest run per folder, so a slow earlier run cannot overwrite a newer one. */
  const runs = new Map<string, number>();
  let warnedNoBase = false;

  const config = () => vscode.workspace.getConfiguration("dbscope");
  const isMigration = (doc: vscode.TextDocument) =>
    doc.uri.scheme === "file" &&
    config()
      .get<string[]>("migrations", [])
      .some((pattern) => vscode.languages.match({ language: "sql", pattern }, doc) > 0);

  async function checkFolder(dir: string) {
    const c = config();
    const folder = vscode.workspace.getWorkspaceFolder(vscode.Uri.file(dir))?.uri.fsPath ?? dir;
    const dsnEnv = c.get<string>("dsnEnv", "DBSCOPE_DSN");
    const dsn = dsnEnv ? process.env[dsnEnv] : undefined;
    const fromFolder = (p: string) => (p && !path.isAbsolute(p) ? path.join(folder, p) : p);
    const schemaDump = fromFolder(c.get<string>("schemaDump", ""));
    const sqlite = fromFolder(c.get<string>("sqlite", ""));
    const mysqlVersion = c.get<string>("mysqlVersion", "");
    if (!dsn && !schemaDump && !sqlite && !mysqlVersion) {
      if (!warnedNoBase) {
        warnedNoBase = true;
        const open = "Open settings";
        const picked = await vscode.window.showWarningMessage(
          `dbscope needs a base schema to check migrations: set ${dsnEnv || "the dbscope.dsnEnv variable"} in the environment VS Code starts from, set dbscope.schemaDump to a pg_dump --schema-only file, or set dbscope.sqlite to a SQLite database file.`,
          open,
        );
        if (picked === open) void vscode.commands.executeCommand("workbench.action.openSettings", "dbscope");
      }
      return;
    }

    const run = (runs.get(dir) ?? 0) + 1;
    runs.set(dir, run);
    try {
      const { report, root } = await check({
        binary: c.get<string>("path", "dbscope") || "dbscope",
        dir,
        cwd: folder,
        dsn,
        schemaDump,
        sqlite,
        mysqlVersion,
        stats: fromFolder(c.get<string>("stats", "")),
      });
      if (runs.get(dir) !== run) return;
      const byFile = new Map<string, vscode.Diagnostic[]>();
      for (const p of problems(report, root)) {
        const doc = vscode.workspace.textDocuments.find((d) => d.uri.fsPath === p.file);
        const endColumn = doc && p.endLine < doc.lineCount ? doc.lineAt(p.endLine).text.length : Number.MAX_SAFE_INTEGER;
        const d = new vscode.Diagnostic(
          new vscode.Range(p.line, p.column, p.endLine, endColumn),
          p.message,
          SEVERITY[p.severity],
        );
        d.source = "dbscope";
        d.code = p.code;
        byFile.set(p.file, [...(byFile.get(p.file) ?? []), d]);
      }
      diagnostics.forEach((uri) => {
        if (uri.fsPath.startsWith(dir + path.sep) && !byFile.has(uri.fsPath)) diagnostics.delete(uri);
      });
      for (const [file, list] of byFile) diagnostics.set(vscode.Uri.file(file), list);
    } catch (err) {
      if (runs.get(dir) !== run) return;
      const message = err instanceof Error ? err.message : String(err);
      log.appendLine(`check ${dir}: ${message}`);
      void vscode.window.showErrorMessage(`dbscope could not check ${path.basename(dir)}: ${message}`);
    }
  }

  const checked = new Set<string>();
  const onDocument = (doc: vscode.TextDocument, saved: boolean) => {
    if (!isMigration(doc)) return;
    const dir = path.dirname(doc.uri.fsPath);
    if (saved ? !config().get<boolean>("checkOnSave", true) : checked.has(dir)) return;
    checked.add(dir);
    void checkFolder(dir);
  };

  context.subscriptions.push(
    diagnostics,
    log,
    vscode.workspace.onDidOpenTextDocument((doc) => onDocument(doc, false)),
    vscode.workspace.onDidSaveTextDocument((doc) => onDocument(doc, true)),
    vscode.commands.registerCommand("dbscope.checkMigrations", async (uri?: vscode.Uri) => {
      const target = uri ?? vscode.window.activeTextEditor?.document.uri;
      if (!target || target.scheme !== "file") {
        void vscode.window.showInformationMessage("Open a migration file to check its folder.");
        return;
      }
      const stat = await vscode.workspace.fs.stat(target);
      await checkFolder(stat.type === vscode.FileType.Directory ? target.fsPath : path.dirname(target.fsPath));
    }),
    vscode.commands.registerCommand("dbscope.openWorkbench", () => {
      // The binary is the terminal's process, so no shell has to quote its path.
      const shellPath = config().get<string>("path", "dbscope") || "dbscope";
      vscode.window.createTerminal({ name: "dbscope", shellPath, shellArgs: ["ui"] }).show();
    }),
  );
  for (const doc of vscode.workspace.textDocuments) onDocument(doc, false);
}

export function deactivate() {}
