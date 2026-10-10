# Security Policy

## Supported versions

Only the latest `0.x` minor release receives security fixes. Older minor
releases are not supported.

| Version      | Supported |
| ------------ | --------- |
| latest `0.x` | Yes       |
| older `0.x`  | No        |

## Reporting a vulnerability

Please report vulnerabilities privately. Do not open a public issue, and do not
disclose the details publicly until a fix has been released.

Preferred channel: [GitHub Security Advisories](https://github.com/j000k3r/weatherglass/security/advisories/new).
A private advisory keeps the report, the discussion and the fix in one place
that only the maintainers can see until a release is out.

### What to include

- A description of the issue and its impact.
- Steps to reproduce, ideally a minimal proof of concept.
- The affected version(s) and deployment method.
- Any suggested fix or mitigation, if you have one.
- How you would like to be credited, if at all.

### Response times

This is a volunteer project, so support is best effort. We aim to acknowledge a
report within 7 days and will keep you updated as we investigate and prepare a
fix.

## Authentication and authorization

Weatherglass has no global password. Every sign-in is an account, created on the
setup screen or by an administrator, and every account carries one of three
ordered roles. A route is assigned the lowest role that may reach it, in one
table, and that table is the whole authorization policy.

- **viewer** reads everything the dashboard draws and may change nothing, apart
  from its own account: its interface preferences, its password and its second
  factor.
- **operator** is a viewer who may also act on an outage that has already
  happened — acknowledge and un-acknowledge an incident, pause or resume a
  sensor, run a probe by hand, run the diagnostics. Nothing here changes what is
  monitored or how.
- **admin** may create, change and delete sensors, sites and channels, reorder the
  dashboard, point the map at a tile server, rotate the API key, and manage the
  accounts and the example data.

Sign-in is two steps when the second factor is on: the password, then a six-digit
TOTP code (RFC 6238, HMAC-SHA1, a thirty-second step, one step of drift either
way) or one of eight one-time recovery codes. A code whose time step has already
been spent is refused rather than accepted a second time, so a code read over a
shoulder is worthless once used.

Sessions carry an identity. The cookie names the account and a token epoch, and
every authenticated request re-reads the account row, so disabling an account,
changing its role or deleting it takes effect on the next request rather than when
a thirty-day cookie happens to expire. A password change bumps only that account's
epoch: it signs that person out everywhere and nobody else anywhere.

### The API key is an administrator with no account behind it

The API key is a deliberate exception to all of the above, and it is stated here
rather than in a footnote because it is the one credential an operator has to
understand before handing the key out.

The key has full administrator rights. It is not attached to any account, so it
has no username, no role of its own, no second factor and no recovery codes, and
it is unaffected by disabling or deleting an account — including the account that
generated it. It is the same credential the Home Assistant integration and every
script use, and it has always had full access. Where the rest of the API treats a
role as an entitlement, the key simply is an administrator.

Two consequences follow, and both are intended:

- A request authenticated with the key reads the interface preferences as their
  defaults and writes to them nowhere, because there is no account to hold them.
- Anyone who holds the key can do anything the product can do. Generate it only
  for a script or an integration you control, treat it like a root password, and
  delete it in **Settings → API key** when it is no longer needed.

## The backup file carries every secret in the clear

**Settings → Backup** writes the installation's configuration, its accounts and,
if asked for it, its history into one JSON document — and that document names
every secret the installation holds. The password hash of every account, their
TOTP secrets and the hashes of their recovery codes; the API key; the community
string of every SNMP sensor; and the credentials of every alert channel: the
Telegram bot token, the Discord and Slack webhook URLs, the Matrix access token,
the ntfy token, the webhook's HMAC secret. The map's tile URL is in it too.

This is the one place in Weatherglass where a stored secret is written out
**unmasked**. Everywhere else `notify.StoredMarker` stands in for it, so that a
channel's token is never handed back to a browser that already holds the channel.
A backup that did the same would restore a channel whose token reads
`__stored__`: the file would import cleanly, the channel would appear in the list
with the right name and type, and it would fail to send anything — and nobody
would find out until the first outage, which is exactly the moment the thing is
needed. A backup with masked credentials is not a backup. The unmasking is
deliberate, it is said plainly on the panel that produces the file, and it is said
here rather than in a footnote.

What Weatherglass does about it:

- **The export is administrator-only**, like every route that can change the
  installation. A viewer and an operator cannot download the file, and cannot
  learn any of this from the API.
- **The response is a download and can be nothing else.** It is served as
  `application/json` with `Content-Disposition: attachment` and the existing
  `X-Content-Type-Options: nosniff`, so a browser saves the file instead of
  rendering it, and cannot be talked into executing it as a page or a script. The
  filename is built server-side from a constant and the date; no part of it comes
  from a request. The response carries `Cache-Control: no-store`.
- **Nothing from a request is ever written into it.** The document is assembled
  from the program's own structures, table by table, streamed out through
  `json.NewEncoder`. There is no path by which a caller supplies part of the file.
- **The contents never reach a log line.** `backup exported` and
  `backup restored` record counts per table and nothing else — the same rule the
  Telegram bot token already follows, where the URL that contains it is never
  logged.
- **A restore validates before it writes.** `POST /api/restore` also requires an
  administrator, and it checks the format marker and the format and schema
  versions before touching a single row. A file that is not a Weatherglass backup,
  or one written by a newer schema, is refused with a message saying so and
  changes nothing.
- **A restore takes a safety copy first.** `VACUUM INTO` writes a byte-exact copy
  of the database next to it before anything is replaced, and a restore whose
  safety copy fails does not proceed. That copy is the way back if the file turns
  out to lock the operator out.

What that does not protect you from:

- **Anyone who obtains the file.** It is exactly as sensitive as the database file
  beside the daemon, and it should be guarded the same way: not in a chat, not in a
  ticket, not in a cloud drive you do not control, and not in a git repository. If
  a backup leaks, every credential in it is compromised at once — rotate the API
  key, rotate each channel's token, and have everyone change their password and
  re-enrol their second factor.
- **A restore that undoes a sign-out.** A session carries an account's token
  epoch, and the epoch travels in the backup with everything else. Restoring a
  *newer* backup into an *older* database therefore re-validates sessions that were
  signed out in between. "Sign out everywhere" is not a defence against a file
  somebody else holds.
- **A restore onto a different installation.** Sensors, sites and channels are
  matched by name, not by identifier, so a backup from another instance will
  overwrite the things whose names line up and add the rest. Restore into a fresh
  data directory unless you mean to do that.

## The Docker socket is root on the host

Docker container sensors read the Docker Engine API over `/var/run/docker.sock`.
That socket is not a monitoring interface with a monitoring-shaped permission
model: it is the daemon's control channel, and anything that can talk to it can
ask the daemon to start a container that mounts the host's root filesystem. In
practice, mounting the socket into a container is the same as giving that
container root on the host. This is stated here, in full, for the same reason the
API key is: it is a decision an operator has to make knowingly, not discover.

What Weatherglass itself does with the socket:

- The prober issues exactly one request per probe,
  `GET /containers/{target}/json`, and reads `Name`, `State` and `RestartCount`
  from the answer. It never starts, stops, restarts, removes, execs into or
  changes a container, and it never touches an image, a network or a volume.
- It speaks HTTP over the socket with the Go standard library. There is no Docker
  client library in the dependency tree that could reach further by accident, and
  no code path that chooses the HTTP method.
- If the socket is missing or unreadable, the probe fails and says so. It does
  not fall back to anything.

What that does **not** protect you from:

- A read-only bind mount (`:ro`) does not restrict what can be sent over a socket
  you can already open. It is a readability hint, not a wall.
- The socket widens what a compromise of the dashboard is worth. Whoever gets an
  administrator session or the API key can already change the configuration;
  with the socket mounted, whatever code runs inside the container can also reach
  the host.
- The prober's good behaviour is a property of this program, not of the socket.
  Any other process in the same namespace is not bound by it.

If you want container sensors without that trade, run a socket proxy that allows
only the read endpoints — the compose file has the plain mount, commented out, so
that turning it on is a deliberate edit. If you do not watch containers, leave it
off; nothing else in Weatherglass needs it.

## SNMP v2c is unauthenticated and the community is clear text

SNMP sensors speak v2c, and v2c has no authentication. The community string —
`public` by default, and a shared secret wherever it is set to anything else — is
sent **in the clear in every packet**, and the answer is neither signed nor
encrypted. Anyone on the path can read the community, and anyone who has it can
ask the same agent anything its own access list allows. Version 3 would fix this;
it is deliberately not implemented, so a monitor that needs authenticated SNMP is
not this one.

Weatherglass does what it can with the part that is in front of it:

- The community is treated as a secret. It is masked in the API like every other
  credential, so it does not appear in a dashboard tooltip, a stored result or a
  log line, and the probe's message never includes it.
- Nothing is written to the device. The prober issues one `GET` of one OID and
  never a `SET`, and it does not walk the MIB.
- The prober reads only what the operator asked for: one OID, whose value is
  checked against a substring or a numeric range.

What that does not change: **use SNMP sensors on a trusted network only.** A
homelab LAN is the intended place; a routed, shared or wireless network is not.
If the community matters, treat it as compromised the moment it crosses one.

## The map fetches its tiles from a third party

The dashboard can draw your sites on a real map, and the tiles that make up that
map are fetched **by the browser**, directly from a tile server. Weatherglass does
not proxy them: the daemon never contacts a tile server and no tile passes through
it. The default is OpenStreetMap, which needs no account and no key.

That is a real disclosure, and it is written down here rather than buried in a
setting's help text: whoever operates the tile server sees the **IP address** of
whoever is looking at the dashboard, their **browser's user agent**, and the
**approximate part of the map** in view. On a dashboard whose sites are what it
draws, that is close to "where this homelab is". On a phone away from home, it is a
third party learning that the dashboard exists.

What Weatherglass does about it:

- **The tile origin is named in the content security policy**, and nothing else is.
  `setSecurityHeaders` appends the origin of the configured tile URL to `img-src`;
  the origin is resolved once at start-up and again when the URL is saved, and
  cached on the server rather than read from the database per request. A remote
  tile server is therefore reachable deliberately, not by accident, and a
  same-origin one needs no change to the policy at all.
- **The URL is validated before it is stored.** It must be `http` or `https` and
  must contain `{z}`, `{x}` and `{y}`. A URL that does not is refused, not stored
  and quietly broken.
- **The setting is administrator-only and installation-wide**, unlike the
  appearance preferences, which every account owns for itself. A viewer cannot
  point the map at a server of their choosing, and cannot use the map to make
  somebody else's browser talk to a host of their choosing.
- **No tile ever reaches the server.** Nothing about the map is written to the
  database, a log line or a stored result.

What that does not protect you from:

- **The tile server's own logs.** It sees what any web server sees. Nothing here
  changes that, and a homelab on a home network is the case the default is chosen
  for.
- **Anything else on that origin.** The policy allows the origin you name, so if
  you point the map at a host that serves other things, the browser may fetch
  those too. That is the same trust you extend to any server you type into a
  setting.

If the map's subject is somebody else's infrastructure, or the dashboard is
reachable from outside your network, run a tile server inside your own network and
put its address in **Settings → Map**. Nothing about the map needs the public
internet.

## Scope

In scope:

- Authentication bypass.
- Privilege escalation between roles, or a way past the second factor.
- API-key or session leakage.
- SSRF via check targets.
- Stored XSS in the dashboard.
- Alert-channel credential exposure.

Out of scope:

- Misconfiguration of your own homelab, including exposed ports, weak or reused
  credentials, and permissive firewall rules.
- Vulnerabilities in third-party services or software that Weatherglass merely
  monitors or talks to.

## Deployment note

Weatherglass is intended to run on a trusted private network. Do not expose it
directly to the public internet. If remote access is required, place it behind a
reverse proxy and use TLS and strong credentials.
