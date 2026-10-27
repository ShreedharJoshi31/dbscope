# Sharing with a team

A connection's canvas layouts, decisions on inferred foreign keys and assistant
glossary live in each person's local dbscope store. To share them, export one
JSON file, commit it to the application's repository, and have teammates import
it. Nothing about the database itself is in the file: no connection details, no
data, no credentials.

```bash
dbscope share export --connection orders --out db/orders.dbscope.json
git add db/orders.dbscope.json
# a teammate, after pulling:
dbscope share import --connection orders db/orders.dbscope.json --dry-run
dbscope share import --connection orders db/orders.dbscope.json
```

In the UI, the command palette has **Export team file for …** (downloads the
file) and **Import team file into …** (previews, then imports, with Undo).

## The file

```json
{
  "kind": "dbscope-shared-state",
  "formatVersion": 1,
  "foreignKeys": {
    "public.posts(user_id)->public.users(id)": "accepted"
  },
  "glossary": {
    "active user": {
      "definition": "Logged in during the last 30 days.",
      "sql": "last_login_at > now() - interval '30 days'"
    }
  },
  "layouts": {
    "public": {
      "collapsed": [
        "public.audit_log"
      ],
      "positions": {
        "public.orders": [420, 80],
        "public.users": [0, 0]
      }
    }
  }
}
```

- Every entry is keyed by a name that means the same on every machine: the
  inferred foreign key as dbscope renders it, the glossary term, the layout key
  (the sorted list of schemas shown) and `schema.table`.
- The file is written the same way every time: keys sorted, two-space indent,
  one table position per line as whole pixels, multi-line definitions and SQL
  as arrays of lines, a trailing newline, no timestamps and no per-person view
  state. The glossary keeps the order its terms have in dbscope rather than
  being sorted, since teams order it on purpose. Moving one table is a one-line
  diff; git merges two people's moves cleanly unless the tables are next to
  each other in the sorted list.
- `formatVersion` is an integer. A file from a newer dbscope is refused with a
  message to update; unknown top-level fields (such as `$schema`) are ignored.
- Limits match the glossary editor: 500 terms, terms 1 to 100 characters
  (surrounding spaces are trimmed) and unique regardless of case, definitions
  and SQL up to 1000 characters. Inferred foreign key keys must look like
  `schema.table(col)->schema.table(col)`, positions must be `[x, y]`, and a key
  that appears twice in one object is refused. Files are at most 8 MiB.

## Importing

- **Merge** (default): every entry in the file is taken; entries you have that
  the file does not mention stay. Table positions merge per table, so your
  positions for tables the file does not place are kept.
- **Replace**: the connection ends up exactly as the file.
- The preview lists what is added, changed (with old and new values), kept or
  removed. In the UI it also flags entries for tables or inferred keys the
  database does not have, when dbscope can read the database at that moment
  (only for schemas it read). Those are imported anyway and simply go unused.
- A merge that would leave more than 500 glossary terms is refused.
- The import is one transaction: reading the current state, merging and
  writing happen together, so nothing saved meanwhile is lost. In the UI, an
  open canvas saves its pending moves first, stops saving, and redraws from the
  imported layout; **Undo import** puts back exactly what was there before,
  glossary order included.
- After a CLI import, reload any open dbscope tab, or its canvas and glossary
  editor will save what they still show over the import.

On a merge conflict in git, take either side, import it, rearrange or edit in
dbscope, and export again; the file is regenerated, so there is nothing to
merge by hand.

## Why this format

Tools that key diagrams by internal ids (ChartDB renumbers on export, DrawDB and
DBeaver `.erd` use sequential ids) produce large diffs when one table is
added; Azimutt keys by `schema.table` but stores timestamps and selection state;
DBML keeps annotations by name but has no positions. dbscope follows the
natural-key approach and the conventions of files meant for git (sorted keys,
stable rounding, no volatile fields, as Jupyter's nbformat and npm's lockfile
do). Sources: [research-sharing.md](research-sharing.md).
