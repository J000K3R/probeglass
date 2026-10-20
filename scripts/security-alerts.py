#!/usr/bin/env python3
"""List the open code- and secret-scanning alerts for the ProbeGlass repositories.

Usage:
    GH_TOKEN=... scripts/security-alerts.py [OWNER/REPO ...]

With no arguments it checks the three repositories this project publishes. It
exits 1 when any alert is open, so the weekly workflow fails rather than letting
a finding sit unread, and 2 when no token is set.

The token is a fine-grained personal access token with "Code scanning alerts:
read" and "Secret scanning alerts: read" (Metadata is added automatically). The
GITHUB_TOKEN that Actions provides can read code scanning with
`security-events: read`, but not secret scanning; that half is then reported as
unavailable instead of being silently skipped.

Only the alert's type, location and page are printed — never the secret itself.
"""

import json
import os
import sys
import urllib.error
import urllib.request

OWNER = os.environ.get("SECURITY_ALERTS_OWNER", "J000K3R")
DEFAULT_REPOS = [
    f"{OWNER}/probeglass",
    f"{OWNER}/probeglass-docs",
    f"{OWNER}/probeglass-website",
]
KINDS = (
    ("code-scanning", "code scanning"),
    ("secret-scanning", "secret scanning"),
)


def fetch(url, token):
    """Return (data, status, body). status is None for a 200."""
    request = urllib.request.Request(url, headers={
        "Authorization": f"Bearer {token}",
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    })
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response), None, ""
    except urllib.error.HTTPError as err:
        body = err.read().decode("utf-8", "replace")
        return None, err.code, body
    except urllib.error.URLError as err:
        return None, 0, str(err)


def describe(kind, alert):
    if kind == "code-scanning":
        rule = alert["rule"]
        location = alert["most_recent_instance"]["location"]
        severity = rule.get("security_severity_level") or rule.get("severity") or "?"
        print(f"  #{alert['number']} [{severity}] {rule['id']} "
              f"{location['path']}:{location.get('start_line')}")
        print(f"    {alert['html_url']}")
    else:
        name = alert.get("secret_type_display_name") or alert.get("secret_type")
        print(f"  #{alert['number']} {name} (validity: {alert.get('validity')})")
        print(f"    {alert['html_url']}")


def main():
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or ""
    if not token:
        print("security-alerts: set GH_TOKEN to a token that can read security alerts",
              file=sys.stderr)
        return 2

    repos = sys.argv[1:] or DEFAULT_REPOS
    open_alerts = 0
    unreadable = []

    for repo in repos:
        for path, label in KINDS:
            url = f"https://api.github.com/repos/{repo}/{path}/alerts?state=open&per_page=100"
            alerts, status, body = fetch(url, token)

            if status == 404:
                # A repository that has no analysis, or has secret scanning
                # disabled, is not a failure: there is nothing to read.
                print(f"{repo}: {label} is not enabled")
                continue
            if status in (401, 403):
                print(f"{repo}: {label} could not be read ({status})")
                unreadable.append(f"{repo} {label}")
                continue
            if status is not None:
                print(f"{repo}: {label} failed ({status}): {body[:200]}")
                return 1
            if not alerts:
                print(f"{repo}: no open {label} alerts")
                continue

            open_alerts += len(alerts)
            print(f"{repo}: {len(alerts)} open {label} alert(s)")
            for alert in alerts:
                describe(path, alert)

    if open_alerts:
        print(f"\n{open_alerts} open alert(s). Read them before releasing.")
        return 1
    if unreadable:
        print("\nNo open alerts were returned, but these could not be checked: "
              + ", ".join(unreadable))
        print("Configure SECURITY_ALERTS_TOKEN with secret-scanning read access "
              "for the full check.")
        return 0
    print("\nNo open security alerts.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
