const assert = require("node:assert/strict");
const path = require("node:path");
const vscode = require("vscode");

/** Resolves with a file's dbscope diagnostics once there are any. */
function diagnosticsFor(uri, timeout = 20000) {
  return new Promise((resolve, reject) => {
    const own = () => vscode.languages.getDiagnostics(uri).filter((d) => d.source === "dbscope");
    if (own().length) return resolve(own());
    const timer = setTimeout(() => {
      sub.dispose();
      reject(new Error(`no dbscope diagnostics on ${uri.fsPath}`));
    }, timeout);
    const sub = vscode.languages.onDidChangeDiagnostics(() => {
      if (!own().length) return;
      clearTimeout(timer);
      sub.dispose();
      resolve(own());
    });
  });
}

exports.run = async () => {
  const root = vscode.workspace.workspaceFolders[0].uri.fsPath;
  const risky = vscode.Uri.file(path.join(root, "db/migrations/0002_risky.sql"));
  const safe = vscode.Uri.file(path.join(root, "db/migrations/0001_add_status.sql"));

  // Opening a migration checks its folder, so the risky file is flagged without being opened.
  await vscode.window.showTextDocument(safe);
  const found = await diagnosticsFor(risky);
  assert.deepEqual(
    found.map((d) => [d.range.start.line, d.code, d.severity]),
    [
      [0, "DBS-TYPE-REWRITE", vscode.DiagnosticSeverity.Error],
      [0, "DBS-TYPE-NARROWING", vscode.DiagnosticSeverity.Warning],
      [0, "DBS-NO-LOCK-TIMEOUT", vscode.DiagnosticSeverity.Error],
      [1, "DBS-INDEX-NOT-CONCURRENT", vscode.DiagnosticSeverity.Error],
      [1, "DBS-NO-LOCK-TIMEOUT", vscode.DiagnosticSeverity.Error],
    ],
  );
  assert.equal(vscode.languages.getDiagnostics(safe).length, 0);

  // Fixing the file and saving clears what the fix resolved.
  const editor = await vscode.window.showTextDocument(risky);
  await editor.edit((e) =>
    e.replace(
      new vscode.Range(0, 0, editor.document.lineCount, 0),
      "SET lock_timeout = '2s';\nCREATE INDEX CONCURRENTLY orders_user_idx ON orders (user_id);\n",
    ),
  );
  const cleared = new Promise((resolve) => {
    const sub = vscode.languages.onDidChangeDiagnostics(() => {
      if (vscode.languages.getDiagnostics(risky).length === 0) {
        sub.dispose();
        resolve();
      }
    });
  });
  await editor.document.save();
  await Promise.race([cleared, new Promise((_, r) => setTimeout(() => r(new Error("diagnostics not cleared after save")), 20000))]);

  const commands = await vscode.commands.getCommands(true);
  assert.ok(commands.includes("dbscope.checkMigrations"));
  assert.ok(commands.includes("dbscope.openWorkbench"));
};
