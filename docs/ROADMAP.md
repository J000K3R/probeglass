# Roadmap

What exists, what is next, and what is deliberately out of scope. Dates are not
promises; the ordering is what the design already supports cheaply.

## Done — 0.1.0

- [x] HTTP, TCP (with TLS and send/expect), ICMP ping, and DNS sensors, each
  validated before it is saved.
- [x] Dashboard with live status over SSE, response-time charts, uptime bars,
  sparklines, incident timeline, search, grouping, and tags.
- [x] Three-tier history: raw for 7 days, hourly for 90 days, daily forever,
  with exactly one resolution answering any range.
- [x] Incident lifecycle with retries before an incident opens, reminder
  intervals while it stays open, recovery alerts, and per-incident
  acknowledgement with a note.
- [x] Seven alert channels: Telegram, Discord, Matrix, ntfy, Gotify, Slack, and
  signed generic webhooks, with custom message templates.
- [x] First-run setup, bcrypt passwords, hardened session cookies, API key, and
  cross-site protection for cookie-authenticated writes.
- [x] Sensors and channels declared in YAML, re-applied on every start.
- [x] Multi-arch container image (amd64, arm64, armv7), CI with race tests and
  lint, and a test suite across store, config, probe engines, notifications and
  the API. The image is the only thing that ships; the cross-compiled archives
  for darwin, windows and freebsd that 0.1.0 also published were removed in
  0.2.0 — they promised a way to run this that nothing documented.

- [x] **`probeglass doctor`** — one command that reports what is wrong with an
  installation and changes nothing: the configuration, the data directory, the
  database's size, integrity and schema, whether the stored sensors and channels
  still validate, and whether the daemon is answering. Built for the container,
  where there is no shell to fall back on. The same report is available from
  Settings → Diagnostics, through `POST /api/doctor`.
- [x] Every failing request now leaves a log line with its cause. A `5xx` is
  logged once, at `error`, with the message the client was given and the error it
  was not.
- [x] **Acknowledging an incident** — silence one outage, with a note, without
  stopping the probe or the history. Reminders stop, the first `down` alert and
  the recovery alert do not. Pausing a sensor remains a different thing.
- [x] **A simple and an advanced interface**, chosen during setup and switchable
  in Settings (`ui.mode`). Simple hides areas and folds fields; it never disables
  anything.
- [x] **Field help as `?` popovers** instead of paragraphs under the forms, with
  the example for a channel field coming from the server's own description of
  that channel type.
- [x] **A password strength indicator** at setup and at password change — local,
  dependency-free, advisory only. The server's minimum stays at eight characters.
- [x] **Four example sensors** on a fresh installation, attached to no channel and
  carrying a week of invented history — including an outage and one sensor reading
  **Flaky** — so a new dashboard is never an empty grid and never a wall of grey.
- [x] **A guided tour** of the dashboard, driven by a data list of steps so a new
  feature adds a step rather than a new tour. It zooms into one example sensor and
  names its parts, says out loud that what is on screen is invented, explains the
  sample-data button, and offers to go deeper into sensors, incidents or alerts at
  the end.
- [x] **Accounts with roles.** Setup asks for a username as well as a password,
  and that first account is an administrator. Everyone else is an operator or a
  viewer, added under Settings → Users. A role change, a disabled account or a
  deleted account takes effect on the next request.
- [x] **Two-factor authentication**, per account: a QR code drawn by the server,
  six digits, thirty seconds, and eight one-time recovery codes shown exactly once.
  A code from a time step the account has already spent is refused.
- [x] **Settings per account.** Theme, accent, density, layout, language, interface
  mode and tour progress belong to the person rather than to the installation.
- [x] **One-click removal of the sample data.** The setup and `seed --demo` mark
  what they create, so the button deletes exactly the invented sensors and can say
  how many it is about to remove. Example sensors are marked as such on their card,
  and the same panel offers to load them back — with history — after they are gone.
- [x] **TLS certificate sensors.** A host or `host:port` and nothing else, with the
  certificate as the subject rather than a side effect of an HTTP request. It
  reports the days left and separates the three certificate problems: a name that
  does not match, a certificate that has expired, and a chain this host does not
  trust.
- [x] **Docker container sensors.** One `GET /containers/{name}/json` over the
  unix socket, and nothing else — the probe never starts, stops or changes a
  container. It checks the running state, the `HEALTHCHECK` result where there is
  one, and the restart count.
- [x] **SNMP sensors.** A single `GET` of a single OID over v2c, checked against a
  substring or a numeric range. Enough for a switch, a router or a NAS, and
  deliberately not a walk, not traps and not v3.
- [x] **Sites.** A named place — the cellar rack, the VPS at a hoster — with a kind
  and optional coordinates. Sensors point at one by name, which is what lets the
  configuration file describe a place with a single line. Optional throughout: a
  sensor without a site behaves exactly as it did before sites existed.
- [x] **Five views of one dashboard.** Cards and list as before, plus a foldable
  **tree** by site and group, a **wall** of tiles carrying the last forty checks as
  a heartbeat, and a **map** of your sites. One setting, not five pages, with a
  switcher in the top bar.
- [x] **An account menu** in the top bar: who is signed in, their role, the theme,
  density and language switches, a way into Settings and **Sign out** — which used
  to be reachable only from the bottom of the Settings page.
- [x] **One search box, and it searches everything.** The field in the middle of the
  top bar finds sensors, sites, channels, incidents, the individual probe messages
  of the retention window, the settings panels and — for administrators — the
  accounts, and takes you to whichever you picked. It matches literally, so a `%` is
  a percent sign, and it never looks at a credential.
- [x] **A dashboard that moves when a probe lands.** A result redraws the one card it
  belongs to rather than the whole board, so an open tree branch stays open and the
  focus stays where you left it. A **Live** indicator in the top bar shows whether
  the event stream is connected and turns it off (`ui.live`) if you would rather it
  did not.
- [x] **A top bar that holds one line at every width.** The words go first and the
  icons stay; when even those do not fit, the four working controls — layout
  switcher, Customise, New sensor, Live — fold into a `⋮` beside them and give their
  line to the search field. Below about 620 pixels the field leaves the bar rather
  than shrink to a stub, and a magnifier in the row lays it back over the bar at full
  width — the field does not go in behind the `⋮`, because a search box that has to be
  unfolded out of a menu is a search box nobody on a phone uses. One line from 320
  pixels to 2400, with less on it.
- [x] **Settings in named tabs** with icons, one category on screen at a time rather
  than ten identical panels in a column. A settings hit in the search switches to
  the tab that owns it.
- [x] **A header made of widgets.** The strip at the top of the dashboard — the ring
  and the four figures — can be rearranged, added to and emptied: a clock, the recent
  incidents, the slowest sensors, the most outages, a small map and a row of
  shortcuts, arranged by dragging the tiles or from Settings and stored per account.
- [x] **A filter line on the incidents screen** — by state, by sensor, by period and
  by a word from the message, with a reset that puts the whole list back.
- [x] **Backup and restore from the dashboard.** The configuration, or that plus the
  incidents and the rollups of the last 7, 30 or 90 days, as one JSON document; read
  back in after a format check and a byte-exact safety copy. The file carries secrets
  in clear text, which is stated rather than hidden.
- [x] **A setup recipe in the channel editor.** Each channel type explains how to get
  its credentials — `@BotFather` for Telegram, the webhook path for Discord, the
  homeserver and access token for Matrix — under the fields, in the dialog where the
  question is actually asked.
- [x] **A second interface language.** German joins English — since the 0.1.0 tag,
  so it ships with 0.2.0. The dictionary is a file
  beside the English one — same keys, same order, same placeholders, held there by a
  test — and one entry in the two lists that say what the interface offers, so the
  next language is no code at all. The switch is in Settings and in the account menu,
  and it covers dates and numbers as well as words. Three surfaces stay English on
  purpose: the watchdog page, `doctor`, and the channel types and recipes the server
  sends as its own text.

- [x] **Maintenance windows.** Silence a sensor or a group for a defined period
  — expected downtime should not open an incident and should not count against
  the uptime figure. The check keeps running and the result is recorded; what it
  is spared is the incident, the alert and the arithmetic. Recurring weekly or a
  single stretch of time, in the zone the person creating it is in.

- [x] **Dependencies between sensors.** Mark the router as a parent of everything
  behind it, so one power cut produces one incident and one alert instead of thirty.
  A sensor behind a parent that is down is excused: no incident, no alert, and the
  checks it fails in the meantime do not count against its uptime — while the probe
  keeps running. Chains work; circles and unknown parents are refused. The same
  mechanism as a maintenance window, with a second reason.

- [x] **Symbols on sensors, places and groups.** Twenty-six drawings ship in the
  binary and any image can be uploaded to the database instead — PNG, JPEG or GIF,
  never SVG. A group's heading can wear one too, along with a colour from the same
  six the accent setting offers, which is the half of "per-group colours" that used
  to be a roadmap line of its own. Uploaded images are bytes in the one SQLite file
  rather than files beside it, so copying that file is still the whole backup, and
  an image something still points at cannot be deleted without saying so.

## Next — 0.2.x

Ordered by value per unit of work.

- [ ] **More sensor types.** SSH banner, SMTP/IMAP greeting, UDP port, an ICMP
  TTL/hop sensor, and an HTTP sensor that follows a redirect chain and reports
  what it found. Each is one file in `internal/sensor`.
- [ ] **Drag-to-arrange groups.** The order is a number in the group editor today,
  which works and is not a thing anybody wants to do twice. (Per-group colours and
  icons shipped above; the side-by-side comparison view this item used to promise is
  the **tree**, the **wall** and the **map**, and pinning a sensor to the top is the
  header built out of widgets.)
- [ ] **Escalation.** A second channel that only fires when an incident is still
  open after N minutes.
- [ ] **Import and export, for the configuration file.** The dashboard can already
  write the whole installation out as JSON and read it back in — sensors, channels,
  sites, accounts, and as much history as you ask for. What this item still wants is
  the **other direction**: writing the `probeglass.yaml` a file-driven installation
  is configured from, so the versioned part of a setup stops being something
  maintained by hand.

## Later — 0.3.x and beyond

- [ ] **A `/metrics` endpoint**, so a homelab that already runs a monitoring stack
  can scrape ProbeGlass instead of duplicating the checks it already makes.
- [ ] **Public status page.** A read-only view on its own path and its own
  token, safe to share outside the LAN, with the incidents you choose to publish.
- [ ] **SLA reports.** Monthly uptime per sensor and per group as a printable
  summary, built from the daily rollups that are already kept forever.
- [ ] **Certificate inventory.** One page listing every TLS certificate seen by
  any HTTPS or TLS-enabled TCP sensor, sorted by expiry.
- [ ] **Remote probes.** A second instance reporting into the first, for
  watching the homelab from outside.
- [ ] **Notification digest.** A daily "here is what happened" message instead
  of only event-driven alerts.
- [ ] **OIDC login** for homelabs that already run an identity provider.
- [ ] **Optional Postgres backend** for instances that outgrow one SQLite file.

## Out of scope

- **Agents and infrastructure discovery.** ProbeGlass watches what you tell it
  to watch; it is not a service-discovery or inventory tool.
- **Windows and macOS at all.** The container is the only thing that ships;
  there is no archive to unpack on a desktop.
- **A full SNMP poller.** ProbeGlass reads single OIDs over v2c, because that is
  what a switch, a router or a NAS is asked for. What stays out is the rest of it:
  a MIB walk, trap receivers, v3 with its authentication and privacy protocols, and
  the table-and-rate model that a serious poller is built around. A homelab that
  needs those wants a different program, and the generic webhook is the bridge.
- **Multi-tenancy.** One instance, one homelab. Several people can have accounts
  on it with different roles, but they all see the same sensors, the same history
  and the same incidents — ProbeGlass assumes one group of people who trust each
  other with the dashboard. Isolating two organisations from each other is a
  different product, and running two containers is the answer to wanting two.
- **Plugins loaded at runtime.** The sensor and channel interfaces are the
  extension point, and they live in the binary where they can be tested.

## Guiding constraints

Every item above is judged against the same four rules, which are why the
project exists:

1. **One container, no sidecars.** If a feature needs a second process to run,
   it is not this feature. A small pure-Go library is a different thing: it ships
   inside the one binary and costs nothing at run time. The bar is that the
   protocol would otherwise have to be written by hand and got wrong — DNS, TLS,
   SQLite and SNMP cleared it; a library that wanted to bring a daemon did not.
2. **One SQLite file.** The whole state is that file, and copying it is still a
   complete backup. The JSON export added above does not replace it and is not a
   second store: it is a document written out of this one database and read back
   into it, which is why it can express "without the history" and a file copy
   cannot.
3. **No frontend build step.** A change to a `.js` file is a reload.
4. **The numbers must be right.** Uptime percentages, gaps, and incident
   durations are the product. Anything that risks double-counting a probe or
   hiding a gap loses to anything that does not.
