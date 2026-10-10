# Weatherglass

**A small, self-hosted watchdog for your homelab.**

> ### ⚖️ A hobby project, vibe coded, and nobody is liable for anything
>
> **Weatherglass is not a product, and it is not written by a professional software
> developer.** It is one person in their spare time, for their own homelab, in
> dialogue with an AI coding agent (Claude Code) — written because they wanted
> something this small, and shared in case somebody else is looking for something
> similar. There is no company behind it, no support contract, no paid tier and no
> SLA, and no promise that a bug gets fixed or that a breaking change waits for a
> major version.
>
> It has had **no design review and no security audit**, and not every line has
> been read by a second human. Expect the rough edges that come with that.
>
> It is MIT licensed and provided **"as is", without warranty of any kind** — which
> covers data loss, a container that will not start, and the case this project
> exists to catch: **a service that was down for a day while the dashboard said it
> was fine. Do not make it your only line of defence.** If a missed alert would
> cost you money, keep a second, independent check on the things that matter most.
>
> How it came about, and how it is written today:
> **<https://j000k3r.github.io/weatherglass-docs/about>**

Weatherglass probes your services on a schedule, keeps their history, draws the
graphs, and shouts at you when something breaks. One binary, one SQLite file, about
three megabytes of RAM.

```bash
docker run -d \
  --name weatherglass \
  --restart unless-stopped \
  -p 8080:8080 \
  -v weatherglass-data:/data \
  --cap-add NET_RAW \
  j000k3r/weatherglass:latest
```

Open <http://localhost:8080> and create the first account in the setup screen.

`NET_RAW` is only needed for ICMP ping sensors. Without it, Weatherglass falls back
to an unprivileged UDP ping and says so in the sensor result if that is not available
either.

---

## Why this image is small

| | Weatherglass | Typical container-based monitor |
|---|---|---|
| Memory at idle | ~3 MB | 200–400 MB |
| Runtime dependencies | none (static binary) | Node.js, a JVM, or a full stack |
| Frontend build | none — embedded in the binary | npm, a bundler, a CDN |
| Data | one SQLite file you can copy | a database server, or a volume of JSON |

The dashboard is baked into the binary via `embed.FS`. The runtime stage is
`gcr.io/distroless/static-debian12:nonroot`: no shell, no package manager, uid
65532. That is why `docker exec … sh` will not work — there is no shell to give you,
and nothing in the image that needs one.

## What it does

- **Sensors** — HTTP(S), TCP (with optional TLS and send/expect), TLS certificate
  expiry, ICMP ping, DNS, Docker containers, and SNMP. Configured in the web UI or
  declared in YAML.
- **Dashboard** — five views of the same sensors: cards, a dense list, a foldable
  **tree** by site and group, a **wall** of tiles with the last forty checks as a
  heartbeat, and a **map** of your sites on real OpenStreetMap tiles. One setting
  rather than five pages, with a switcher in the top bar. Response-time charts,
  uptime bars, and an incident timeline. Updates over Server-Sent Events: no
  polling, no page reloads — a probe result redraws the one card it belongs to, so
  an open tree branch stays open and the focus stays where you put it. A **Live**
  indicator in the top bar shows whether the stream is connected and turns it off
  with one click.
- **Search** — one field in the middle of the top bar, looking through everything:
  sensors, sites, channels, incidents, the probe messages of the last days, and the
  settings themselves. Press a hit and it takes you there. Credentials are never
  searched.
- **Sites** — the places your sensors are, each with a kind (homelab or external)
  and optional coordinates. They group the dashboard and are drawn on the map.
  Optional throughout: a sensor without a site works exactly as before.
- **History** — raw samples for 7 days, hourly rollups for 90 days, daily rollups
  forever. Uptime percentages stay correct at every zoom level.
- **Incidents** — a configurable number of consecutive failures before an incident
  opens, repeat alerts while it stays down, and a recovery alert naming how long it
  was down. The list filters by state, by sensor, by period and by a word from the
  message. An open incident can be acknowledged with a note: the reminders stop,
  the probing and the history do not.
- **Alerts** — Telegram, Discord, Matrix, ntfy, Slack, and generic webhooks with an
  HMAC-SHA256 signature.
- **Simple or advanced** — choose an interface mode at setup and change it later.
  Simple hides what a casual user does not need and never disables anything.
- **Not empty on day one** — setup creates four example sensors, attached to no
  channel, so there is something to look at before you configure anything.
- **Built-in diagnostics** — `docker exec weatherglass weatherglass doctor`, or the
  same report from Settings → Diagnostics in the dashboard.
- **Auth** — first-run setup creates the first account (a username and a password),
  and every account is a viewer, an operator or an admin. Passwords are bcrypt,
  sessions carry the account and are re-checked on every request, and two-factor
  authentication (TOTP) is optional per account with eight one-time recovery codes.
  An API key serves scripts and Home Assistant.

## Tags

| Tag | Meaning |
|---|---|
| `latest` | The most recent release |
| `0.1.0`, `0.1`, `0` | A specific release, and its minor and major aliases |

Architectures: `linux/amd64`, `linux/arm64`, `linux/arm/v7`.

For a reproducible deployment, pin a specific version rather than `latest`.

## Volumes and ports

| | |
|---|---|
| `/data` | The database. Everything Weatherglass knows is in this one volume |
| `8080` | The dashboard, plain HTTP |

Backing up is copying one file, and that is still the complete backup. There is
also a **Backup and restore** panel in the dashboard: it writes the configuration —
and, if you ask for it, the incidents and the rollups of the last 7, 30 or 90 days —
as one JSON document, and takes a safety copy of the database before it restores
one. That file carries secrets in clear text, because a backup with masked
credentials restores an alert channel that cannot send anything. See the
[backup documentation](https://j000k3r.github.io/weatherglass-docs/operations/backup).

## Configuration

Mount a `weatherglass.yaml` into `/data` and it is picked up automatically, or pass
`-config`. Every setting also has a `WEATHERGLASS_*` environment variable:

```bash
docker run -d --name weatherglass \
  -p 8080:8080 -v weatherglass-data:/data \
  -e WEATHERGLASS_BASE_URL=http://weatherglass.lan:8080 \
  -e WEATHERGLASS_LOG_FORMAT=json \
  -e WEATHERGLASS_RETENTION_RAW=14d \
  -e WEATHERGLASS_ADMIN_PASSWORD='correct horse battery staple' \
  j000k3r/weatherglass:latest
```

`WEATHERGLASS_ADMIN_PASSWORD` creates an administrator account named `admin` on a
database that has no accounts at all, and is ignored afterwards — it does not touch
an existing account, so leaving it in your compose file is not a back door.

## Try it with demo data

```bash
docker volume create weatherglass-demo

docker run --rm -v weatherglass-demo:/data \
  j000k3r/weatherglass:latest seed --demo --days 7

docker run -d --name weatherglass-demo -p 8080:8080 \
  -v weatherglass-demo:/data --cap-add NET_RAW \
  j000k3r/weatherglass:latest
```

Five sensors covering every type, a week of plausible history, and six outages — so
the charts and the incident log have something real in them. Delete the volume when
you are done.

## Operations

```bash
docker logs weatherglass                  # what the daemon is doing
docker exec weatherglass weatherglass version
docker exec weatherglass weatherglass healthcheck
docker exec weatherglass weatherglass doctor
```

The image has a `HEALTHCHECK`, so `docker ps` shows the real state of the container
without needing a shell inside it.

`doctor` is the one to reach for when something is wrong. The image is distroless,
so there is no shell and no `curl` to fall back on — this is the command that says
what is broken: the configuration, the data directory, the database's integrity and
schema, the stored sensors, and whether the daemon is answering. It opens the
database read-only and never sends an alert, so it is safe on a database you are
worried about.

## Documentation

Full documentation: **<https://j000k3r.github.io/weatherglass-docs/>**

- [Installation](https://j000k3r.github.io/weatherglass-docs/getting-started/install)
- [Sensors and their options](https://j000k3r.github.io/weatherglass-docs/concepts/sensors)
- [Alerts](https://j000k3r.github.io/weatherglass-docs/concepts/alerts)
- [Configuration](https://j000k3r.github.io/weatherglass-docs/configuration/)
- [Reverse proxy](https://j000k3r.github.io/weatherglass-docs/operations/reverse-proxy)
- [Troubleshooting](https://j000k3r.github.io/weatherglass-docs/operations/troubleshooting)
- [HTTP API](https://j000k3r.github.io/weatherglass-docs/reference/api)

Source code and issue tracker:
<https://github.com/j000k3r/weatherglass>

## License

MIT — which also means, in plain words: no warranty of any kind and no liability
for anything, and the authors are not liable for any claim or damage arising from
this software. It is a hobby project by somebody who is not a professional software
developer, written in dialogue with an AI coding agent — not a product anybody stands
behind commercially. It was built for one homelab and is shared on the chance that
somebody else finds it useful. The long version, including how it came about and how
it is written today, is in
[Who makes this, and how](https://j000k3r.github.io/weatherglass-docs/about).
