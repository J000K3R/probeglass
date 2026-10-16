#!/bin/sh
#
# release-notes.sh — print one version's section of CHANGELOG.md.
#
# The release body is the changelog, not a paragraph of commit subjects. The
# changelog is written for a reader: it says what changed and why it matters, in
# an order somebody chose, grouped by what kind of change it is. `--generate-notes`
# says which commits landed, which is a different question and one nobody outside
# this repository can answer usefully.
#
# It fails loudly rather than printing nothing. A release whose notes are an empty
# body is worse than no release: it is published, it is what the tag page shows, and
# nobody notices until somebody reads it. The failure is the reminder that the
# `## [Unreleased]` heading still has to be a version number before the tag.
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

# From the version's own heading to the next one at the same level. `index(...) == 1`
# rather than a regex, because the heading text is interpolated and a version number
# contains dots, which a regex would read as metacharacters.
#
# The second stop is for the link definitions at the foot of the file — the
# `[0.2.0]: https://...` lines that make each `## [0.2.0]` heading a link. They follow
# the last version with no heading of their own, so a section that happens to be the
# last one would otherwise carry them into the release body, where they are not
# definitions of anything.
notes=$(awk -v want="## [$version]" '
  index($0, want) == 1     { found = 1; next }
  found && /^## \[/        { exit }
  found && /^\[[^]]+\]:/   { exit }
  found                    { print }
' "$changelog")

if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "$0: CHANGELOG.md has no section headed '## [$version]'." >&2
  echo "" >&2
  echo "Before tagging, rename the '## [Unreleased]' heading to '## [$version] - $(date +%Y-%m-%d)'" >&2
  echo "and add a fresh '## [Unreleased]' above it. A release is a version of the" >&2
  echo "changelog, and there is nothing to publish until the changelog says so." >&2
  exit 1
fi

printf '%s\n' "$notes"
