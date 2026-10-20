#!/bin/sh
#
# security-alerts.sh — list open code- and secret-scanning alerts.
#
# Usage:  GH_TOKEN=... scripts/security-alerts.sh [OWNER/REPO ...]
#
# With no arguments it checks the three repositories this project publishes. It
# exits 1 when any alert is open, so the weekly workflow fails rather than letting
# a finding sit unread, and 2 when no token is set.
#
# The token is a fine-grained personal access token with "Code scanning alerts:
# read" and "Secret scanning alerts: read" (Metadata is added automatically). The
# GITHUB_TOKEN that Actions provides can read code scanning with
# `security-events: read`, but not secret scanning; that half is then reported as
# unavailable instead of being quietly skipped.
#
# Only the alert's type, location and page are printed — never the secret itself.
#
set -eu

owner="${SECURITY_ALERTS_OWNER:-J000K3R}"
token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
if [ -z "$token" ]; then
  echo "security-alerts: set GH_TOKEN to a token that can read security alerts" >&2
  exit 2
fi

repos="${*:-$owner/probeglass $owner/probeglass-docs $owner/probeglass-website}"
open_alerts=0
unreadable=""

# check fetches one alert list and prints it. The status code is kept apart from
# the body: 404 is a feature that is not enabled, 401/403 is a token that cannot
# see that half, and anything else is a failed check.
check() {
  repo=$1 path=$2 label=$3
  body=$(mktemp)
  status=$(curl -sS -o "$body" -w '%{http_code}' \
    -H "Authorization: Bearer $token" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com/repos/$repo/$path/alerts?state=open&per_page=100") || status=0

  case "$status" in
    200)
      count=$(jq 'length' "$body")
      case "$count" in
        ''|*[!0-9]*)
          echo "$repo: $label returned an unreadable list"
          rm -f "$body"
          exit 1
          ;;
      esac
      if [ "$count" = "0" ]; then
        echo "$repo: no open $label alerts"
      else
        open_alerts=$((open_alerts + count))
        echo "$repo: $count open $label alert(s)"
        if [ "$path" = "code-scanning" ]; then
          jq -r '.[] | "  #\(.number) [\(.rule.security_severity_level // .rule.severity // "?")] " +
            "\(.rule.id) \(.most_recent_instance.location.path):\(.most_recent_instance.location.start_line)\n" +
            "    \(.html_url)"' "$body"
        else
          jq -r '.[] | "  #\(.number) \(.secret_type_display_name // .secret_type) " +
            "(validity: \(.validity))\n    \(.html_url)"' "$body"
        fi
      fi
      ;;
    404)
      echo "$repo: $label is not enabled"
      ;;
    401|403)
      echo "$repo: $label could not be read ($status)"
      unreadable="$unreadable $repo: $label"
      ;;
    *)
      echo "$repo: $label failed ($status): $(head -c 200 "$body")"
      rm -f "$body"
      exit 1
      ;;
  esac
  rm -f "$body"
}

for repo in $repos; do
  check "$repo" code-scanning "code scanning"
  check "$repo" secret-scanning "secret scanning"
done

if [ "$open_alerts" -gt 0 ]; then
  echo ""
  echo "$open_alerts open alert(s). Read them before releasing."
  exit 1
fi
if [ -n "$unreadable" ]; then
  echo ""
  echo "No open alerts were returned, but these could not be checked:$unreadable"
  echo "Configure SECURITY_ALERTS_TOKEN with secret-scanning read access for the full check."
  exit 0
fi

echo ""
echo "No open security alerts."
