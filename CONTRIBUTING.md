# Contributing to dbscope

Thanks for helping. Bug reports, wrong migration verdicts, docs fixes and code
are all welcome.

## Before you start

- **Bugs and wrong verdicts**: open an issue with the bug template. A migration
  rated safe that locked or failed on a real server is the most valuable report
  there is, so include the server version and the statement.
- **Features and new engines**: open an issue first and describe the problem it
  solves. dbscope adds a mode to an engine only when it can be built on that
  engine's own semantics and tested against a real server (see
  [docs/ENGINES.md](docs/ENGINES.md)), so it is worth agreeing on the shape
  before writing code.
- **Small fixes** (typos, docs, an obvious bug): send a pull request directly.
- **Security issues**: do not open a public issue; see [SECURITY.md](SECURITY.md).

## Setting up

You need Go 1.26+, Node 22.12+ and, for the database tests, Docker.

```bash
git clone https://github.com/ShreedharJoshi31/dbscope.git
cd dbscope
make build          # web UI, then the Go binary in bin/dbscope
./bin/dbscope ui
```

For UI work, run the agent and the Vite dev server side by side:

```bash
./bin/dbscope ui --dev-origin http://localhost:5173   # terminal 1
make dev                                              # terminal 2, hot reload on :5173
```

## The flow

1. Fork the repository and create a branch from `main`.
2. Make the change, with tests (see below).
3. Run the checks:

   ```bash
   make test   # go vet + Go tests, web lint + typecheck + unit tests
   make e2e    # Playwright browser tests, when you touched the UI
   ```

4. Open a pull request against `main` and fill in the template. CI runs the web
   checks and Playwright, and the README's build command on each OS. It does
   not run the Go tests, so run `make test` before you open the pull request.
5. A maintainer reviews it. Address review comments with new commits.

## Tests

- **Every rule is tested against a real server.** A migration or query rule
  needs a test that runs the statement on the database it claims to cover and
  checks dbscope's verdict against what the server did (look for
  `TestMigratePredictions` in each engine's package). A rule that says "safe"
  when the server locks or fails is worse than no rule.
- **Database tests use Docker.** Each engine's package starts its own
  containers through testcontainers. Without Docker they are skipped, and
  PostgreSQL tests can use `DBSCOPE_TEST_DSN` instead. Packages run two at a
  time (`-p 2`) because several engines are JVMs.
- **Web code** uses Vitest and Testing Library for units, Playwright for flows.
  Test behaviour through the UI, not mocks of the code under test.
- **Screenshot baselines** for CI come from the Playwright Linux image. If your
  change alters a screenshot, regenerate the Linux baselines inside it:

  ```bash
  docker run --rm --platform linux/amd64 -v "$PWD/web:/src:ro" -v "$PWD/web/e2e/__screenshots__:/out" \
    mcr.microsoft.com/playwright:v1.64.1-noble bash -c \
    'cp -r /src /w && rm -rf /w/node_modules && cd /w && npx -y pnpm@12.15.0 install --frozen-lockfile &&
     npx -y pnpm@12.15.0 exec playwright test e2e/screenshots.spec.ts --project chromium --update-snapshots &&
     cp -r e2e/__screenshots__/chromium-linux /out/'
  ```

## Style

- Go: `gofmt`, `go vet` clean, no cgo. Errors are returned, not logged and
  swallowed.
- Web: `pnpm lint` (Biome) and `pnpm typecheck` clean. UI primitives are shadcn
  on Base UI.
- Comments explain why, not what.
- User-facing text is plain and specific: say what will happen to which table,
  not "this may be risky".
- Commit messages are one short sentence in lower case, describing the change:
  `check that down migrations undo their up migrations`.

## License

dbscope is licensed under the [Apache License 2.0](LICENSE). By submitting a
contribution you agree that it is licensed under the same terms (section 5 of
the license). Only submit work you have the right to license this way.
