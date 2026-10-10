# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Accounts replace the single dashboard password.** There is no global password
  any more, and no single credential to share. Setup asks for a username and a
  password and creates the first account with the `admin` role; the `409` the
  endpoint then returns means "an account already exists", not "a password
  exists". Accounts live in a new `users` table, so two people sharing one
  installation no longer share one password.
- **Three roles, ordered.** `admin` changes the configuration, rotates the API
  key and manages the accounts; `operator` acts on an outage that has already
  happened — acknowledge and un-acknowledge an incident, pause or resume a
  sensor, run a probe by hand, run the diagnostics — and changes nothing about
  what is monitored; `viewer` reads everything and changes nothing except its own
  account. Every route is assigned the lowest role that may reach it, in one
  table in `internal/api/server.go`, so the authorization policy is one screen
  rather than a search. The account editor will not remove the last active
  administrator, and nobody can disable or delete the account they are signed in
  with.
- **Sessions carry an identity.** The cookie names the account that signed in and
  the token epoch it was issued under, and every authenticated request re-reads
  the account row. Disabling somebody, changing their role or deleting them
  therefore takes effect on their next request rather than when a thirty-day
  cookie happens to expire. Changing a password bumps only that account's epoch:
  it signs that person out everywhere and nobody else at all.
- **Two-factor authentication.** An account can enrol a TOTP authenticator, fixed
  at RFC 6238's defaults — HMAC-SHA1, six digits, a thirty-second step, one step
  of drift either way — because those are what every app already speaks. Sign-in
  becomes two steps, and a code whose time step has already been spent is refused
  rather than accepted twice. The enrolment QR code is drawn on the server
  (`rsc.io/qr`, a PNG) and served `Cache-Control: no-store`, because the image is
  the shared secret. The second step is throttled by account rather than by
  address: by then the password is known, and the six digits are the thing being
  guessed at.
- **Eight one-time recovery codes.** Shown once when the second factor is enrolled
  and never again, each good for exactly one sign-in, hashed before storage and
  marked used rather than deleted. They are the way back in when the phone is
  lost, and the account's own password is asked for before a fresh set is issued.
- **Interface settings belong to the account.** Theme, accent, density, layout,
  language, interface mode and tour progress now live on the user row rather than
  as installation-wide `settings` rows, so people sharing an installation may
  disagree about the theme. A request authenticated with the API key has no
  account behind it, so those reads answer with the defaults and those writes go
  nowhere.
- **Example data can be taken back out.** Everything the setup screen or
  `seed --demo` invents is marked `origin = example`; `GET /api/examples` reports
  how many sensors, results and incidents there are, and `DELETE /api/examples`
  removes exactly those and cascades to their results and incidents. The mark is
  an origin column, never the group name or the `(example)` suffix, because both
  of those are things a person can edit.
- **The dashboard is tested in a real browser.** One test now renders the page
  instead of only calling the API: it starts the real binary, drives first-run
  setup, and asserts on what is on screen — that the loading spinner goes away,
  that exactly one view is visible, and that a reload keeps the session. It writes
  a screenshot on failure. It lives in its own module, `e2e/`, so that the browser
  driver never enters the product's dependency list. Run it with
  `make test-browser`.
- **`weatherglass doctor`.** One command that says what is wrong with an
  installation, built for the container: `docker exec weatherglass weatherglass
  doctor`. It reports the configuration, the data directory, the database's size,
  integrity and schema version, whether the stored sensors and channels still
  validate, and whether the daemon is answering — and exits non-zero if any of it
  is fatal. It opens the database read-only and never sends an alert, because a
  diagnostic that quietly repairs what it was asked to report, or fires a real
  message into a chat room, is worse than no diagnostic at all. It replaces the
  `weatherglass config check` the documentation had been promising.
- **A fresh installation is not empty.** First-run setup creates four example
  sensors — the dashboard's own health endpoint, a gateway ping, a public
  resolver, and a public website — grouped under *Examples* and marked
  `(example)` in the name. They are ordinary sensors: editable, pausable,
  deletable, and attached to no channel, so a new installation never sends a
  notification before its owner has decided where notifications should go. The
  first one points at Weatherglass itself, which means the dashboard is never
  entirely red on a machine that has no route to the internet. They are created
  only when the sensor table is empty and the configuration file declares no
  sensors — an installation driven from YAML is deliberate and is left alone, and
  a setup request repeated in the window before the account is stored does not
  produce a second set.
- **An interface mode, chosen at setup.** The dashboard can be run *simple* or
  *advanced* (`ui.mode`). Simple hides the areas a casual user does not need —
  the per-type options, channel selection, retries and reminders, tags, the API
  key, the appearance pickers — and folds the rest. It hides, it never disables:
  every sensor keeps probing at the interval it had and every channel keeps
  firing, so an installation configured in advanced mode does not quietly stop
  behaving that way because the picker that set it went out of sight. There is no
  built-in default; an installation that never answered the question reads the
  fallback and sees everything.
- **Field help instead of paragraphs.** Every field in the sensor form, the
  channel form and the settings screen now carries a small `?` beside its label
  that opens a panel with one sentence about what the field is for and one
  example value. It replaced around thirty blocks of help text that sat under the
  forms. The panel is opened by click and by keyboard, closes on `Escape`, and
  maintains `aria-expanded`. Channel fields get their example from the server's
  own description of the channel type, so an example cannot describe a field the
  sender does not read.
- **A password strength indicator.** The setup screen and the password change
  form show a bar and a word — weak, fair, good, strong — with one concrete
  suggestion about what is missing. It informs; it does not gate. The server's
  eight-character minimum is unchanged, and this is a local calculation with no
  dependency and no request to anything.
- **Incidents can be acknowledged.** Pausing a sensor stops watching it;
  acknowledging an incident stops the noise and changes nothing else. The
  acknowledgement hangs off the incident rather than the sensor and carries a
  free-text note, so "which outage did I silence?" has an answer and a sensor
  that fails again next week has not been excused in advance. It suppresses the
  next reminder; it does **not** suppress the first `down` alert, which fires on
  the transition before anyone can acknowledge, and it does not suppress the
  recovery alert, because "it is back" is useful to the person who acknowledged
  it too. Taking it back restarts the reminders immediately rather than waiting
  out the rest of the interval. `POST /api/incidents/{id}/ack` with a `note`,
  `DELETE` on the same path to take it back, and `POST /api/sensors/{id}/ack` as
  a shortcut for whatever that sensor currently has open — which answers `409`
  when nothing is down, rather than quietly doing nothing.
- **Diagnostics run from the dashboard.** The Settings screen has a panel that
  runs the same checks `weatherglass doctor` runs on the command line and shows
  both the individual results and the literal text the command prints, alongside
  the `docker exec` command to run it yourself. `POST /api/doctor`, because it
  reads the whole database and opens a socket — a `GET` would be cacheable and
  misleading. Repeated clicks share one run through a single-flight cache rather
  than starting a second full read of the database, and the run is bounded by the
  request's own deadline. One honest caveat, which the panel states: from
  the dashboard the configuration check is always green, because the daemon
  answering the request is by definition running.
- **A guided tour of the dashboard.** On the first visit after setup, a short
  sequence of steps highlights the parts of the page and explains them in one
  sentence each. It is data, not code: a step names a target, a placement, a
  title and a body, and may declare that it requires a mode or a piece of state,
  so steps whose target is absent are skipped rather than pointing at nothing.
  The second version switches views to follow its subject, takes one example
  sensor apart piece by piece — its name, target, last message, sparkline,
  statistics and actions — says plainly that the data is sample data, and ends by
  asking which of sensors, incidents or alerts to go into more deeply. New
  features add a step rather than a new tour, and the version number rises so the
  tour is offered to anyone who has seen an older one. Progress is stored on the
  signed-in account through `POST /api/onboarding/tour`, and the tour can be
  started again from Settings.
- **Example data says so, and can be loaded back.** Every sensor that came from
  the sample data now carries a badge on its card, so nothing invented is mistaken
  for a sensor you configured — a distinction that matters most on a wall display
  and in a screenshot. The Settings panel no longer disappears once the data is
  removed: it offers `POST /api/examples` instead, which loads the richer demo
  dataset — five sensors in two groups with an invented week of history — because
  somebody who deleted everything and asked for it back wants a populated
  dashboard, not an empty one. The call is idempotent: a name already in the table
  is left alone, so pressing it twice creates and invents nothing. On an
  installation whose sensors come from a configuration file it answers `409`, for
  the same reason the setup route is skipped there.
- **TLS certificate sensors.** A `tls` sensor takes a host or `host:port` and
  nothing else, because a certificate is a thing to watch in its own right rather
  than a side effect of an HTTP request. It reports the days left and separates
  the three problems that used to arrive as one "certificate not trusted": a name
  that does not match (`x509.HostnameError`), a certificate that has expired, and
  a chain this host does not trust. HTTP and TCP sensors share the same
  certificate helper, so all three report the number the same way.
- **Docker container sensors.** A `docker` sensor watches one container through
  the Docker Engine API over the unix socket, with a single
  `GET /containers/{name}/json` per probe and nothing else — it never starts,
  stops, restarts, removes or execs into anything, and there is no Docker client
  library in the tree. It checks the running state, the `HEALTHCHECK` result where
  the container has one, and the restart count, which is reported on every probe
  whether or not a limit is set. The health assertion is on by default and the
  form offers switching it off as a named choice, because most containers have no
  `HEALTHCHECK` and one can never report `healthy`; a probe that meets such a
  container says which option to clear rather than only reporting the mismatch. A
  socket that is missing or unreadable fails the probe with a message saying
  which, rather than refusing to save the sensor: the socket belongs to the host
  and may well be absent while you are writing it. Mounting it gives the container
  root on the host, which `SECURITY.md` now states plainly and
  `docker-compose.yml` carries commented out.
- **SNMP sensors.** A `snmp` sensor asks one OID over v2c and checks the answer
  against a substring or a numeric range — enough for a switch, a router or a NAS,
  and deliberately not a walk, not traps and not v3. This reverses a documented
  "out of scope" decision, so both roadmap files now say what the scope actually
  is. OIDs are written as dotted numbers rather than names, because resolving a
  name needs a MIB file and a tool that quietly guesses at one quietly watches the
  wrong thing. The community string is a credential: it carries the `Secret` flag
  and is masked in the API like a bot token. v2c is unauthenticated and the
  community travels in clear text, which `SECURITY.md` and the security page say
  rather than imply. It brings a library with it — `gosnmp`, pure Go, no sidecar —
  and the roadmap's "no dependencies" rule now spells out the difference between a
  library that ships inside the one binary and a daemon that does not.
- **The About panel says when the binary was built.** A development build has no
  version to tell it apart from the last one — they are all `0.1.0-dev` — and the
  first question about a feature that is not on screen is whether the running
  binary predates it. It was found exactly that way: a dashboard served from a
  build made before the `docker` and `snmp` types existed, with nothing on screen
  to say so. The browser test now also compares the type list in the form against
  `model.SensorTypes`, so a type implemented in Go and missing from the dashboard
  fails a test rather than confusing somebody.
- **Sites.** A sensor can name the place it lives — the rack in the cellar, the VPS
  at a hoster — in a new `sites` table with a kind (`local` or `external`) and
  optional coordinates. The reference from a sensor to a site is by **name** rather
  than by id, which is what lets the configuration file describe a place with one
  line (`site: Rack A`) instead of a second lookup table; `ON UPDATE CASCADE` makes
  a rename one statement and `ON DELETE RESTRICT` makes deleting a site that is
  still in use a refusal with a count rather than a silent cascade. A sensor
  without a site behaves exactly as it did before sites existed, and
  `DELETE /api/sites/{name}?unassign=1` is the deliberate way to remove a place and
  leave its sensors without one. `lat` and `lon` are nullable and **0 is not
  "unknown"** — 0/0 is a real coordinate in the Gulf of Guinea — so a site with no
  coordinates groups and shows and is simply not drawn.
- **Five views of the dashboard.** The two layouts became five, and they stay one
  setting rather than five pages: `ui.layout` now takes `tree`, `wall` and `map`
  beside `grid` and `list`, and the top bar carries a switcher for them. **Tree**
  folds by site and then by group, with the worst status beneath a heading so a
  closed branch still says whether anything inside it is unhappy. **Wall** is a
  tile per sensor with a **heartbeat** — one rectangle per recent check, green or
  red, from a new `heartbeat` field on `StatusView` filled from the same `recent`
  map the sparkline already used, so it costs no extra query. A heartbeat exists
  because the sparkline cannot carry one: it draws a failure and a genuine 0 ms
  success the same way, and a wall that reported a fast probe as an outage would be
  worse than no wall. **Map** draws sites that have coordinates on real raster
  tiles, with the markers placed by projection alone, so a homelab with no route
  to the tile server sees a plain grid with the places correctly arranged on it and
  one line saying the tiles could not be reached. Homelab and external sites differ
  in shape as well as colour, because the colour is the status.
- **An account menu in the top bar.** Who is signed in, their role, the theme,
  density and language switches, a way into Settings, and **Sign out** — which
  until now was reachable only from the bottom of the Settings page. The switches
  and the Settings screen share one write path (`persistUI`), so a refused write
  puts the previous value back everywhere it was shown instead of leaving the
  screen promising something the server did not keep.
- **A tile server can be configured, and is named in the content security
  policy.** `PUT /api/map` stores an installation-wide `tile_url` and
  `attribution`; the default is OpenStreetMap, which needs no account and no key.
  `setSecurityHeaders` appends the configured tile origin to `img-src` — without
  it, every remote tile would be blocked **silently** and the map would simply be
  empty — and the origin is resolved once at start-up and again when the URL is
  saved, and cached on the server, so a new tile source takes effect without a
  restart and serving a page costs no database read. The URL must be `http(s)` and
  must carry `{z}`, `{x}` and `{y}`; `{s}` is optional. The browser fetches the
  tiles itself and the daemon never proxies them, which means the tile server sees
  the viewer's address and roughly where they are looking — stated in
  `SECURITY.md` and in the settings panel rather than left to be discovered.
- **Site tests, in Go and in the browser.** `internal/store/sites_test.go` and
  `internal/api/sites_test.go` cover the foreign key, the rename cascade, the
  refuse-or-unassign delete, the coordinate rules and the tile-URL validation.
  `e2e/shell_test.go` adds three browser tests: the account menu (open, role, the
  quick switch surviving a reload, sign-out falling back to the sign-in shell), the
  layout switcher (each view's own element, surviving a reload, absent on other
  routes), and the site round trip — created, assigned, then seen in the tree, the
  wall and the map, with the markers compared before and after the tile server is
  broken, which proves they depend on the projection and nothing else.
- **One search box, and it looks through everything.** The field in the top bar
  used to say "Search sensors" and find sensors. It now sits in the middle of the
  bar and answers about sensors, sites, channels, incidents, the individual probe
  messages of the retention window, the settings panels, and (for administrators)
  the accounts. `GET /api/search?q=…` runs the six queries in one transaction and
  answers `{"hits":[…],"truncated":bool}`; sensors, sites, channels, accounts and
  the settings are answered from what the page already holds and appear as you
  type, while the incidents and the probe messages arrive a moment later from the
  server. A hit carries the kind, what to do with it and a title, so pressing it
  opens a sensor's window, jumps to a settings panel, or — for an incident or a
  probe message — opens the sensor it belongs to, where the message is shown in
  its own context. Two things are deliberately not searchable: a sensor's
  `options` and a channel's `config` hold credentials, and a search box that
  answers with somebody's bot token is worse than no search box; a channel's
  masked value would be a hit that says nothing anyway. Account hits are
  administrator-only, and that check is in the handler rather than the interface.
  A query must be between two and sixty-four characters, and a shorter one is
  answered with an empty list rather than an error, because somebody typing one
  letter at a time has done nothing wrong.
- **The search matches literally, not as a pattern.** Every comparison is
  `instr(lower(column), lower(?))`, never `LIKE`. A search term is not a pattern:
  a `%` typed on a keyboard has to mean a percent sign. `LIKE` would need an
  escape function *and* an `ESCAPE` clause in each of six queries, and one
  forgotten clause turns a search into a full scan of the history that finds the
  wrong thing on the way. `instr` knows no pattern and has no such failure mode.
  The cost is the same scan `LIKE '%…%'` pays anyway. The one table that grows
  without bound is bounded twice instead: by the raw retention window and by a
  per-kind `LIMIT` (twenty, at most fifty), with an overall cap of two hundred and
  a `truncated` flag when a kind had more to give.
- **The dashboard moves when a probe lands, and only where it should.** The event
  stream was already carrying every result; the dashboard answered all of it by
  rebuilding the whole board, which closed any tree branch you had opened and took
  the focus out of whatever you were typing — once a minute. Now a `result` patches
  **one card**: its figures, its status dot, its sparkline and its heartbeat, in
  place, with a brief highlight only when a sensor's *status* changes rather than
  its number. `status` and `incident` leave the debounced-refresh path, and a
  sensor or channel event still triggers a refresh, because what moved there is the
  shape of a list and no patch can express that. A detail window that is open while
  its sensor is checked is left alone — a chart that rescales under somebody who is
  reading it has to be read again — and offers a quiet "N new checks — refresh"
  instead. Under `prefers-reduced-motion` the values still arrive; only the
  animation is dropped.
- **Live updates have an off switch, visible in the top bar.** A dot and a word at
  the right-hand end show whether the event stream is connected, which is also the
  first place a dropped connection is visible at all. Clicking it toggles the new
  per-account preference `ui.live` (`on` by default). Off means the browser does not
  subscribe: no stream, no patching, no traffic, with the minute-by-minute refresh
  and the reload on returning to the tab still running underneath.
- **The settings page is split into five tabs, and only the chosen one is shown.**
  Ten identical panels in a column became **Look**, **Monitoring**, **Access**,
  **Backup** and **Help and maintenance**, each with a tab that carries its icon and
  its name, and the other categories are not further down the page — they are
  `hidden`. The panels stay in the document and stay direct children of the same
  container, so every delegated listener, every panel id and the map panel's own
  readers are untouched, and the simple/advanced fold still works inside a tab: a
  tab whose panels are all folded away goes with them, which is how a viewer sees
  no **Backup** tab at all. Each panel also carries a `data-panel` key, and a
  settings hit in the search **switches to that panel's tab** rather than scrolling
  to a panel that is no longer on screen. Which tab you are on is kept in the page
  and not on the account — it is where you are looking, not a setting — and it
  survives the redraw that every poll causes.
- **The dashboard header is a strip of widgets.** The ring and the four figures
  that used to be the whole header are now five widgets among eleven, and which ones
  are on it, in what order, is a per-account setting (`ui.widgets`). The new ones
  are a **clock**, the **recent incidents**, the **slowest sensors**, the **most
  outages**, a **small map** of your sites and a row of **shortcuts**. It is
  arranged by dragging the tiles, with ◀ ▶ buttons beside them because a list that
  can only be reordered with a pointer is a list somebody cannot reorder at all.
  The button that starts the arranging is in the top bar beside the layout switcher,
  and while it is on, the switcher stands down for the **+** that opens the widget
  picker: the header exists only in the card view, so the switcher is unusable while
  it is being arranged, and it is the width of the button that replaces it — which
  is what keeps the top bar on one line. Adding a widget is a window listing the
  ones that are not on the header, not dashed placeholder tiles in the strip, which
  had to be read as controls before they could be used. Neither control carries a
  `data-widget`, and neither is inside `#summary`. The same list with the same order
  is in **Settings → Monitoring → Dashboard defaults** as checkboxes. A fresh account
  starts with exactly the header that was there before, so nothing moves for anyone
  who does not ask for it. Two details are
  load-bearing: the list is read off `#summary > [data-widget]`, so a control inside
  the strip cannot be saved as a widget that does not exist, and the map tile is
  mounted only once its host has a real size and then only has its pins recoloured —
  `map.js` measures its host at mount and is never asked again, so a tile mounted
  behind a hidden view would keep the fallback size for the session. The map tile
  also gives up its keyboard handling, because a focusable canvas in the header
  would swallow the arrow keys of anybody tabbing across the top of the page.
- **Incidents can be filtered by state, sensor, period and a word.** The single
  "Ongoing only" switch became a filter line in the same shape as the rest of the
  product: a segmented All / Open / Resolved control, a sensor dropdown, period
  chips, and a small text field that matches the cause, the sensor's name and the
  acknowledgement note. `GET /api/incidents` takes `state`, `sensor`, `range` (or
  `since`) and `q`; `open=1` is kept as the older spelling of `state=open`, because
  it is what a script written against the last version sends. An empty list under
  a filter says that the filter is what emptied it, rather than leaving somebody to
  look for a fault that is a setting on the same screen.
- **The channel editor explains how to get the credentials.** Each channel type in
  `internal/notify` now carries a `Setup` recipe — numbered steps, shown under the
  fields in the editor, always visible rather than behind a disclosure, and redrawn
  when the type changes. It is where the question "what is a bot token and where do
  I find one" is finally answered: `@BotFather` and `/newbot` for Telegram, the
  Integrations → Webhooks path for Discord, the homeserver without `/_matrix` and
  the access token in Element for Matrix, the topic and its guessing hazard for
  ntfy, the Incoming Webhooks app for Slack, and the headers a generic webhook
  receives. Each one ends where it should: the Telegram recipe says in as many
  words that the `getUpdates` address contains the token and belongs in no chat and
  no ticket.
- **An installation can be backed up and restored from the dashboard.** The
  **Backup** panel writes one JSON document — settings, sites, sensors, channels and
  accounts, plus the incidents and the hourly and daily rollups for the last 7, 30
  or 90 days or for everything — and reads one back in. `GET /api/backup?scope=…&days=…`
  streams it with a server-built `Content-Disposition` filename and
  `Cache-Control: no-store`; `POST /api/restore` takes the file as the request body,
  capped at 64 MiB, because the project has no multipart path and this is not the
  place to invent one. Before anything is replaced the file is checked — a document
  that is not a `weatherglass-backup`, or one from a newer schema, is refused and
  nothing is written — and a byte-exact `VACUUM INTO` safety copy is taken beside
  the database; if that copy cannot be written, the restore stops. A table the file
  does not mention is left alone, so restoring a configuration never wipes history,
  and the running scheduler is reconciled afterwards so probes resume against the
  restored sensors. Sensors, sites and channels travel by **name** rather than by
  row id, because a name is what a sensor is in this program and an id from another
  installation is a different thing wearing the same number. **The file carries
  every secret in clear text** — password hashes, TOTP secrets, recovery-code
  hashes, the API key and every channel's credentials — because a backup with masked
  credentials would restore a channel that cannot send anything, and nobody would
  find out until the first outage. That is stated on the panel, in the changelog
  here, and openly in `SECURITY.md` and `docs/concepts/security.md`. Nothing from
  the file, and no credential in it, is ever written to a log line: the export and
  the restore log row counts per table and nothing else.
- **A site can be made while the sensor is made.** The sensor form's place picker
  ended with the places that already existed, so the choice when a sensor stood
  somewhere new was to leave the half-filled form, declare the place under Settings
  and come back — or to give up on the place. The picker's last entry is now **New
  site…**; choosing it brings up a name and a kind, and the place is made together
  with the sensor. It is made **on save** and not when the entry was chosen, so a
  form closed again leaves nothing behind and a name that already exists is used
  rather than made twice. The two fields stay off screen until that entry is picked,
  so nothing in the form invites a name for a place nobody asked for.

### Changed

- **Sign-in is per account now, and every existing session is invalidated once.**
  The session cookie gained an identity and a version, so a cookie issued by an
  older build is rejected with a plain "sign in again". Every session everywhere
  stops working on the first start of this version, which is the only way to
  retire a cookie format that carried no identity at all. An installation
  upgrading from the single-password model has its global password hash moved
  into an administrator account named `admin` — the username every such
  installation now signs in with — and the old `settings` row is deleted with it,
  along with the global `ui.*` settings, which move onto that account. The
  migration runs only on a database that has no accounts at all, so it cannot
  overwrite an installation that has already been through it. A password left in
  `WEATHERGLASS_ADMIN_PASSWORD` still bootstraps an account named `admin`, now
  with the admin role, on a database with no accounts.
- **Help text beside the fields became `?` buttons.** See above: the paragraphs
  are gone, and the answer to "what is this field?" is one click away instead of
  always on screen.
- **"Checks" are now called sensors.** The monitored objects were a "check" in
  the UI, the API, the database, the YAML file and the Go source at the same
  time as "check" was the verb for probing one, which made both words harder to
  read. Everything a person sees or writes now says *sensor*. The rename is
  complete and deliberately has no compatibility shims:

  | | before | now |
  |---|---|---|
  | UI and docs | "Checks" | "Sensors" |
  | HTTP API | `/api/checks`, `/api/check-order` | `/api/sensors`, `/api/sensor-order` |
  | JSON | `check_id`, `checks`, `check_count` | `sensor_id`, `sensors`, `sensor_count` |
  | YAML | `checks:` | `sensors:` |
  | Webhook payload | `{"check": {…}}` | `{"sensor": {…}}` |
  | Alert templates | `{{.Check.Name}}` | `{{.Sensor.Name}}` |
  | Database | `checks`, `check_channels`, `check_id` | `sensors`, `sensor_channels`, `sensor_id` |
  | Go packages | `internal/check`, `model.Check`, `check.Checker` | `internal/sensor`, `model.Sensor`, `sensor.Prober` |

  Two consequences worth reading before upgrading:

  - **A saved alert template that refers to `{{.Check.…}}` will fall back to the
    built-in text.** The custom template is replaced by a variable that no
    longer exists, and a template that fails to render is deliberately not
    allowed to lose the alert. Edit the template to `{{.Sensor.…}}` to get it
    back.
  - **The database cannot be upgraded in place.** Nothing has been released yet,
    so there is one schema and it is spelled correctly from the start rather
    than carrying the old names in a migration forever. A file from before the
    rename is refused at startup with a message that says so, instead of failing
    later with "no such table: sensors".
  - A configuration file that still uses the old top-level `checks:` key is
    rejected with an error naming the new one. The loader was made strict at the
    same time, so a misspelled key is now a startup failure rather than a line
    that is silently ignored.

- **Sign out moved into the account menu.** It used to be a button at the bottom
  of the Settings page, which is not where anyone looks for it. The handler is
  unchanged; the button on Settings is gone, and the menu in the top right corner
  is now the one place an account ends. The tour's version rises to 3 for the two
  steps that explain the menu and the view switcher, so an installation that has
  already seen an older tour is offered this one.
- **A tour step can require a layout.** Eight of the tour's steps take a sensor
  card apart, and a person who had saved the wall or the tree would have been
  shown a highlight ring around nothing at all. A step may now name the layout it
  belongs to, exactly as it could already name the view, and the runner switches to
  it and back.
- **The top bar is a three-column grid, so the search box is actually centred.**
  It was a flex row with the tabs taking all the slack, which pushed the search
  field up against the buttons on the right and left "centred" meaning "wherever
  the tabs happened to end". The bar is now `1fr auto 1fr` with the brand and the
  tabs in the left column and the actions in the right, so the field sits in the
  middle of the bar rather than in the middle of what was left over. `#tabs` keeps
  its id and its position in the document, which is what the tour and the
  screenshot program navigate by — only its parent changed. Below the existing
  breakpoint the columns stop being three and the bar folds its working controls
  away instead; see the entry below.
- **"Search sensors" is gone as a name.** The field searches everything now, so the
  placeholder, the accessible name and the i18n entry all say "Search". The old
  label was not wrong about the behaviour it described; it was the behaviour that
  changed.
- **"Ongoing only" is gone from the incidents screen**, replaced by the filter line
  described above. `GET /api/incidents?open=1` still works and is still what a
  script should send.

- **The top bar gives up its words before it gives up its line.** Every tab and
  button in the bar carried a word and nothing else, and the bar holds one line by
  wrapping its right-hand column when the two no longer fit — so on an ordinary
  laptop the layout switcher, New sensor and the account chip slid onto a second
  row, and everything under them moved down. Each one now carries an icon beside its
  word and, below roughly 1570 pixels, the words are dropped and the icons stay. The
  label moves onto the button for the same reason: a word hidden with `display: none`
  is not read out at all, and an icon with no name is a control nobody who has not
  seen it before can use.
- **And when the words are gone and it still does not fit, the four working controls
  fold into a `⋮`.** Below roughly 1130 pixels the bar used to wrap after all — two
  rows, then three at 600 pixels and four at 440, each side breaking inside itself —
  which is exactly the sliding and the pushed-down page the words were sacrificed to
  avoid. The layout switcher, the header's Customise button, New sensor and the Live
  indicator are now laid out as the rows of a panel behind a `⋮` beside them, by CSS
  alone: the same elements, so every listener, label and piece of state goes on
  working, and the words they gave up come back with them because a panel has no line
  to buy. The line they freed is the search field's, and below about 620 pixels the
  field leaves the bar altogether rather than shrink to a stub.
- **The search field keeps its place in the row as a magnifier, rather than moving in
  behind the `⋮`.** A search box that has to be unfolded out of a menu is a search box
  nobody on a phone uses, so the bar draws a `#topbar-search` button in the row where
  the field would have been, and one press lays the field back over the bar at the
  full width of it — the only shape worth typing in at that width. Escape puts the bar
  back. It is the one control the bar never gives up, on the same reasoning that makes
  the field itself the last thing to leave; and because a field that is not in the bar
  is not in the tab order either, it is a real button rather than a picture of one.
  Below 380 pixels the tabs give up their side padding to pay for it: the strip scrolls
  rather than overflows, so a magnifier that had squeezed the last tab out of sight
  would have been a bar that still measured as one line. The bar is one line at 320
  pixels as it is at 2400. Every breakpoint is measured against what the bar is
  actually holding rather than guessed at, and `e2e/shell_test.go` measures them —
  including the tab strip, which nothing else in the bar would have caught.

### Fixed

- **A ping probe never sent a packet on the socket it prefers to use.** The sensor
  opens an unprivileged datagram ICMP socket first, because that is the one that
  works without `NET_RAW` — and then addressed its echo requests with a
  `*net.IPAddr`. On a datagram socket the destination has to be a `*net.UDPAddr`;
  the IP form reaches `net.UDPConn.WriteTo`, which refuses it with `EINVAL` before
  anything leaves the host. Every such probe therefore failed, on Linux and on
  macOS alike, with `no ICMP reply from <host> (3/3 lost)` — a message about the
  network, produced by a socket that had sent nothing at all. The address is now
  built for the socket that will carry it, and a regression test pins the two
  together without touching the network. The failure is also told apart from the
  one it used to imitate: a probe that never sent says `could not send ICMP to
  <host>` and names the kernel's reason, the way a probe that sent and was ignored
  says how many replies were lost.
- **A failed request left no trace of why.** Thirty-four handlers returned a
  server-side error to the browser and only four of them logged anything, so a
  `500` in the terminal named the endpoint and nothing else — and the browser was
  told only, for example, `could not load sensors`. Every error response now carries
  its cause up to the middleware, which writes exactly one line per request: at
  `error` as `msg="http request failed"`, with the message the client was given and
  the error it never sees, or the usual `info` line otherwise. No request can end in
  a `5xx` silently any more.
- **A reload during the password hash threw the password away.** Hashing takes about
  a quarter of a second, and the handler wrote the result with the request's own
  context — so a browser that navigated away inside that window cancelled the write
  at the last moment. The password was not stored, nothing said so, and the next page
  load showed the setup form again as though it had never been filled in. The write is
  now detached from the connection and bounded by its own timeout, which leaves the
  outcome intact. The same applied to changing a password. Both now have a regression
  test that cancels the request mid-flight.
- **A full-height loading spinner could sit on top of a dashboard that had loaded
  perfectly.** The screens are switched by setting the `hidden` attribute, and a
  rule in the dashboard's own stylesheet was able to outrank it: `[hidden]` is a
  user-agent rule, so `.boot { display: grid; min-height: 100vh }` won. Every
  request returned `200` and the data arrived, so nothing but a rendered page could
  see it. There is now an explicit `[hidden] { display: none !important }`, and a
  test that fails without it.
- **Setting the password on first run looked like it had been rejected.** The same
  cause: the setup card set `hidden`, the card stayed on screen, and a password that
  had in fact been created looked like a form that had reset itself.
- **The answer to the Test button was drawn behind the dialog that asked for it.**
  A toast was appended to the page's own container, and a modal `<dialog>` lives in
  the browser's top layer and paints its backdrop over everything in the normal flow
  — so the one message that matters arrived at the one moment it could not be read,
  while the sensor form was still open. The toasts now move inside the open dialog
  for as long as it is open, the same move the field help already made, through one
  shared rule for where an overlay goes. The browser test asks what is under the
  pointer, not what exists in the DOM.
- **The Test button looked like it did nothing.** It checks the draft in the sensor
  form without saving it, and its answer had exactly one place to go — a thin red
  line at the bottom of a form that is taller than the window. That line was also
  the whole of the reply in the most common case there is: a draft the server
  refuses, which is precisely what the button is for. Every outcome now writes the
  line *and* raises a message over the form — the refused draft, the probe that ran
  and failed, the probe that could not be made at all, and the request that never
  got there. Both, because they are read at different moments, and only the line
  survives the message taking itself away. The browser test drives a form with no
  target and asks what is readable on screen.
- **`dashboard listening` was logged before the port was bound.** A port collision
  therefore read as "it started, then something unrelated broke". The listener is
  bound first, and the error names the port and says what to check.
- **The shell is served with `Cache-Control: no-store`.** The shell names the script
  files to load, so a cached copy pins a browser to a stale dashboard — and
  revalidating does not help when there is no validator to revalidate against.
- Non-asset requests are now logged at `info`, so the boot sequence is readable from
  the terminal. `GET /`, `/api/session` and `/api/summary` together tell a rendering
  problem apart from a connectivity one.

## [0.1.0] - 2026-10-08

The first release: a working foundation that monitors, records, and alerts, with
the structure in place to grow.

### Added

- **Sensors** — HTTP(S), TCP (with optional TLS handshake and send/expect
  exchange), ICMP ping, and DNS (A, AAAA, CNAME, MX, TXT, NS, SRV, PTR, with an
  optional custom resolver). Each type validates its own target and options, in
  the UI as well as on save.
- **Dashboard** — live status grid fed by Server-Sent Events, per-sensor
  response-time charts, uptime bars, sparklines, incident timeline, search,
  grouping, and tags. Theme (auto/dark/light), accent colour, density, and
  layout are selectable in Settings.
- **History** — three resolutions with retention: raw samples for 7 days, hourly
  rollups for 90 days, daily rollups forever. Exactly one resolution answers any
  given range, so no probe is counted twice; gaps are reported as gaps.
- **Incidents** — a state machine per sensor: a configurable number of
  consecutive failures before an incident opens, repeat alerts while it stays
  open, and a recovery alert naming how long the sensor was down. State is
  rebuilt from the database on restart, so a restart neither loses an outage nor
  re-announces one.
- **Alerts** — Telegram, Discord, Matrix, ntfy, Slack, and generic webhooks with
  HMAC-SHA256 request signing. Per-sensor channel assignment, per-sensor reminder
  intervals, custom title and body templates with a safe fallback when a template
  is broken, and a test button per channel.
- **Authentication** — first-run setup screen, bcrypt password hashing,
  HMAC-signed session cookies, an API key for scripts, cross-site protection for
  cookie-authenticated writes, and an in-memory login rate limit.
- **Configuration** — YAML file, `WEATHERGLASS_*` environment variables, and
  command-line flags with a documented precedence order, plus day and week
  duration suffixes for retention settings.
- **Declarative sensors and channels** — declare them in the configuration file
  and they are re-applied on every start.
- **API** — a documented JSON API for sensors, history, incidents, channels,
  settings, and an SSE event stream. Sensor updates are partial: a body changes
  only the fields it sends, and an explicit empty value clears a field.
- **CLI** — `serve`, `version`, `healthcheck` (used by the image's
  `HEALTHCHECK`, so no shell is needed), and `seed --demo` for a synthetic week
  of history.
- **Packaging** — multi-arch container image (amd64, arm64, armv7) on a
  distroless, non-root base with the dashboard embedded; static binaries for
  Linux, macOS, Windows, and FreeBSD; CI running formatting, vetting,
  `go test -race`, a seven-platform build, and `golangci-lint`.

### Security

- Channel credentials are never returned by the API — the UI receives a marker
  instead and sending the marker back preserves the stored value.
- The Telegram bot token is stripped from error messages before they reach a log.
- `insecure_skip_verify` is available only as an explicit per-sensor opt-in.
- The session cookie is `HttpOnly` and `SameSite=Strict` and deliberately omits
  `Secure`, because homelab installs commonly serve plain HTTP on the LAN.

[Unreleased]: https://github.com/j000k3r/weatherglass/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/j000k3r/weatherglass/releases/tag/v0.1.0
