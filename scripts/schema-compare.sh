#!/usr/bin/env bash
# Compare two pg_dump outputs, ignoring what carries no meaning.
#
# Two of the three ways these dumps differ say nothing about the database:
#
#   - column order, because pg_dump prints columns in ordinal position and ent puts
#     its mixin's columns first. Nothing reads a column by position.
#   - foreign-key constraint names, because ent names them its own way and offers no
#     hook to override it. The columns and the ON DELETE action are what matter, and
#     those are still compared.
#
# So every line is normalised and then sorted, which makes the comparison a set
# comparison. Anything left is a real difference: a column, a type, a default, an
# index, a check.
#
# No awk: the first version of this used asort, which BSD awk does not have, and the
# script printed "schemas match" after awk had died on line forty. A comparison that
# can pass on truncated input is worse than no comparison.
set -euo pipefail

normalise() {
    sed -E \
        -e 's/^[[:space:]]+//' \
        -e 's/,$//' \
        -e 's/CONSTRAINT "?[a-z0-9_]+"? (FOREIGN KEY|CHECK)/CONSTRAINT <name> \1/' \
        -e 's/ADD CONSTRAINT [a-z0-9_]+ (FOREIGN KEY|CHECK)/ADD CONSTRAINT <name> \1/' \
        "$1" |
        grep -vE '^(--|$|SET |SELECT pg_catalog)' |
        LC_ALL=C sort
}

left=$(mktemp)
right=$(mktemp)
trap 'rm -f "$left" "$right"' EXIT

normalise "$1" > "$left"
normalise "$2" > "$right"

# Non-empty, or the whole thing passes on a failed read.
for file in "$left" "$right"; do
    if [ ! -s "$file" ]; then
        echo "normalising produced nothing; the comparison would pass for the wrong reason" >&2
        exit 2
    fi
done

if diff -u "$left" "$right"; then
    echo "schemas match once column order and constraint names are set aside"
else
    echo
    echo "the differences above are real: a column, a type, a default, an index or a check"
    exit 1
fi
