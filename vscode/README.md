# dbscope for VS Code

Flags lock and rewrite risk in PostgreSQL migration files as you work on them. The
extension runs the `dbscope` CLI (`dbscope migrate check --format json`) and shows each
finding on the statement it is about, with the safer alternative in the message.

- Opening or saving a migration checks every migration in its folder, in order, so a
  statement is judged against the schema the earlier files leave behind.
- **dbscope: Check migrations in this folder** runs the same check on demand.
- **dbscope: Open workbench** starts `dbscope ui` in a terminal.

Ratings map to severities: critical and high are errors, medium is a warning, low is
information.

## Settings

| Setting | Default | |
|---|---|---|
| `dbscope.path` | `dbscope` | The binary: a name on PATH or an absolute path. |
| `dbscope.migrations` | `**/migrations/**/*.sql`, `**/migrate/**/*.sql` | Which `.sql` files are migrations. |
| `dbscope.dsnEnv` | `DBSCOPE_DSN` | The environment variable holding the database URL. The URL is never stored in settings. |
| `dbscope.schemaDump` | | A `pg_dump --schema-only` file, used when the variable is unset. |
| `dbscope.mysqlVersion` | | Check as MySQL or MariaDB (`8.4`, `mariadb-11.8`). With `DBSCOPE_MYSQL_DSN` in VS Code's environment, the live server is used, with table sizes. |
| `dbscope.sqlite` | | A SQLite database file (opened read-only). When set, migrations are checked as SQLite and the PostgreSQL settings are not used. |
| `dbscope.stats` | | A `dbscope stats export` file that adds table sizes to the dump. |
| `dbscope.checkOnSave` | `true` | Check on save. |

A base schema is required: the database URL in the named variable (as seen by the
environment VS Code was started from) or a schema dump checked into the repository.

## Build and test

```bash
npx -y pnpm@12.15.0 install
npx -y pnpm@12.15.0 test          # report mapping, and the real CLI from ../bin/dbscope
npx -y pnpm@12.15.0 package       # dist/dbscope.vsix
```

`node test/run-vscode.mjs` runs the extension inside VS Code against a copy of
`test/fixture` with a throwaway profile. It downloads VS Code unless `VSCODE_PATH` names
an installed one (on macOS, `/Applications/Visual Studio Code.app/Contents/MacOS/Electron`).
Build `../bin/dbscope` first (`make build` in the repository root).
