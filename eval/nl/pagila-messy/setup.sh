#!/bin/sh
# Loads Pagila into an empty database (../pagila/setup.sh, pinned and
# checksummed) and turns it into the messy variant (transform.sql): schema
# px with cryptic names, no foreign keys and coded values. Schema public's
# Pagila objects are dropped, so use a database dedicated to this dataset.
#
#   ./setup.sh 'postgres://user:pass@127.0.0.1:5432/pagila_messy'
set -eu

if [ $# -ne 1 ]; then
	echo "usage: $0 DSN" >&2
	exit 2
fi
HERE=$(cd "$(dirname "$0")" && pwd)
PAGILA_CACHE=${PAGILA_CACHE:-$HERE/../pagila/.cache} "$HERE/../pagila/setup.sh" "$1"
echo "building schema px"
psql "$1" -Xq -v ON_ERROR_STOP=1 -f "$HERE/transform.sql" >/dev/null
echo "messy pagila ready (schema px)"
