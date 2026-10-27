#!/bin/sh
# Downloads Pagila at a pinned commit, verifies its checksums and loads it
# into the database named by the DSN, which must exist and be empty.
#
#   ./setup.sh 'postgres://user:pass@127.0.0.1:5432/pagila'
#
# Needs curl, psql (PostgreSQL 14 or newer client and server) and
# shasum or sha256sum. Files are cached in $PAGILA_CACHE (default
# ./.cache next to this script); the DSN is never printed.
set -eu

COMMIT=23f7fe7a1e9f5772f85fc14f530583ffa258c6d2
BASE=https://raw.githubusercontent.com/devrimgunduz/pagila/$COMMIT
SCHEMA_SHA256=18de72ae8669a0099b47be6d54b60ca5334114f38db65bf21ae82b0fc752d1ad
DATA_SHA256=2ca58493e45c01e0978ddf9a9e4796ef3acd3290ac0093b91a6d4e7b6b80d625

if [ $# -ne 1 ]; then
	echo "usage: $0 DSN" >&2
	exit 2
fi
DSN=$1
HERE=$(cd "$(dirname "$0")" && pwd)
CACHE=${PAGILA_CACHE:-$HERE/.cache}
mkdir -p "$CACHE"

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

fetch() { # name expected-sha256
	f=$CACHE/$1
	if [ ! -f "$f" ] || [ "$(sha256 "$f")" != "$2" ]; then
		echo "downloading $1 (pagila ${COMMIT%"${COMMIT#???????}"})"
		curl -fsSL --retry 3 -o "$f.tmp" "$BASE/$1"
		mv "$f.tmp" "$f"
	fi
	got=$(sha256 "$f")
	if [ "$got" != "$2" ]; then
		echo "checksum mismatch for $1: got $got, want $2" >&2
		rm -f "$f"
		exit 1
	fi
}

fetch pagila-schema.sql "$SCHEMA_SHA256"
fetch pagila-data.sql "$DATA_SHA256"

tables=$(psql "$DSN" -XAtqc "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm')")
if [ "$tables" != "0" ]; then
	echo "the target database already has $tables relations in schema public; use an empty database" >&2
	exit 1
fi

# The dump sets transaction_timeout, which only exists from PostgreSQL 17;
# that one line is dropped so 14-16 load it too. The files themselves are
# checked above, unmodified.
load() {
	sed '/^SET transaction_timeout = /d' "$CACHE/$1" | psql "$DSN" -Xq -v ON_ERROR_STOP=1 -f - >/dev/null
}
echo "loading schema"
load pagila-schema.sql
echo "loading data"
load pagila-data.sql
psql "$DSN" -Xq -v ON_ERROR_STOP=1 -c "ANALYZE" >/dev/null
echo "pagila loaded"
