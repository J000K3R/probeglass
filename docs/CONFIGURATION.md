# Configuration reference

Weatherglass reads configuration from four sources. Later sources override
earlier ones, key by key:

1. **Built-in defaults**
2. **A YAML file** — the `-config` flag, else `$WEATHERGLASS_CONFIG`, else
   `<data_dir>/weatherglass.yaml` when that file exists. A missing file is fine
   unless you named it explicitly, in which case startup fails rather than
   silently running with defaults.
3. **`WEATHERGLASS_*` environment variables**
4. **Command-line flags**

Everything the dashboard can change — sensors, alert channels, theme — lives in
the database and is edited over the API. The configuration file is for the
process itself, plus an optional declarative form of sensors and channels that is
re-applied on every start.

## Process settings

| Key | Environment | Default | Notes |
|---|---|---|---|
| `listen` | `WEATHERGLASS_LISTEN` | `:8080` | host:port to serve on |
| `data_dir` | `WEATHERGLASS_DATA_DIR` | `./data` in a checkout, `/data` in the image | holds the database and the default config file |
| `db_path` | `WEATHERGLASS_DB` | `<data_dir>/weatherglass.db` | full path to the SQLite file |
| `base_url` | `WEATHERGLASS_BASE_URL` | empty | public URL, used in links inside alerts; a trailing slash is trimmed |
| `log_level` | `WEATHERGLASS_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `log_format` | `WEATHERGLASS_LOG_FORMAT` | `text` | `text` or `json` |
| `trust_proxy` | `WEATHERGLASS_TRUST_PROXY` | `false` | honour `X-Forwarded-For` / `X-Forwarded-Proto` |
| — | `WEATHERGLASS_ADMIN_PASSWORD` | unset | creates an administrator account named `admin` on a database with no accounts; ignored afterwards |
| — | `WEATHERGLASS_CONFIG` | unset | path to the YAML file |

Truthy values for boolean environment variables are `1`, `true`, `yes`, and `on`
(case-insensitive); everything else is false.

```yaml
listen: ":8080"
data_dir: /data
base_url: https://weatherglass.lan
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
| `user_agent` | `Weatherglass/<version>` | overrides the User-Agent |
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
Weatherglass container. The target is a container name or id. The probe reads the
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

## Map tiles

Also stored in the database and set in the UI, but **installation-wide** rather
than per account, and administrator-only:

| Setting | Notes |
|---|---|
| `map.tile_url` | default `https://tile.openstreetmap.org/{z}/{x}/{y}.png`; must be `http(s)` and must contain `{z}`, `{x}` and `{y}` (`{s}` is optional) |
| `map.attribution` | default `© OpenStreetMap contributors`; shown in the corner of the map |

The browser fetches the tiles directly, so the tile server sees the viewer's
address and roughly where they are looking; the daemon never proxies them. Point
it at a tile server inside your own network for anything more than a homelab. The
origin is added to the dashboard's content security policy automatically, which is
what lets the tiles load at all — see `SECURITY.md`.

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

To check a file without starting anything, run `weatherglass doctor --config
PATH`. The `config` line says whether the file parses and the `config sensors` line
says whether every sensor and channel in it would be accepted — the same validation
as above, applied by the same code. It also reports a declaration that has not
reached the database yet, which is the state between editing the file and
restarting the daemon.
