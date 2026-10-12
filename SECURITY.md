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

Preferred channel: [GitHub Security Advisories](https://github.com/j000k3r/probeglass/security/advisories/new).
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

ProbeGlass has no global password. Every sign-in is an account, created on the
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

This is the one place in ProbeGlass where a stored secret is written out
**unmasked**. Everywhere else `notify.StoredMarker` stands in for it, so that a
channel's token is never handed back to a browser that already holds the channel.
A backup that did the same would restore a channel whose token reads
`__stored__`: the file would import cleanly, the channel would appear in the list
with the right name and type, and it would fail to send anything — and nobody
would find out until the first outage, which is exactly the moment the thing is
needed. A backup with masked credentials is not a backup. The unmasking is
deliberate, it is said plainly on the panel that produces the file, and it is said
here rather than in a footnote.

What ProbeGlass does about it:

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
  versions before touching a single row. A file that is not a ProbeGlass backup,
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

What ProbeGlass itself does with the socket:

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
off; nothing else in ProbeGlass needs it.

## SNMP v2c is unauthenticated and the community is clear text

SNMP sensors speak v2c, and v2c has no authentication. The community string —
`public` by default, and a shared secret wherever it is set to anything else — is
sent **in the clear in every packet**, and the answer is neither signed nor
encrypted. Anyone on the path can read the community, and anyone who has it can
ask the same agent anything its own access list allows. Version 3 would fix this;
it is deliberately not implemented, so a monitor that needs authenticated SNMP is
not this one.

ProbeGlass does what it can with the part that is in front of it:

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

## The map draws itself, and fetches nothing by default

The dashboard draws your sites on a map. The map's world comes out of the binary —
coastlines, the lakes and rivers, the country, district and province borders, and the
names of the countries, provinces, cities and towns, nearly
all of it from
public-domain datasets decoded in the browser — and **out of the box nothing about it
leaves the machine**. No request is made to any server to draw it, so there is nobody to
disclose anything to: not your IP address, not your browser, and not the part of the
world you are looking at. The daemon never contacts a map server either, by default or
otherwise.

**Two files on that map are not public domain, and this is where that is written down.**
The district borders are Austria's, and they come from Statistik Austria under
**CC BY 4.0**; the town names come from GeoNames under the same licence. CC BY is not a
licence that asks for nothing: it asks to be named when the
work is shown. Ten of the twelve embedded datasets are Natural Earth's and are public
domain; these two are the exception, they are the only obligation this project passes on
to whoever runs it, and one of them is also the only source URL here that changes from
year to year, because the register in it carries a date. The wording to use is in each
file's own header
(`internal/web/assets/districts.js` and `internal/web/assets/towns.js`) and in the
dashboard documentation.

An administrator can still enter a tile URL in **Settings → Look → Map**, and that is a
real decision with a real cost, so it is written down here rather than buried in a
setting's help text. Once one is entered the tiles are fetched **by the browser**, directly from
that server — ProbeGlass does not proxy them and no tile passes through the daemon.
Whoever operates that server then sees the **IP address** of whoever is looking at the
dashboard, their **browser's user agent**, and the **approximate part of the map** in
view. On a dashboard whose sites are what it draws, that is close to "where this homelab
is". On a phone away from home, it is a third party learning that the dashboard exists.

**This is why the default changed.** OpenStreetMap used to be it, and their tile usage
policy no longer permits use by third-party applications, so a dashboard that still
pointed there would be shown a block notice instead of a coastline. The field is left
in place for anybody running a tile server of their own, which is the answer that has
actually improved.

What ProbeGlass does about it:

- **Empty means nothing is fetched, and the content security policy says so.** With no
  tile URL configured, `img-src` stays at `'self' data:` and the policy names no third
  party at all. An address that is entered is appended to `img-src`; the origin is
  resolved at start-up and again when the URL is saved, and cached on the server rather
  than read from the database per request. A remote tile server is therefore reachable
  deliberately, not by accident, and a same-origin one needs no change to the policy.
- **The URL is validated before it is stored.** It must be `http` or `https` and must
  contain `{z}`, `{x}` and `{y}`. A URL that does not is refused, not stored and quietly
  broken. The empty string is not validated, because there is nothing to fetch.
- **The setting is administrator-only and installation-wide**, unlike the appearance
  preferences, which every account owns for itself. A viewer cannot point the map at a
  server of their choosing, and cannot use the map to make somebody else's browser talk
  to a host of their choosing. The borders switch rides in the same payload and is the
  same kind of setting, but it fetches nothing and discloses nothing — it only decides
  whether six files already in the binary are sent to the browser.
- **No data of any kind leaves with the map.** The borders, the water and the names are
  decoded
  from the binary exactly like the coastlines: a query about a place name cannot be
  made, because nothing is asked anywhere. The whole reason the layers are embedded and
  not fetched from a geocoding service is that a map which asks "where is Niedersachsen"
  is a map that has told somebody where you are looking.
- **No tile ever reaches the server.** Nothing about the map is written to the database,
  a log line or a stored result.
- **The retired default is removed, not honoured.** An installation that once saved the
  map settings has OpenStreetMap's address written into its settings table as though
  somebody had chosen it. Migration 5 and every subsequent start delete it, so an
  upgrade does not carry on asking a server that has asked this kind of application to
  stop — and a pre-migration backup restored later does not put it back.

What that does not protect you from:

- **The tile server's own logs**, once you have named one. It sees what any web server
  sees.
- **Anything else on that origin.** The policy allows the origin you name, so if you
  point the map at a host that serves other things, the browser may fetch those too.
  That is the same trust you extend to any server you type into a setting.
- **Coarse data.** The bundled coastlines, country borders, lakes and rivers are 1:10m,
  the province borders, their names, the city names and the town names are
  1:10m as well, the country names 1:110m — and all of it is simplified to about 550
  metres, which is
  under a pixel to about zoom 8 and visible by about zoom 12. In a city it is a straight
  line between distant points, and the districts are the only layer simplified harder
  (about 200 metres) because they are only ever drawn at a zoom where 550 metres
  would show. Only the 472 largest river systems are in the world file and only the
  middle-sized ones of Europe are added to it, so a stream through a valley is not on
  this map and neither is a river in a country Natural Earth did not draw one for. That
  is
  the honest limit of a world
  map small enough to embed, and it is the other half of the reason the tile URL field
  exists. It is also the price of the property above: a dataset small enough to ship is
  a dataset coarse enough to see.

If the map's subject is somebody else's infrastructure, or the dashboard is reachable
from outside your network, run a tile server inside your own network and put its address
in **Settings → Look → Map**. Nothing about the map needs the public internet.

## The container starts as root, and leaves it before it opens anything

The image's `USER` is root and its `ENTRYPOINT` starts as root. This is a deliberate
decision with a real cost, so here it is in full rather than as a line in a Dockerfile.

**Why.** The data directory is normally a bind mount, and on the platforms ProbeGlass
is installed on — the MOS Hub, and anything with a template that mounts an appdata
folder — that folder is created by the platform, as root, before the container ever
starts. A process that is not root cannot write into it, and cannot repair it either,
because only root may change who owns a file. Every comparable image on those platforms
starts as root, takes its folder over and then drops; that is the entire reason their
installations work without a `chown` and ProbeGlass's did not. It is fixed by doing the
same thing.

**What root is used for, exactly.** Three things, in this order, before the database is
opened or a port is bound: `umask` is set to `UMASK`; the data directory is created if it
is missing; and the directory and everything inside it is handed to `PUID:PGID` with
`lchown`. Then the process becomes that user — `setgroups`, `setgid`, `setuid` — and
checks with `geteuid` that it worked; if it did not, it refuses to continue rather than
run as root. The chown uses `lchown` and never follows a symlink, so a link planted in a
folder that other things can write to cannot point it at `/etc/shadow`.

**What that costs.** For those few milliseconds the process is root. Nothing has been
read from the network, no request has arrived, and no path has come from a request — the
data directory is the one from the configuration — so the reachable surface during the
window is the startup code itself. It is small, and it is not zero, and saying otherwise
would be the kind of reassurance this file exists to avoid.

**Afterwards.** The drop is permanent: a privileged `setuid` sets the saved uid as well,
so no code running later, including ProbeGlass's own, can return to root. Go applies
`setuid` to every OS thread, so there is no thread left behind as root. `CAP_NET_RAW`,
which ICMP ping needs, is carried across the drop on purpose; if that fails the daemon
logs which capabilities were lost and keeps running, because a watchdog that refuses to
start is the worse trade.

`no-new-privileges: true`, as in `docker-compose.yml`, does not interfere: it forbids
*gaining* privileges, not giving them away.

**Setting `PUID=0`** keeps the container as root for its whole life. It is supported,
documented and logged loudly at startup, and it is not recommended: it gives up the
property that everything a sensor or a request can reach runs unprivileged.

**If the container must not start as root at all**, that is possible and stays possible:
start it with `--user 500:500` (or `user:` in compose) and hand the folder to that user
once, yourself. The daemon then logs that `PUID`, `PGID` and `UMASK` are set but cannot
take effect, because that is exactly what is happening.

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
- Vulnerabilities in third-party services or software that ProbeGlass merely
  monitors or talks to.

## Deployment note

ProbeGlass is intended to run on a trusted private network. Do not expose it
directly to the public internet. If remote access is required, place it behind a
reverse proxy and use TLS and strong credentials.
