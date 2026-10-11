# Architecture

Weatherglass is one process: an HTTP server, a scheduler, and a SQLite file. This
document explains how the pieces fit together and why they were cut this way.
It is written for someone about to change the code, not for an end user —
[CONFIGURATION.md](CONFIGURATION.md) covers the knobs.

## Shape of the process

```
cmd/weatherglass        flags, wiring, graceful shutdown
  ├── internal/config   defaults → YAML → environment → flags → validate
  ├── internal/privdrop the data directory is handed over, then root is given up
  ├── internal/doctor   read-only inspection, for `weatherglass doctor`
  ├── internal/store    one *sql.DB, schema, queries, accounts, rollups, retention
  ├── internal/scheduler  one goroutine per enabled sensor
  │     └── internal/sensor   http | tcp | ping | dns
  │     └── internal/notify  telegram, discord, matrix, ntfy, slack, webhook
  │     └── internal/event   in-process pub/sub → SSE
  ├── internal/totp     RFC 6238 one-time passwords, for the second factor
  └── internal/api      HTTP handlers, sessions, accounts, CSRF, SSE, doctor
        └── internal/web  embed.FS with index.html, app.js, charts.js, i18n.js,
                          style.css, users.js, password.js, tooltip.js, tour.js
```

Startup is deliberately linear: load configuration, hand the data directory over and
give up root (`internal/privdrop`), open the store, apply the declarative sensors, build
the scheduler and the dispatcher, build the API server, then serve until the signal
arrives. Shutdown cancels the root context, waits for the scheduler's goroutines, and
closes the database.

## Storage

Everything is in one SQLite file, opened with `modernc.org/sqlite` — a pure-Go
translation of SQLite, so `CGO_ENABLED=0` holds and the binary cross-compiles to
a Pi without a toolchain. The connection pool is pinned to a single connection
(`SetMaxOpenConns(1)`): the writes are tiny, correctness and lock-freedom are
worth more than concurrency on a homelab workload. WAL, `busy_timeout=5000`,
`foreign_keys=ON`, and `synchronous=NORMAL` are set on open.

```
sensors           one row per sensor, options as a JSON blob, origin to mark examples
channels          one row per alert channel, config as a JSON blob
sensor_channels   many-to-many, both sides ON DELETE CASCADE
results           raw probe results, indexed by (sensor_id, ts)
results_hourly    bucket = "2026-10-08T13:00:00.000000000Z"
results_daily     bucket = "2026-10-08"
incidents         started_at, resolved_at, cause, and the acknowledgement
users             one account per row: username, password hash, role, TOTP state,
                  token epoch, prefs (this account's interface settings)
recovery_codes    one hashed one-time code per row, marked used rather than deleted
settings          key/value: the API key and the session secret — the
                  installation-wide facts that belong to no single person
```

The `settings` table is deliberately smaller than it used to be. Anything that
belongs to a person lives on their `users` row now: the interface preferences as a
`prefs` JSON object, and the second-factor state beside them. What remains in
`settings` belongs to the installation and to nobody in particular — the API key and
the session secret — alongside two rows that migration v2 removes on the way up:
`auth.password_hash`, the single password of a pre-accounts installation, which it
reads into an administrator named `admin`, and the global `ui.*` rows, which move
onto that account. The migration runs only when the `users` table is empty, so it
cannot overwrite an installation that has already been through it.

Two schema decisions carry weight beyond their size:

**Timestamps are fixed-width UTC strings.** They are written as
`2006-01-02T15:04:05.000000000Z`, never `RFC3339Nano`, because the latter drops
trailing zeros and `…:00.5Z` then sorts *before* `…:00Z`. Since every range query
is a lexicographic `BETWEEN` on `ts`, the format is a correctness requirement, not
a style choice. `store_test.go` pins it down, including a test that asserts the
RFC3339Nano form sorts wrong.

**Buckets are derived from the timestamp prefix.** An hourly bucket is
`substr(ts,1,13) || ':00:00.000000000Z'`, a daily one `substr(ts,1,10)`. No
boundary arithmetic, no timezone surprises, and the rollup statements can be
expressed in pure SQL.

### Three resolutions, one answer

Raw samples live for `retention.raw` (default 7 days), hourly rollups for
`retention.hourly` (default 90 days), daily rollups forever. Each rollup row
stores `samples`, `ok`, and the latency sum/min/max for its bucket, which is
enough for uptime percentages and latency averages without keeping every probe.

A range query selects **exactly one** resolution — raw if the window fits in the
raw window, hourly if it fits in the hourly window, daily otherwise — and reads
only from that table. This is the property that keeps the numbers honest: if two
resolutions could contribute to one chart, samples inside the overlap would be
counted twice and uptime would quietly drift above 100%. A test
(`TestSeriesPicksOneResolution`) probes exactly this.

Rollups are idempotent. The insert is an
`ON CONFLICT DO UPDATE SET samples = excluded.samples`, so a repeated pass
rewrites the same bucket rather than accumulating it. Only *complete* hours and
days are rolled up — the boundary is the start of the current hour or day — which
is why the hour you are currently in is never summarised half-finished. Raw
pruning deletes rows below the raw window; the rollups they fed stay.

Charts therefore render a gap as a gap. A bucket with no samples is returned as
empty rather than as a zero, because "we did not probe" and "the probe failed"
are different facts and a monitoring tool that conflates them is worse than
useless.

## Sensors

`internal/sensor` defines one interface:

```go
type Prober interface {
    Type() model.SensorType
    Validate(model.Sensor) error
    Run(context.Context, model.Sensor) (Outcome, error)
}
```

Adding a sensor type means writing one file with those three methods and
registering it. `Options` is a `map[string]any` with typed accessors (`Str`,
`Int`, `Bool`) that return defaults for anything missing or of the wrong type, so
a sensor saved by an older version keeps working — a `count` that arrives as a
string falls back to the default rather than becoming zero.

Each engine validates itself, and the same validation runs on the draft from the
UI (`POST /api/sensors/validate`), so the form can tell you the target is not
`host:port` before you save it rather than after.

Two engine details worth knowing:

- **Ping** tries an unprivileged datagram ICMP socket first, then a raw socket,
  and reports precisely which one failed and why when neither works. Silent ping
  failure is the most confusing thing about containerised monitoring; a sensor
  that cannot ping says so.
- **DNS** builds its own queries with `golang.org/x/net/dns/dnsmessage` instead
  of leaning on the system resolver, so it can point at a specific server and
  inspect the actual records rather than whatever the resolver decided to return.

## Scheduler and incident lifecycle

One goroutine per enabled sensor, each on its own ticker with a little jitter so
that thirty sensors configured at `60s` do not fire in the same millisecond. The
scheduler does not own the sensor list: `Reconcile` diffs the stored sensors
against the running loops and starts, restarts, or stops them, and it is called
after any change made over the API. A sensor edited in the UI therefore changes
behaviour without a restart. The loops, though, belong to `Start`: `Reconcile`
reaches the scheduler from inside an HTTP request, and a loop parented to that
request would die with the response, halfway through its first probe. Before the
daemon has started its scheduler, `Reconcile` rebuilds the sensors' runtime state
and starts nothing.

Per sensor, the runtime state is: the current status, the consecutive-failure
count, the open incident, and the time of the last alert. On startup this is
rebuilt from the database — the running failure streak and any still-open
incident included — so a container restart neither loses an outage nor
re-announces one that is already known.

The state machine is small and lives in one function:

```
success  → fail streak resets; if an incident was open, close it and alert "up"
failure  → fail streak grows; when streak == retries, open an incident and alert "down"
           while an incident stays open, alert again every reminder_every
```

The transition and its incident row are written while the mutex is held; logging,
event fan-out, and alert delivery happen after it is released. Notifications
never run inside the lock, so a slow Telegram API call cannot stall every sensor
in the process.

Alert delivery fans out to a sensor's channels in parallel goroutines, each with
up to three attempts and a linear backoff; a `429` or a `5xx` is retried, other
`4xx` responses are not, because a bad token will not fix itself. A channel that
keeps failing has its error recorded and surfaced in the UI, and never suppresses
the others. `ProbeNow`, which backs the dashboard's
"Run now" button, deliberately bypasses all of this: a manual probe is
informational and never opens an incident or fires an alert.

## API and authentication

`net/http`'s `ServeMux` with method-and-pattern routing (`GET /api/sensors/{id}`)
— no router dependency, and Go 1.22+ handles the path variables.

`internal/api` is split by concern: `server.go` builds the routing tree,
`handlers.go` holds the endpoints, `auth.go` the sessions, account lookups and
sign-in logic, `users.go` the account administration, `totp.go` the second factor,
`mask.go` the secret redaction. Handlers are thin: parse and validate the body,
call the store, write JSON. Validation errors come back as
`{"error": "..."}` with a 4xx status and are shown verbatim in the UI, so the
message a user sees is the one the server wrote.

Updates are partial by contract: every field of a request body is a pointer, so
"absent" is distinguishable from "empty", and the handler overlays only what the
client sent on top of the stored row. A body carrying `{"target": "..."}` changes
the target and nothing else; an explicit `""` or `[]` clears a field. The
alternative — treating an absent field as an empty one — silently blanks the
name, clears the group, and resets the interval to its default.

Authentication resolves the request to a `principal`: an account, or the API key.
There are two credentials:

- **Session cookie** — `weatherglass_session`, value
  `v2.<userID>.<tokenEpoch>.<expiryUnix>.<hmac>` signed with `auth.session_secret`
  and generated on first start. Every authenticated request re-reads the account
  row and compares the epoch with the one in the cookie, which is the whole reason
  a role change, a disable or a delete takes effect on the next click rather than
  in thirty days. The cookie is `HttpOnly` and `SameSite=Strict` and deliberately
  **without** `Secure`, because homelab installs are usually plain HTTP on the LAN.
  The version prefix retires the pre-accounts cookie format, which carried no
  identity at all: an old cookie is rejected with a plain "sign in again", which is
  the correct outcome and the only one available.
- **API key** — a generated key sent as `X-API-Key` or `Authorization: Bearer`, for
  scripts, cron jobs, and Home Assistant. It belongs to no account and is an
  administrator outright; that is a deliberate decision, written out in SECURITY.md
  rather than left implicit.

The router is the whole authorization policy: `view`, `oper` and `adm` wrap a
handler with a role floor, and every check compares `Role.Rank()` rather than naming
roles, so a route added later is a one-word decision. Readings sit at viewer, acting
on an outage sits at operator, and changing the configuration, the API key or the
accounts sits at admin. A handler that only ever touches the caller's own account —
the interface preferences, the password, the second factor, the tour — sits at
viewer, because it is nobody else's business.

Two-factor authentication is per account and optional. `totp_secret` is written as
soon as enrolment starts, and `totp_enabled` stays false until a code is confirmed,
so an interrupted enrolment is a stored secret that grants nothing. A successful
code records its time step in `totp_step`; a code from a step already spent is
refused as a replay rather than accepted a second time. Eight one-time recovery
codes cover a lost device, shown once and stored as hashes. Signing in is one request
when the factor is off and two when it is on: the password step answers with a
short-lived signed challenge token, and the code step trades it for a session.

Cookie-authenticated writes are additionally checked against `Origin` (falling
back to `Referer`) plus `SameSite=Strict`, which together stop a page on another
site from driving the API. Requests with no `Origin` at all — curl, scripts —
pass, because they carry no ambient cookie and are already covered by the API key.

Passwords are bcrypt at cost 12. Login attempts are limited to five per minute in
memory, and the second-factor step has a limiter of its own keyed by account rather
than by address: the password is already known by then, and the six digits are what
is being guessed at. `/api/setup` runs once: a fresh database has no accounts, the
dashboard shows a setup screen, and the endpoint refuses with a `409` once an
account exists. A password in `WEATHERGLASS_ADMIN_PASSWORD` creates the first
account — named `admin`, with the admin role — and is then ignored.

Secrets never leave the server. `internal/api/mask.go` replaces every field the
channel definition marks `Secret: true` with a marker on the way out, and a
request that sends the marker back keeps the stored value instead of overwriting
it with the literal marker. The Telegram sender strips the token from error
messages before they reach a log line or the UI.

## Search

`internal/store/search.go` answers one query with six SQL statements in a single
transaction, one per kind — sensors, sites, channels, incidents, raw probe results,
and accounts — and returns them flattened to `{kind, ref, title, sub, state, at}`
plus a `truncated` flag. The handler decides who may see what: the `user` query is
skipped entirely below administrator, and the check is in the handler rather than in
the interface, because an interface is not where an authorization decision belongs.

**`instr(lower(col), lower(?)) > 0`, never `LIKE`.** A search term typed by a person
is not a pattern, and a `%` has to mean a percent sign. `LIKE` would need its own
escaping function *and* an `ESCAPE` clause in each of the six statements — six
places to forget it, and one forgotten one turns a search into an unindexed scan
over everything that also returns the wrong rows. `instr` has no pattern syntax, so
the failure cannot occur. Both sides are lowered because SQLite only folds case for
ASCII otherwise. The scan is the one `LIKE '%…%'` would pay anyway, and none of
these columns has an index.

Two things bound the cost rather than the accuracy. The raw-results query is fenced
by the retention window from `Store.Retention()`, because it is the only table that
grows without limit — without it, every keystroke would walk the whole history. And
each kind is capped at twenty rows (fifty at most) with an overall ceiling of two
hundred; the answer says whether it had to cut, and there is no cursor, because the
only caller is a search field and the right response to "too many" is a longer word.

**Credentials are not searchable.** A sensor's `options` and a channel's `config`
hold `bearer_token`, `basic_auth_pass`, `community`, bot tokens and webhook URLs,
and `password_hash`, `totp_secret` and `code_hash` are left alone on the account
side. A channel's config would only ever match on its masked value, which is a hit
that tells nobody anything. The rule is written where the queries are, so the next
kind added meets it.

`internal/api/search.go` is thin on purpose: it reads `q`, refuses anything past
sixty-four characters, and calls the store once. A query under the two-character
minimum is **not** an error — somebody typing one letter at a time has done nothing
wrong, and answering their first keystroke with a red toast teaches them to stop
typing. There is deliberately no `limit` parameter; the store's per-kind bound is
the right one for the one caller there is.

## Logging and diagnostics

Every request produces exactly one log line: `info` as `msg="http request"` with the
method, path, status and duration — or, when the status is `500` or worse, `error` as
`msg="http request failed"` with the same fields plus `message` (what the client was
told) and `error` (what actually went wrong, which the client never sees). The
invariant is enforced by the response writer rather than by discipline at the call
sites: the recorder the middleware already wraps around every handler carries the
failure up from wherever it was written, so a handler added later that forgets to log
is still logged. Two things bypass it deliberately — a panic, which `net/http` recovers
into its own `ErrorLog`, and a background error that does not spoil the response (a
reconcile failing after an already-successful `201`), which is logged where it happens.
A request that took longer than ten seconds logs a separate `warn` on top, because
"slow" and "failed" are different statements.

`internal/doctor` is the other half of that: what the log says about one request, doctor
says about the whole installation. Its two rules shape the code — it changes nothing
and it sends no alert — which is why it opens the database through `store.OpenReadOnly`
with a DSN of its own. A `journal_mode` pragma is a write, so reusing `store.Open`'s DSN
would make a read-only open fail outright rather than merely refuse to write. The same
reasoning produced `config.Resolve` beside `config.Load`: a diagnostic has to keep
naming the data directory precisely when the configuration file is the thing that is
broken, and `Load`'s job is to refuse to start.

## Live updates

`internal/event` is a tiny pub/sub broker: publishers never block, and a
subscriber whose buffer is full drops the event rather than stalling the
scheduler. `/api/events` forwards the stream to the browser as SSE. The dashboard
is eventually consistent by design — every client also fetches
`/api/summary` when it connects — so a dropped event costs a stale row for a
moment, never a wrong number.

### One card, not the whole board

The stream carries two different things, and the client reads them apart.

A `result` event is **a number**. The dashboard replaces that one sensor's card —
status dot, latency, message, sparkline, heartbeat — and leaves the rest of the page
alone. Everything else on screen is a thing somebody may be in the middle of: an
open tree branch, a map panned to a corner, a form with the cursor in it, a detail
window being read. Rebuilding the board on every probe, which is what it used to do
once a minute, closed the branch, moved the map back and took the focus away.

A `status` or `incident` event is **a state**, and additionally moves the dot and
the open outage and marks the card briefly. A `sensor`, `channel` or `settings`
event says a *list* changed shape, and those still take the debounced refetch —
no patch can express a row appearing.

Two consequences worth knowing before changing this code. The card builder for the
current view is what draws the replacement, so every layout needs a way to build
**one** node — which is why `layouts.js` exports `treeRow` and `tile` rather than
keeping them private. And the per-account preference `ui.live` (`on` by default, in
`model.PrefLive`) decides whether the browser subscribes at all: with it off the
stream is never opened, which is what makes it a real off switch rather than a
muted one.

## Backing up and restoring

`internal/store/backup.go` writes the installation out as one JSON document and
reads one back in. Three decisions in it are load-bearing.

**Sensors, sites and channels travel by name.** A name is what a sensor is in this
program — the configuration file can only name things and never know a database
id — so a file that referred to a sensor by id would restore a different sensor on
a different installation, silently. The one place ids do travel is inside the
document itself, where they are re-pointed on the way in, and `sqlite_sequence` is
pulled up to the highest restored id afterwards so the next inserted row does not
collide with one that came back.

**Every table is a pointer.** `BackupDoc` holds `*[]T`, so an absent table and an
empty one are different things. A backup "without history" simply does not mention
`incidents` or `results_*`, and a restore therefore leaves those exactly where they
are — a configuration rollback must not wipe history. Inside a table, rows are
replaced rather than merged, because restoring means putting back.

**Secrets are written in the clear.** Masking them would produce a file that
restores a channel whose token reads `__stored__`, which imports cleanly and cannot
send anything — and nobody would find out until the first outage. The unmasking is
confined to this one path, stated in `SECURITY.md`, and never logged: the export
records row counts per table and nothing else.

The restore validates the format marker and the schema version before writing a
row, and takes a byte-exact `VACUUM INTO` safety copy first; if that copy fails, the
restore stops. The whole thing runs in one transaction, which with
`MaxOpenConns(1)` means a concurrent read sees all of the old state or all of the
new one. Afterwards `Scheduler.Reconcile` brings the running loops in line with the
restored sensors, and the in-memory caches that live on the `Server` rather than in
the database — the API key, the session secret, the cached tile origin — are
reloaded, or the old credentials would stay valid while the database already knows
different ones.

## Frontend

No build step, no framework, no CDN. `internal/web/assets` holds `index.html` and
a dozen-odd plain ES modules — `app.js` for the shell and the routes, `layouts.js`
for the five views, `charts.js` for the canvas drawing, `map.js`, `outline.js`,
`mapborders.js`, `maplabels.js`, `search.js`, `widgets.js`, `users.js`, `user.js`,
`tour.js`, `tooltip.js`, `password.js`, `i18n.js` and `style.css` — embedded with
`embed.FS`. Editing a `.js` file and reloading the page is the whole development
loop, and the image ships exactly the bytes in the repository. The map's four data
files (`world.js`, `borders.js`, `labels.js`, `regions.js`) sit in the same
directory and are the only files there that are generated rather than written —
they come from `cmd/worldgen`, are committed, and are imported as ES modules and by
nothing else.

The map is four modules rather than one. `outline.js` owns the projection — the one
place `lonToX`/`latToY` and the base zoom live — and draws the coastlines.
`mapborders.js` and `maplabels.js` are the two layers over it, imported
*dynamically* by `map.js` the first time a map is built, so that a page which never
shows a map never downloads a megabyte of borders. They take the projection from
`outline.js` rather than copying it, because two projections that each say what the
world's width is are two projections that will disagree. `mapborders.js` is its own
`<svg class="map-borders">` rather than a second path inside `.map-outline`, which
is the coastline and is filled like land. `maplabels.js` draws in *screen*
coordinates — absolutely positioned spans, not SVG text — because a name is a label
in the interface rather than a shape in the world, and because overlapping names
have to be measured against each other to be dropped, which is a question about
pixels.

`search.js` is the leaf module the top bar's search field is built from: it
`mount`s on the input, takes the few functions it needs from the app as arguments
rather than importing `app.js`, and answers at once from the state the page already
holds — sensors, sites, channels, accounts and the settings panels — while the
incidents and the probe messages come back from `/api/search`. It is written that
way to keep one rule intact: a module may import `i18n.js`, `charts.js` and
`tooltip.js`, but never `app.js`, so the shell cannot grow a cycle through its own
parts.

`widgets.js` is the dashboard header. It is a registry — an id, an icon and a
build function per widget — plus the code that arranges the strip: `mount` on the
container, `setWidgets` for a stored list, `refresh` for new numbers. What is on
the strip is one account setting, `ui.widgets`, and the order is the value. Two
things there are deliberately not like the rest. The list is read off
`#summary > [data-widget]` rather than off every child, so that a control inside
the strip cannot be saved as a widget that does not exist. And the map tile is
mounted only once its host has a size, then only has its pins recoloured, because
`map.js` measures its host when it is mounted and is never told to measure again —
a map mounted behind a hidden view would keep the fallback size for the session,
and a map rebuilt on every poll would drop the viewport under the reader. The one
thing this file asks of the shell is where it is: whether the strip is being
arranged decides whether the layout switcher in the top bar is shown, and `app.js`
owns that attribute, so the strip reports the change through `onArranging` and the
shell reads `isArranging()` when the route changes under it.

`app.js` renders from state and refetches on SSE events; `charts.js` draws the
sparklines and response-time charts as hand-written SVG without a charting library.
Charts are drawn for the theme in use and redrawn when it changes, so they stay
readable in high-contrast and dark modes. The theme is the signed-in account's
own preference, so the same page can render differently for two people at once.
`users.js` is the administrator's account editor, and `tour.js` is the guided tour
written as data: a list of steps that name a target, a placement, a title and a
body, and may declare that they belong to a mode, a topic or a view.

`i18n.js` is a message catalogue with a language list. English is the only
dictionary so far; adding a language means adding a dictionary and one entry,
with no changes elsewhere, because the markup never contains user-facing strings.

## Packaging

`Dockerfile` is a two-stage build. The builder is pinned to
`--platform=$BUILDPLATFORM` and uses `TARGETOS`/`TARGETARCH`, so a multi-arch
build cross-compiles rather than emulating: the arm64 image for a Pi builds at
native speed. The runtime stage is
`gcr.io/distroless/static-debian12:nonroot`, with `USER 0:0` on top of it — no
shell, no package manager. It starts as root for one reason: the data directory
is a bind mount on the platforms this is installed on, created by the platform as
root, and only root can change who owns a file. `internal/privdrop` hands the
directory to `PUID:PGID`, becomes that user and verifies it, all before the
database is opened; see "Privileges" below. Because there is no shell, the image's
`HEALTHCHECK` runs the binary itself (`weatherglass healthcheck`), which makes a
`GET /healthz` and exits non-zero on failure.

`docker buildx bake`-style multi-arch publishing and the release archives are
driven from `.github/workflows/release.yml`, with the version, commit, and build
date stamped into `internal/model` via `-ldflags`, so `weatherglass version` and
`/api/version` report exactly which build is running.

## Privileges

`internal/privdrop` is the one place in the program that runs as root, and it runs there
for milliseconds. Its job is the folder rather than the process: a bind mount keeps the
ownership of the host folder, the platforms this ships to create that folder as root, and
only root can hand it to somebody else. So the container starts as root, sets `UMASK`,
creates the data directory if it is missing, `lchown`s it and its contents to `PUID:PGID`
(`lchown`, and never through a symlink), and then becomes that user with
`setgroups`/`setgid`/`setuid`, verifying with `geteuid` afterwards and refusing to
continue if it is still root. Everything after the call in `cmd/weatherglass` — the store,
the scheduler, the API — is unprivileged, and a privileged `setuid` sets the saved uid, so
nothing can go back.

The Linux half lives behind a build tag; elsewhere `Apply` is a refusal, not a no-op, when
it is asked to run as root. Capabilities are per-thread in the kernel, so every
`prctl`/`capset` goes through `syscall.AllThreadsSyscall`; Go's `setuid` already does.
`CAP_NET_RAW` is carried across the drop on purpose — it is what a ping sensor opens a raw
socket with — and if that carry fails the daemon logs which capability was lost and keeps
running, because a ping sensor that falls back is a smaller problem than a watchdog that
does not start.

`doctor` deliberately does not call it. A diagnostic tool that quietly repairs the folder
it was asked to report on would report success for a problem it has just removed; it reads
the settings for their values only, and prints both identities — doctor's own and the
daemon's — because they are not always the same number.

## Testing

`internal/store`, `internal/config`, `internal/sensor`, `internal/notify`,
`internal/scheduler`, `internal/totp`, `internal/privdrop`, and `internal/api` all have
tests; CI runs them with `-race`. `privdrop`'s tests never change the identity of the test
process: they drive the sequence through a fake syscall layer and assert the exact order
of the calls, because the order is the part that a later refactor would break silently. The sensor and notify tests exercise real sockets — a local
`httptest` server, a real TCP listener, a DNS stub built with `dnsmessage` — rather
than mocking the network, because the interesting failures are in the interaction
with it. The API tests drive a real `httptest` server with its own store and
scheduler, and walk a request past all three roles to pin the ordering between
them; they also check that a role change, a disable and a delete reach a session
that is already open, because that is the property the per-request account read
exists to provide. The scheduler tests drive the state machine a step at a time
against a real store, which keeps the incident-lifecycle assertions deterministic
instead of dependent on a ticker.

The suite is not decoration. It has found, before any of them reached a user: a
template-source leak in `notify` that put raw Go template text into an alert, two
parsing gaps in `config` (integer intervals and day/week retention), a
nil-context panic in `Reconcile`, a partial update that detached every alert
channel, and a recovery alert that reported the outage length as zero because the
start time was read after the status timestamp had been overwritten.
