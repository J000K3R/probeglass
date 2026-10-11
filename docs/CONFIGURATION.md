# Configuration reference

ProbeGlass reads configuration from four sources. Later sources override
earlier ones, key by key:

1. **Built-in defaults**
2. **A YAML file** — the `-config` flag, else `$PROBEGLASS_CONFIG`, else
   `<data_dir>/probeglass.yaml` when that file exists. A missing file is fine
   unless you named it explicitly, in which case startup fails rather than
   silently running with defaults.
3. **`PROBEGLASS_*` environment variables**
4. **Command-line flags**

Everything the dashboard can change — sensors, alert channels, theme — lives in
the database and is edited over the API. The configuration file is for the
process itself, plus an optional declarative form of sensors and channels that is
re-applied on every start.

## Process settings

| Key | Environment | Default | Notes |
|---|---|---|---|
| `listen` | `PROBEGLASS_LISTEN` | `:8080` | host:port to serve on |
| `data_dir` | `PROBEGLASS_DATA_DIR` | `./data` in a checkout, `/data` in the image | holds the database and the default config file |
| `db_path` | `PROBEGLASS_DB` | `<data_dir>/probeglass.db` | full path to the SQLite file |
| `base_url` | `PROBEGLASS_BASE_URL` | empty | public URL, used in links inside alerts; a trailing slash is trimmed |
| `log_level` | `PROBEGLASS_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `log_format` | `PROBEGLASS_LOG_FORMAT` | `text` | `text` or `json` |
| `trust_proxy` | `PROBEGLASS_TRUST_PROXY` | `false` | honour `X-Forwarded-For` / `X-Forwarded-Proto` |
| — | `PROBEGLASS_ADMIN_PASSWORD` | unset | creates an administrator account named `admin` on a database with no accounts; ignored afterwards |
| — | `PROBEGLASS_CONFIG` | unset | path to the YAML file |

Truthy values for boolean environment variables are `1`, `true`, `yes`, and `on`
(case-insensitive); everything else is false.

### The container user: `PUID`, `PGID`, `UMASK`

These three do not belong to the `PROBEGLASS_*` family, and there is no line in the
configuration file for them, because they are not settings of the daemon — they are about
the user it runs as, which is decided before it starts.

| Variable | Default | Meaning |
|---|---|---|
| `PUID` | `500` | the user the daemon becomes |
| `PGID` | `500` | the group it becomes |
| `UMASK` | `022` | the mask for files it creates |

The container starts as root, takes its data directory over — a bind mount keeps the host
folder's ownership, and on a NAS the platform created that folder as root — and then
becomes `PUID:PGID` with `UMASK` set, before it opens the database. That is what makes a
fresh install work without a `chown`; [SECURITY.md](SECURITY.md) describes what the root
moment costs and why it is spent that way.

- These have **no effect when the process does not start as root** — with `--user`, with
  `user:` in compose, or on a rootless runtime. The daemon says so in the log rather than
  ignoring the values in silence.
- A binary run as yourself, outside a container, ignores them for the same reason. Run it
  as root and it does what the container does: hands the data directory to `PUID:PGID` and
  becomes that user — so `PUID=0` is how you keep root there.
- `PUID=0` and `PGID=0` mean "stay root for the whole run". Supported, logged loudly, not
  recommended.
- A value that cannot be parsed **stops the start** when the process is root, the same
  treatment `PROBEGLASS_LOG_LEVEL` gets and unlike an unparseable retention duration,
  which keeps its default. A `PUID` that was quietly ignored would leave the daemon
  running as 500 while its operator believed otherwise, which is the exact failure these
  variables exist to remove.
- **Quote `UMASK`** wherever it is written down: `UMASK: "022"` in a compose file, not
  `UMASK: 022`. YAML reads an unquoted `022` as the number 18, and 0018 is not a mask
  anybody typed — so the daemon refuses it instead of running with it.

```yaml
listen: ":8080"
data_dir: /data
base_url: https://probeglass.lan
log_level: info
log_format: text
trust_proxy: false
```

## Durations

Every duration accepts Go's syntax (`30s`, `5m`, `2h`, `1h30m`), a bare number,
which means seconds, and the day and week suffixes that retention is naturally
written in:

```yaml
retention:
  raw: 7d
  hourly: 90d
```

`90d`, `2w`, and `1.5d` are all valid; `45` means 45 seconds.

## Retention

| Resolution | Default | Notes |
|---|---|---|
| Raw samples | 7 days | every individual probe result |
| Hourly rollups | 90 days | count, success count and latency sum per hour |
| Daily rollups | forever | tiny, and they are what makes multi-year uptime possible |

```yaml
retention:
  raw: 7d          # at least 1h
  hourly: 90d      # must not be shorter than raw
```

A query picks exactly one resolution for its window — raw when the range fits
inside `raw`, hourly when it fits inside `hourly`, daily otherwise — so no probe
is ever counted twice. Rollups are built with an upsert and are safe to run
again; only complete hours and days are rolled up, so the hour you are living in
is never summarised half-finished. Raw rows older than `retention.raw` are
deleted, and the rollups they fed stay behind.

## Declarative sensors, sites and channels

Entries in the file are *managed*: they are upserted on every start and
re-applied over any later edit made in the UI, so the file always wins for the
sensors it names. Sensors created in the UI and not mentioned in the file are left
alone.

```yaml
sites:
  - name: Rack A
    kind: local            # local (default) or external
    lat: 48.1372
    lon: 11.5756

channels:
  - name: telegram
    type: telegram
    config:
      token: "123456:ABC-DEF..."
      chat_id: "-1001234567890"

sensors:
  - name: Router
    type: ping
    target: 192.168.1.1
    interval: 30s
    timeout: 5s
    retries: 3
    group: Network
    site: Rack A
    tags: [infra, lan]
    enabled: true
    reminder_every: 1h
    channels: [telegram]
```

| Sensor field | Notes |
|---|---|
| `name` | required, unique |
| `type` | `http`, `tcp`, `tls`, `ping`, `dns`, `docker` or `snmp` |
| `target` | required; URL, `host:port`, host/IP, DNS name, or container name |
| `interval` | 5s … 24h, default 60s |
| `timeout` | up to 10m, default 10s |
| `retries` | consecutive failures before an incident opens, 1 … 100, default 1 |
| `enabled` | default true |
| `group`, `tags` | display grouping and filtering only |
| `site` | name of a site; **created if it does not exist yet**, default empty |
| `reminder_every` | re-alert while an incident stays open; `0` means never |
| `options` | type-specific, see below |
| `channels` | names of alert channels, as declared above or created in the UI |

| Site field | Notes |
|---|---|
| `name` | required, unique; what a sensor's `site` refers to |
| `kind` | `local` (the homelab) or `external` (a hoster or a cloud); default `local` |
| `lat`, `lon` | optional, but both or neither; `lat` −90…90, `lon` −180…180 |

A site declared here is managed like a sensor from the file: the file owns its
name, kind and coordinates, and none of them can be changed from the dashboard —
the row is refused rather than accepted and quietly put back by the next start. A
site that is only *named* by a sensor — never declared under `sites:` — is a
different thing: it is created if missing and otherwise left alone, so
coordinates somebody gave it in the dashboard survive the next start.

A channel referenced by name that does not exist is a startup error, not a
silent no-alert. All names are validated before anything is written.

### Sensor options

**HTTP** (`type: http`)

| Option | Default | Notes |
|---|---|---|
| `method` | `GET` | `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS` |
| `expected_status` | `200-399` | `200`, `204,301`, `200-299`, or `2xx` |
| `headers` | — | map of request headers |
| `user_agent` | `ProbeGlass/<version>` | overrides the User-Agent |
| `basic_auth_user`, `basic_auth_pass` | — | HTTP basic auth |
| `bearer_token` | — | sent as `Authorization: Bearer …` |
| `body` | — | request body for POST/PUT/PATCH |
| `body_contains` | — | substring the response must contain |
| `body_not_contains` | — | substring the response must not contain |
| `body_regex` | — | regular expression the response must match |
| `expected_headers` | — | map of response headers that must contain a substring |
| `max_body_bytes` | `262144` | how much of the body to read |
| `follow_redirects` | `true` | |
| `insecure_skip_verify` | `false` | accept self-signed certificates |
| `cert_warn_days` | — | fail when the certificate expires within N days |

**TCP** (`type: tcp`)

| Option | Default | Notes |
|---|---|---|
| `tls` | `false` | perform a TLS handshake after connecting |
| `server_name` | host from the target | SNI and verification name |
| `insecure_skip_verify` | `false` | accept self-signed certificates |
| `send` | — | text written after connecting; `\r` and `\n` are decoded |
| `expect` | — | substring the response must contain |

**TLS certificate** (`type: tls`)

The target is a host or `host:port`; a bare host means port 443. HTTP and TCP
sensors read a certificate too, so this type adds no capability — it is the place
to say that the certificate is the subject. It reports how many days are left and
says which of the three certificate problems it hit: a name that does not match,
a certificate that has run out, or an authority this host does not trust.

| Option | Default | Notes |
|---|---|---|
| `server_name` | host from the target | name to send as SNI and verify against |
| `cert_warn_days` | `14` | fail when fewer days are left; `0` switches the check off, an expired certificate still fails |
| `insecure_skip_verify` | `false` | accept any certificate; the message then says the check was skipped |

**Ping** (`type: ping`)

| Option | Default | Notes |
|---|---|---|
| `count` | `3` | echo requests to send, max 20 |

**DNS** (`type: dns`)

| Option | Default | Notes |
|---|---|---|
| `record_type` | `A` | `A`, `AAAA`, `CNAME`, `MX`, `TXT`, `NS`, `SRV`, `PTR` |
| `resolver` | system | e.g. `1.1.1.1` or `192.168.1.1:53` |
| `expected` | — | comma-separated substrings the answers must contain |
| `expect_all` | `false` | require every entry of `expected`, not just one |

**Docker container** (`type: docker`)

Watches one container on the host whose Docker socket is mounted into the
ProbeGlass container. The target is a container name or id. The probe reads the
Docker Engine API over the unix socket with a single `GET /containers/{target}/json`
and nothing else: it never starts, stops or changes a container.

| Option | Default | Notes |
|---|---|---|
| `socket` | `/var/run/docker.sock` | path to the Docker socket, must be absolute |
| `expect_running` | `true` | `false` alerts when the container is up |
| `expect_health` | `healthy` | `healthy`, `starting` or `unhealthy`; empty switches the check off |
| `max_restarts` | `0` | fail above this many restarts; `0` switches the check off |

`expect_health` needs a container with a `HEALTHCHECK`, and most containers have
none — with the check left at its default, such a container is reported down with
the reason spelled out. Set `expect_health` empty for those; the container's
running state, status and restart count are still checked and reported. In the
dashboard this is the empty entry in the health dropdown, labelled "only check
that it runs".

A socket that is missing or unreadable is a probe failure rather than a
configuration error, because the socket belongs to the host and may well be absent
while the sensor is being written. The message says which of the two it is. The
restart count is reported in the sensor's message; it is not a protocol status.

Mounting the Docker socket gives the container effective root on the host. Read
the security notes before you do.

**SNMP** (`type: snmp`)

Asks one OID from a switch, router or NAS and checks what comes back. The target
is a host or `host:port`; a bare host means port 161. Deliberately a single `GET`
of a single OID: no walk, no traps, no v3. A walk enumerates a device's whole MIB
and is a different tool with a different cost; a single OID answers "how warm is
the CPU", "how long has it been up" and "how much ink is left" in one request.

| Option | Default | Notes |
|---|---|---|
| `oid` | — | the object to ask for, dotted, required — `1.3.6.1.2.1.1.3.0` is uptime |
| `community` | `public` | v2c community string; a shared secret, so it is masked in the API |
| `version` | `2c` | `2c` or `1`; v3 is not supported |
| `expect` | — | substring the value must contain |
| `min`, `max` | — | bounds a numeric value must lie within |

The sensor needs at least one of `expect`, `min` or `max`: that the agent
answered at all is what a ping already tells you, so the value has to be judged
against something you wrote down. A `min` or `max` against a value that is not a
number is reported as such rather than guessed at. Names such as `sysUpTime.0` are
refused in favour of dotted numbers, because resolving a name needs a MIB file and
a tool that quietly guesses at one quietly watches the wrong thing. Values are
reported plainly, except uptime, which is shown as `1h 11m` as well as raw.

**SNMP v2c is unauthenticated and sends the community string in clear text.** It
belongs on a trusted network, not across the internet. See the security notes.

`insecure_skip_verify` exists for homelab appliances with self-signed
certificates. It has to be asked for per sensor; there is no global switch.

## Alert channels

Channel `config` maps are type-specific. `GET /api/channel-types` returns the
same information the UI uses to build the forms, including which fields are
secret.

| Type | Fields (required in **bold**) |
|---|---|
| `telegram` | **`bot_token`**, **`chat_id`**, `thread_id`, `silent` |
| `discord` | **`webhook_url`**, `username` |
| `matrix` | **`homeserver_url`**, **`access_token`**, **`room_id`** |
| `ntfy` | **`topic`**, `server_url`, `token`, `priority` (`min`, `low`, `default`, `high`, `urgent`) |
| `slack` | **`webhook_url`**, `channel` |
| `webhook` | **`url`**, `method`, `hmac_secret` |

Every channel additionally takes `title_template` and `body_template`, both
optional Go templates rendered for every event; leaving them empty uses the
built-in wording, which differs per event (down, reminder, recovery, test).
Available variables are `.Name`, `.Type`, `.Target`, `.Group`, `.Status`,
`.Message`, `.LatencyMS`, `.StatusCode`, `.DownFor`, `.DownSince`, `.Time`, and
`.URL`. A template that fails to parse or execute falls back to the built-in
text rather than losing the alert.

## Dashboard settings

Stored per account in the database, not the file, and changed in the UI or over
the API. They belong to whoever is signed in, so two people sharing one
installation may disagree about the theme:

| Setting | Values | Default |
|---|---|---|
| `ui.theme` | `auto`, `dark`, `light` | `auto` (follows the browser) |
| `ui.accent` | `azure`, `violet`, `emerald`, `amber`, `rose`, `slate` | `azure` |
| `ui.density` | `compact`, `comfortable` | `comfortable` |
| `ui.layout` | `grid`, `list`, `tree`, `wall`, `map` | `grid` |
| `ui.language` | language code | `en` |
| `ui.mode` | `simple`, `advanced` | `advanced` — the fallback when setup was never asked; simple hides more |
| `ui.live` | `on`, `off` | `on` — `off` closes the event stream entirely; the dashboard still refreshes once a minute and when the tab regains focus |
| `ui.widgets` | comma-separated widget names | `status,uptime,latency,probes,since` — which widgets the dashboard header is made of, **in the order they appear** |
| `ui.tour_version` | integer written by the guided tour | `0` |

Only `ui.widgets` holds a list rather than one of a fixed set of words, so it is
the one key with a check of its own: every name has to be a widget the server
knows (`model.Widgets`), no name twice, at most twelve names and 256 characters. A
name that is not known is a `400`, not a value stored and never drawn. The order
is the value, which is why it is one string rather than a set.

They travel as one `ui` object: `GET /api/settings` returns it and
`PUT /api/settings` writes it, onto the signed-in account's row. The tour's
progress is part of that object but has a route of its own
(`POST /api/onboarding/tour`), because it is a number that has to be able to grow
rather than one of a fixed set of words. A request authenticated with the API key
has no account, so it reads the defaults and a write is refused.

The dashboard ships in English. Strings live in a message catalogue
(`internal/web/assets/i18n.js`), so a new language is a dictionary plus an entry
in the list of available languages.

## The map

Also stored in the database and set in the UI, but **installation-wide** rather
than per account, and administrator-only:

| Setting | Notes |
|---|---|
| `map.tile_url` | **empty by default**, which means the map fetches nothing; when set, must be `http(s)` and must contain `{z}`, `{x}` and `{y}` (`{s}` is optional) |
| `map.attribution` | empty by default; shown in the corner of the map when a tile URL is set |
| `map.borders` | `on` by default (`DefaultMapBorders`); `off` takes the borders and the names off the map and does not send the six files behind them to the browser at all. The coastlines and the water stay: they are the ground, and there is no switch for them |

**With no tile URL the map draws its own world.** Eleven data sets are embedded in
the binary and decoded in the browser — 1:10m coastlines, 1:10m country borders,
1:10m first-order administrative borders, 1:10m lakes and 1:10m large rivers each in
two copies (the world's and Natural Earth's finer one of Europe), country names,
province names, city names, and Austria's political districts from Statistik Austria
— so a dashboard on a network with no internet still shows where its sites
are, and nothing about it is disclosed to anybody. Ten of the eleven are public
domain Natural Earth data; **the districts are the exception and carry a licence**:
Statistik Austria publishes them under CC BY 4.0, which asks to be named when the
work is shown. Whoever runs a ProbeGlass map with the Austrian districts on it
should name Statistik Austria — the file's own header carries the wording, and so
does the dashboard documentation. The layers are stacked in that order, and the
stacking is the design: the coastlines first, then the water inside
them, then the country borders, then the districts, then the provinces, then any
tiles you configured,
then the names, then the pins. A lake is filled and a river is a line, which is the
whole difference between water and a border; the districts are dimmer than the
country and province lines, because they sit inside a Bundesland and are only drawn
from zoom 8.

Everything but the coastlines arrives with the zoom, in the bands Natural Earth
itself puts its data in: the country borders and their names at a world view, the
provinces and their names from about zoom 5, the finer province bands and the rivers
after that, the finer half of the water at about zoom 6, the cities between about
zoom 3 and 10 depending on their size, and Austria's districts at zoom 8.
`map.borders` is the switch, and it covers the three border files and the three name
files together — turning it off does not merely stop the drawing, it stops
the fetch, so it costs the operator 220 KB less and nothing else. The water is not
behind it: a country
whose lakes and rivers vanished when somebody turned the borders off would be a map
that drew a different world without being asked to. The data is simplified to 0.02°
— about two kilometres, honest to about zoom 6 and visibly angular by about zoom 10 —
with the districts at 0.002°, because they are only ever drawn at a zoom where two
kilometres of error would be visible. Entering a tile URL is what that field is for.

**With a tile URL the browser fetches the tiles directly**, so that server sees the
viewer's address and roughly where they are looking; the daemon never proxies them.
OpenStreetMap is no longer usable this way — their tile usage policy stopped
permitting third-party applications, and their servers answer with a block notice
rather than an error. Google Maps tiles are not usable through this field either:
Google licenses maps through the Maps JavaScript API with a key, and this project
embeds no third-party scripts and stores no keys. Point the field at a tile server
inside your own network. The origin is added to the dashboard's content security
policy automatically, which is what lets the tiles load at all — see `SECURITY.md`.

### Regenerating the map data

`internal/web/assets/world.js`, `countries.js`, `borders.js`, `lakes.js`,
`lakes_europe.js`, `rivers.js`, `rivers_europe.js`, `cities.js`, `districts.js`,
`labels.js` and `regions.js` are generated and committed. To rebuild all eleven:

```sh
ne=https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson
curl -LO $ne/ne_10m_land.geojson
curl -LO $ne/ne_10m_admin_0_boundary_lines_land.geojson
curl -LO $ne/ne_10m_admin_1_states_provinces_lines.geojson
curl -LO $ne/ne_10m_lakes.geojson
curl -LO $ne/ne_10m_lakes_europe.geojson
curl -LO $ne/ne_10m_rivers_lake_centerlines.geojson
curl -LO $ne/ne_10m_rivers_europe.geojson
curl -LO $ne/ne_10m_populated_places_simple.geojson
curl -LO $ne/ne_110m_admin_0_countries.geojson
curl -LO $ne/ne_10m_admin_1_label_points.geojson
# Statistik Austria, CC BY 4.0. The typeName carries the year of the register, so this
# is the one source URL here that has to be kept up to date.
curl -o bezirke.geojson 'https://www.statistik.at/gs-open/GEODATA/ows?service=WFS&version=1.1.0&request=GetFeature&typeName=GEODATA:STATISTIK_AUSTRIA_POLBEZ_20260101&srsName=EPSG:4326&outputFormat=application/json'

go run ./cmd/worldgen -kind land      -in ne_10m_land.geojson                            -out internal/web/assets/world.js
go run ./cmd/worldgen -kind countries -in ne_10m_admin_0_boundary_lines_land.geojson     -out internal/web/assets/countries.js
go run ./cmd/worldgen -kind borders   -in ne_10m_admin_1_states_provinces_lines.geojson  -out internal/web/assets/borders.js
go run ./cmd/worldgen -kind lakes     -in ne_10m_lakes.geojson                           -out internal/web/assets/lakes.js
go run ./cmd/worldgen -kind lakes     -in ne_10m_lakes_europe.geojson -minus ne_10m_lakes.geojson -out internal/web/assets/lakes_europe.js
go run ./cmd/worldgen -kind rivers    -in ne_10m_rivers_lake_centerlines.geojson         -out internal/web/assets/rivers.js
go run ./cmd/worldgen -kind rivers    -in ne_10m_rivers_europe.geojson -minus ne_10m_rivers_lake_centerlines.geojson -out internal/web/assets/rivers_europe.js
go run ./cmd/worldgen -kind cities    -in ne_10m_populated_places_simple.geojson         -out internal/web/assets/cities.js
go run ./cmd/worldgen -kind labels    -in ne_110m_admin_0_countries.geojson              -out internal/web/assets/labels.js
go run ./cmd/worldgen -kind regions   -in ne_10m_admin_1_label_points.geojson            -out internal/web/assets/regions.js
go run ./cmd/worldgen -kind districts -in bezirke.geojson
```

`-kind` picks the reader — closed rings, open lines, named points, or the two kinds
the water arrives in twice — so one
program produces all eleven files. `-minus <geojson>` drops the shapes whose name the
file you hand it already draws, and it is what keeps the two copies of the water from
being drawn on top of each other: the Europe supplements repeat 8 lakes and 72 rivers
of the world files, at the same zooms. It takes the world file's *source*, because the
names are in the source, and an empty name never matches — 610 of the world's lakes
have no name. `-out` is required for every kind except `land`
and `districts`, whose defaults are the files they belong to: an accidental run that
wrote provinces over
`world.js` would be a map that lost its land. `-max-rank` is the river cut, and its
default belongs to the file: 4 for the world's rivers and 11 for Europe's, whose ranks
run 10 to 12 rather than 0 to 10. `cmd/worldgen` fetches nothing
itself. It validates the shape of what it reads, simplifies (Douglas-Peucker,
0.02° by default), quantises to
thousandths of a degree and writes the coordinates delta-coded, and the header of
every file it writes records the source URL, the SHA-256 of the input, the
tolerance and the point counts — so a file in the repository can be checked against
the data it came from. `-tol` trades size for detail, and it is written into the
header so the two cannot drift apart. The rings are simplified in degrees, which
over-simplifies longitude near the poles; the Arctic is coarse either way. The
borders, the lakes and the rivers are grouped into their zoom bands by Natural
Earth's own `MIN_ZOOM` rather than by a threshold of ours, and the bands are
exported with them, so the browser shows exactly what the data says belongs at that
zoom. The districts are the one kind that reads no zoom at all: the Austrian register
has none, so the generator sets the single band 8 and the header says so instead of
pretending the file has a zoom field. Every file is written deterministically: same
input, same bytes.

## Validation

Startup fails, with the offending key named, on: an unknown log level or format,
`retention.raw` below one hour, `retention.hourly` shorter than
`retention.raw`, a sensor with no name or target, a duplicate sensor name, an
unknown sensor type, an interval below 5s or above 24h, a timeout above 10m,
`retries` outside 1–100, a channel with no name or unknown type, a channel whose
required fields are empty, a channel template that does not parse, and a sensor
referring to a channel that does not exist.

Because a bad configuration stops the container rather than starting it
half-working, `docker compose up` after a typo tells you immediately instead of
failing to alert you three weeks later.

To check a file without starting anything, run `probeglass doctor --config
PATH`. The `config` line says whether the file parses and the `config sensors` line
says whether every sensor and channel in it would be accepted — the same validation
as above, applied by the same code. It also reports a declaration that has not
reached the database yet, which is the state between editing the file and
restarting the daemon.
