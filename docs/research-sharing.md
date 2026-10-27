# Research: sharing layouts and annotations through git

Collected 2026-10-28 by a research worker from each tool's source and docs; the resulting design is in docs/SHARING.md. Claims it could not verify are listed at the end.


### 1. Prior art: how these tools persist and share layouts and annotations

**Azimutt**
- **Format:** one JSON project file, validated with zod. The file says the schema "MUST stay in sync" with a backend Elixir schema ([legacyProject.ts](https://raw.githubusercontent.com/azimuttapp/azimutt/main/libs/models/src/legacy/legacyProject.ts)).
- **Keys:**
  - Table ids are strings like `public.users`, or `.users` when there is no schema ([legacyDatabase.ts](https://raw.githubusercontent.com/azimuttapp/azimutt/main/libs/models/src/legacy/legacyDatabase.ts)).
  - `layouts` is an object keyed by layout name.
  - Each layout holds a `tables` array of `{id, position, size, color, columns, selected?, collapsed?}`, plus `groups`, `memos`, `createdAt` and `updatedAt`.
  - Annotations sit at project level, outside the layouts: `metadata` is keyed by table id and holds `notes`, `tags`, `color`, and `columns` keyed by column path.
  - `ignoredRelations` is keyed by table.
- **Git friendliness:** it does not appear to be designed for git. Layouts carry timestamps and per-user UI state (`selected`, canvas `zoom`). The `LegacyProjectJson` export does drop `id`, `storage`, `visibility`, `createdAt` and `updatedAt` at top level.
- **Versioning:** a top-level `version: 1 | 2`.
- **Schema changes:** a source refresh shows added, updated and removed entities, relations and types, and lets you accept or decline the update ([docs/sources](https://azimutt.app/docs/sources)). What happens to layout entries for removed tables is unverified.

**ChartDB**
- **Format:** an exported JSON file is the whole diagram: tables with `id`, `name`, `schema`, `x`, `y`, `fields`, `color`, `createdAt`, `expanded`, and more ([db-table.ts](https://raw.githubusercontent.com/chartdb/chartdb/main/src/lib/domain/db-table.ts)). Notes and areas are arrays ([diagram.ts](https://raw.githubusercontent.com/chartdb/chartdb/main/src/lib/domain/diagram.ts)).
- **Export:** rewrites every id to a running counter ("0", "1", ...) and prints with `JSON.stringify(…, null, 2)`.
- **Import:** parses with zod, sets `createdAt`/`updatedAt` to now, and generates fresh ids ([export-import-utils.ts](https://raw.githubusercontent.com/chartdb/chartdb/main/src/lib/export-import-utils.ts)).
- **Git friendliness:** poor, because the running ids shift whenever a table is inserted earlier in the array.
- **Versioning:** no version field in the schema, based on the file.
- **Merging:** import restores a separate diagram and does not merge ([docs](https://docs.chartdb.io/docs/import/json)).

**DrawDB**
- **Format:** a JSON Schema in [schemas.js](https://raw.githubusercontent.com/drawdb-io/drawdb/main/src/data/schemas.js).
  - Tables are an array with an integer or string `id`, `x`, `y`, `collapsed`, `hidden` and `color`.
  - Relationships reference `startTableId` and `startFieldId`, not names.
  - `notes` and `subjectAreas` have integer ids.
- **Versioning:** none. The schema sets `additionalProperties: true`.
- **Export filename:** includes `new Date().toISOString()` ([Modal.jsx](https://raw.githubusercontent.com/drawdb-io/drawdb/main/src/components/EditorHeader/Modal/Modal.jsx)).
- **Git friendliness:** not designed for git.

**dbdiagram.io / DBML**
- **Annotations are text in DBML** ([syntax](https://dbml.dbdiagram.io/syntax/enrichment-visualization/)):
  - `Note` on project, table, column, index and TableGroup.
  - Sticky notes as named `Note` blocks.
  - `TableGroup` with `color` and `note`.
  - `DiagramView`, which lists tables, notes, groups and schemas by name.
  - Custom properties.
  - `Metadata Table users {…}` blocks that attach properties by name. When the same key is set in more than one place, the higher-priority source wins: inline settings are lowest, then imported files, then the current file, and later blocks win.
- **Positions are not in DBML.** A community thread confirms that exported SQL/DBML loses them ([thread 3152](https://community.dbdiagram.io/t/how-to-keep-my-table-position-when-import-another-computer/3152)). A 2023 proposal would add layout to DBML ([thread 2025](https://community.dbdiagram.io/t/proposal-ability-to-include-layout-info-in-dbml/2025)). Whether it shipped is unverified.
- **dbdocs layouts are stored server-side.** On a new build, positions are kept for tables that still exist in the DBML, and new tables get no position ([dbdocs docs](https://docs.dbdiagram.io/dbdocs/features/relationship-diagrams/)).
- **Keys:** all keyed by name.
- **Renames:** rename handling is unverified.

**DBeaver**
- **Format:** custom diagrams are `.erd` XML files in the project. Each `<entity>` has a sequential `id`, `name`, `fq-name`, `order`, `color-bg`, `x`, `y` and `<path name=…/>` children. Users do edit them by hand in VS Code ([issue #2605](https://github.com/dbeaver/dbeaver/issues/2605)).
- **Schema diagrams:** "Keep layout" saves the layout locally only ([docs](https://dbeaver.com/docs/dbeaver/Database-Structure-Diagrams/)).
- **Unverified:** the loader source location, the missing-table behaviour, and any format version. I could not fetch DiagramLoader.java.

**DataGrip**
- **Format:** diagrams save as `.uml`, which JetBrains calls an internal format. It can only be opened in the project where it was created. For sharing, JetBrains suggests PNG or the export formats: graphml, drawio, dot "with Positions", Mermaid and PlantUML ([docs](https://www.jetbrains.com/help/datagrip/creating-diagrams.html)).
- **Git friendliness:** not designed for git sharing.

**Recommendations**
- Key every entry by a natural key, never an internal id. Use `schema.table`, the FK content key, and the glossary term. The id-based formats (ChartDB, DrawDB, DBeaver) are the ones that diff badly.
- Keep annotations separate from layouts, as Azimutt does with `metadata` and dbdiagram does with `Metadata` blocks. A note then survives a layout being deleted.
- Keep entries for tables missing from the live DB, but flag them as orphaned in the UI instead of dropping them. dbdocs keeps positions while the table exists. None of the tools above was verified to detect renames, so offer a manual remap at import time.

### 2. Merge-friendly JSON conventions

**What sources say**
- **Jupyter nbformat** writes with `sort_keys=True`, `indent=1` and `ensure_ascii=False`. It also splits multi-line strings into arrays of lines (`split_lines`) and strips transient values before writing ([nbjson.py](https://raw.githubusercontent.com/jupyter/nbformat/main/nbformat/v4/nbjson.py)).
- **npm package-lock** is meant to be committed so changes show up in readable diffs. npm infers line endings and indentation from package.json ([docs](https://docs.npmjs.com/cli/v10/configuring-npm/package-lock-json)). On conflict, the advice is to fix package.json and re-run `npm install`, which regenerates a merged lock, or to install `npm-merge-driver` ([v6 docs](https://docs.npmjs.com/cli/v6/configuring-npm/package-locks)).
- **Terraform state** is the counter-example. HashiCorp says not to keep it in version control, not to edit it directly, and to expect the format to change ([docs](https://developer.hashicorp.com/terraform/language/state)).
- **RFC 8785 (JCS)** sorts properties and forbids whitespace between tokens. Numbers are serialized ECMAScript-style, so they may be rounded ([RFC 8785](https://www.rfc-editor.org/rfc/rfc8785.html)). Because the output has no whitespace, it is suited to hashing and signing and is unusable for line diffs.
- **Go `encoding/json`** sorts map keys when encoding ([pkg.go.dev](https://pkg.go.dev/encoding/json)).
- **git `merge=union`** keeps lines from both sides in random order, and the docs warn against it unless you understand it. Custom merge drivers are defined in `.git/config`, so each user has to install them ([gitattributes](https://git-scm.com/docs/gitattributes)).
- **VS Code `settings.json`** is JSONC, which allows comments ([docs](https://code.visualstudio.com/docs/languages/json)). Strict JSON parsers reject comments, so do not adopt JSONC for an interchange file.

**Recommendations for dbscope**
- **Key order:** use Go maps, or sort explicitly, for every keyed collection. Put one entry per line with 2-space indent, LF line endings, UTF-8, and a trailing newline.
- **Arrays:** where an array is unavoidable, such as the schema set used as a layout key or a list of collapsed tables, sort it and dedupe it.
- **Floats:** round positions to integers or snap them to the grid before writing, so a 0.0001 px drag does not create a diff.
- **Excluded data:** write no export timestamps, no app build version, no per-user state (zoom, viewport, selection), and no connection details. Write only the format version.
- **Multi-line text:** store glossary definitions and SQL snippets as arrays of lines, as nbformat does, so diffs stay line-sized.
- **File split:** consider separate files for layouts, FK decisions and glossary to reduce the conflict surface. This is my suggestion, not taken from a source.
- **Conflicts:** don't recommend `merge=union`. Document the npm-style recovery: take either side, re-import, re-export. An optional `dbscope merge-driver` is possible later, but each user must configure it.

### 3. Import semantics: replace vs merge

**What tools do**
- **Excalidraw:**
  - Loading a file warns that it "will replace your existing content" and offers a backup first.
  - Drag-and-drop replaces by default, and holding Shift keeps existing content.
  - A toast offers undo after the replace ([en.json](https://raw.githubusercontent.com/excalidraw/excalidraw/master/packages/excalidraw/locales/en.json)).
- **DrawDB:**
  - JSON import fully replaces the diagram.
  - SQL import has an `overwrite` checkbox. Unchecked, it appends without deduplicating.
  - Both paths clear the undo and redo stacks ([Modal.jsx](https://raw.githubusercontent.com/drawdb-io/drawdb/main/src/components/EditorHeader/Modal/Modal.jsx)). That is an anti-pattern to avoid.
- **ChartDB:** import always creates a new diagram with fresh ids. There is no merge.
- **Azimutt:** a source refresh previews the differences (added, updated, removed) and lets the user confirm or decline ([docs](https://azimutt.app/docs/sources)).
- **dbdocs:** on a rebuild, it keeps positions for surviving tables and leaves new tables unpositioned.
- **DBML `Metadata`:** precedence is explicit and deterministic, and later sources win.

**Recommendations**
- Make **merge the default**, keyed per entry, and offer **replace** as an explicit choice.
- Before applying, show a preview grouped by kind (layout, FK decision, glossary), with counts and an expandable list:
  - **Added:** new from the file.
  - **Changed:** the file differs from local.
  - **Unchanged.**
  - **Local-only:** kept on merge, removed on replace.
  - **Orphaned:** the key is not in the current DB.
- Handle conflicts per entry, with "take file" or "keep mine" and bulk actions. Don't silently resolve an FK decision that is accepted on one side and rejected on the other.
- Snapshot the local SQLite state before applying and offer one-step undo.

### 4. Format versioning and `$schema`

**What sources say**
- **npm** uses an integer `lockfileVersion` (1, 2, 3) and documents the compatibility of each. It upgrades old files automatically and tries to read versions it was not designed for ([docs](https://docs.npmjs.com/cli/v10/configuring-npm/package-lock-json)).
- **Excalidraw** writes `type`, `version` and `source` ([json.ts](https://raw.githubusercontent.com/excalidraw/excalidraw/master/packages/excalidraw/data/json.ts)). On load, `restore.ts` migrates legacy props field by field, for example `strokeSharpness` to `roundness` and binding schema v1 to v2. It also spreads the original element so unknown properties survive "for forward-compatibility" ([restore.ts](https://raw.githubusercontent.com/excalidraw/excalidraw/master/packages/excalidraw/data/restore.ts)).
- **Azimutt** uses `version: 1 | 2`, as described in section 1.
- **VS Code** treats `$schema` in an instance file as VS Code-specific, not part of the JSON Schema spec, and warns that it changes the data. It supports workspace mappings through `json.schemas` with `fileMatch` and a relative `url` such as `./myschema.json`. `json.schemaDownload.enable` controls fetching http(s) schemas, and a status-bar warning appears when a schema cannot be downloaded ([docs](https://code.visualstudio.com/docs/languages/json)).

**Recommendations**
- Use top-level `"kind": "dbscope-shared-state"` and `"formatVersion": 1` as integers.
- On read, run chained `vN→vN+1` upgrade functions in Go. Refuse files newer than the binary supports, with a clear message.
- Always write the latest version, and only on explicit export. Never rewrite the committed file during import.
- Accept and ignore a `$schema` key on import.
- For offline editor validation, either:
  - commit the schema file next to the export and point `$schema` at a relative path, or
  - document a `.vscode/settings.json` `json.schemas` entry with a relative `url` (documented above).

  An https `$schema` URL will make VS Code try to fetch it.

### Unverified
- DBeaver `.erd`: format version, the loader source path, and how missing or renamed tables are handled.
- DataGrip `.uml` internal structure and keying.
- What Azimutt does with layout entries whose table no longer exists. Azimutt's version 1 to 2 migration code.
- Whether dbdiagram shipped layout-in-DBML (the proposal in holistics/dbml#53).
- Whether any of the listed tools detects table renames.
- Whether VS Code resolves a relative path in an instance-level `$schema` property. Only `json.schemas` relative `url` is documented in the page I read.
- Go `encoding/json` float formatting details and struct field order. I only verified that map keys are sorted.
- JetBrains IDE handling of instance `$schema`.

