#!/bin/sh
#
# release-notes.sh — the body of a GitHub release: that version's own section.
#
# Not a paragraph of commit subjects. `--generate-notes` says which commits landed,
# which is a different question, and one that nobody outside this repository can
# answer usefully. The changelog is written for a reader: what changed, in an order
# somebody chose, grouped by what kind of change it is.
#
# Only the section for the tag. A release page is about one release; the older
# sections stay on their own release pages and in CHANGELOG.md, so the notes do not
# grow with the whole history every time.
#
# The version still has to have a section of its own, and the script fails if it
# does not. That check is the point of running this before `gh release create`: a
# release whose notes are empty is worse than no release, because it is published,
# it is what the tag page shows, and nobody notices until somebody reads it. The
# failure is the reminder that `## [Unreleased]` has to become a version number
# before the tag.
#
# Usage:  scripts/release-notes.sh 0.2.0
#
set -eu

version="${1:-}"
if [ -z "$version" ]; then
  echo "usage: $0 <version>   (without the leading v — the tag is v0.2.0, this is 0.2.0)" >&2
  exit 2
fi

# The changelog sits beside the script's parent, so that this works from any
# directory the workflow happens to run in.
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
changelog="$root/CHANGELOG.md"
[ -f "$changelog" ] || { echo "$0: no CHANGELOG.md at $changelog" >&2; exit 2; }

# This version's own section: from its heading to the next one at the same level.
# `index(...) == 1` rather than a regex,
# because the heading text is interpolated and a version number contains dots, which
# a regex would read as metacharacters. The second stop is for the link definitions
# at the foot of the file — the `[0.2.0]: https://...` lines that make each heading a
# link, and which follow the last version with no heading of their own.
section=$(awk -v want="## [$version]" '
  index($0, want) == 1     { found = 1; next }
  found && /^## \[/        { exit }
  found && /^\[[^]]+\]:/   { exit }
  found                    { print }
' "$changelog")

if [ -z "$(printf '%s' "$section" | tr -d '[:space:]')" ]; then
  echo "$0: CHANGELOG.md has no section headed '## [$version]'." >&2
  echo "" >&2
  echo "Before tagging, rename the '## [Unreleased]' heading to '## [$version] - $(date +%Y-%m-%d)'" >&2
  echo "and add a fresh '## [Unreleased]' above it. A release is a version of the" >&2
  echo "changelog, and there is nothing to publish until the changelog says so." >&2
  exit 1
fi

# That section and nothing else. The check above guarantees it is not empty.
printf '%s\n' "$section"
