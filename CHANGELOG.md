# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

What each release holds, one line each. Every version below is written out in
full; the release page for a tag carries its own section of this file, so what is
here and what the release says are the same document.

| Version | Released | In one line |
| --- | --- | --- |
| [Unreleased] | — | Nothing yet |
| [0.2.3] | 2026-10-20 | Six more sensor types, a grouped sensor chooser with tag settings, escalation, a YAML export, Gotify as a seventh channel, and example pictures and maintenance windows on the first dashboard |
| [0.2.2] | 2026-10-18 | A symbol on every sensor, place and group — ours, yours, or searched for online — and one set of measurements for the whole dashboard |
| [0.2.1] | 2026-10-16 | A sensor under maintenance says so |
| [0.2.0] | 2026-10-16 | Maintenance windows, a second interface language, and the container as the only thing that ships |
| [0.1.0] | 2026-10-12 | The first release: sensors, history, incidents, alerts, accounts, and the dashboard |

## [Unreleased]

### Added

- **SNMP v3.** User name, security level, SHA authentication and AES privacy, on
  top of the v1 and v2c the sensor already spoke. The form shows only the fields
  the chosen level uses; the two v3 passphrases are masked in the API like a
  community string. v3 with `authPriv` is the answer to v2c's clear-text community,
  and the security notes now say so.
- **Generated credentials.** The community, the two v3 passphrases, an HTTP basic
  auth password and a generic webhook's HMAC secret can be made from the field:
  128 random bits as hex, revealed and copied, so the same value can be stored on
  the device before the sensor or channel is saved.
- **A sensor catalogue with device templates.** The type field opens a searchable
  catalogue of every sensor, and beside the individual ones there is a template
  library: QNAP, Synology, UniFi, APC UPS, MikroTik, pfSense/OPNsense, Proxmox,
  TrueNAS, printers and a generic HOST-RESOURCES set. The templates are filtered
  by vendor, system and what they watch, and each one names the MIB or community
  template its OIDs were checked against. Choosing one asks for the device's
  address and SNMP credentials, and **Check device** makes one real request before
  anything is created — the box answers with its own description, so a wrong
  address, community or vendor is visible first. Then the checks are ticked and
  created as ordinary sensors in one group, all of them or none, in one
  transaction; they stay editable, because vendor OIDs move between firmware
  versions.
- **Application templates and a deeper MOS integration.** Templates are no
  longer SNMP-only: a check can carry its own HTTP/TCP/ping target pattern, the
  template can ask for an API token, and checks can read a JSON value with
  `json_path` (optionally selecting one item from a list) and judge it with
  `json_expect`, `json_min` or `json_max`. The catalogue grows with Home
  Assistant, Pi-hole, AdGuard Home, Grafana, Portainer, Jellyfin, Uptime Kuma,
  Node-RED, Mosquitto and Shelly. **MOS NAS OS** goes further: its template reads
  CPU load, CPU temperature and memory, and **Check device** then asks MOS for the
  disks, pools, Docker and LXC containers, VMs and hardware sensors it actually
  has, offering each as a tickable sensor. The template dialog now opens on the
  template catalogue; single sensor types are one tab away.
- **New sensor starts at the catalogue, and a device carries across templates.**
  The button opens the template catalogue instead of an empty sensor form; the
  single sensor types are one tab away. The address, name, group and credentials
  entered for one template are reused by the next, so a device can receive several
  sets of sensors without typing its address again.
- **A group can be deleted with its sensors.** The group dialog offers to keep the
  sensors and remove only the label, delete selected sensors, or delete all of
  them. Sensors declared in the configuration file are refused, because only the
  file may remove those.
- **API-key application templates.** Sonarr, Radarr, Lidarr, Prowlarr, Bazarr,
  Overseerr, Jellyseerr, the Portainer API and Paperless-ngx: each asks for its
  API key once and checks its own status endpoint. The HTTP sensor sends the key in
  a configurable header (`X-Api-Key`, `X-API-Key`, or `Authorization` with a
  `Token ` prefix), masks it like every credential, and the *Arr templates verify
  the `appName` the application reports.
- **The whole Ubiquiti line.** Access points, switches, gateways and Dream
  Machines, Protect, Access and Talk, plus airMAX/airFiber and EdgeMAX. Each has
  uptime and reachability; airMAX also checks board temperature. SNMP still has to
  be enabled for the device in the controller.
- **The template cards sort alphabetically and can be arranged.** Drag one
  template onto another to put it in front; the order is remembered on your
  account. The **A–Z** button next to the filters clears it and returns to the
  translation's alphabetical order.
- **More homelab templates.** Plex, Emby, Tautulli, SABnzbd, qBittorrent,
  Transmission, Nextcloud, Immich, Vaultwarden, Gitea, Syncthing, Netdata,
  Prometheus, InfluxDB, Traefik, Nginx Proxy Manager, ESPHome, Tasmota,
  Zigbee2MQTT, Frigate, OctoPrint, Homebridge, Audiobookshelf, Navidrome, Kavita,
  OpenMediaVault and Unraid — 27 more entries, each with its web/health endpoint
  and reachability.
- **A network scan that suggests sensors.** An administrator gives a private
  IPv4 range of at most 256 addresses; ProbeGlass dials a fixed list of the ports
  its templates already know, optionally identifies a device over SNMP, and offers
  ping, HTTP, TLS and SNMP checks for what answered — naming the matching template
  where it recognizes the product. Everything starts ticked; choose a group and
  create the selected sensors in one transaction. Only private, loopback and
  link-local ranges can be scanned, and the scan credentials are not stored.
- **A `/metrics` endpoint for Prometheus.** Sensor status, the 24-hour, 7-day and
  30-day uptime ratios, the last latency and the open incidents, in the Prometheus
  text format, so a homelab that already runs a monitoring stack can scrape
  ProbeGlass instead of repeating its checks. The endpoint is off until a token is
  generated under **Settings → Monitoring → Metrics**, and that token is read-only
  and separate from the API key: a scraper gets the numbers and no write route.
  Rotating it closes the old one; removing it makes `/metrics` answer `404`.

### Fixed

- **Session cookies are `Secure` when the dashboard is reached over HTTPS.** The
  flag follows the connection: a direct TLS request or a reverse proxy sending
  `X-Forwarded-Proto: https` marks the cookie `Secure`. A homelab reached over
  plain HTTP keeps the old behaviour, because a browser refuses to send a
  `Secure` cookie on an HTTP origin and nobody could sign in.
- **The OpenStreetMap attribution link checks the tile server's hostname**, not
  whether `openstreetmap.org` appears anywhere in the address, so a look-alike
  domain no longer gets the credit link.
- **The Test button stops a DNS lookup it has given up on.** SNMP hosts are now
  resolved through the probe's context, and a test is capped at ten seconds, so a
  `.local` name with no mDNS answer no longer keeps asking the network after the
  form has moved on.
- **The UniFi template asks for the standard uptime OID**, because not every
  firmware answers UniFi's private one. Reachability is the second check.
- **MOS Docker checks are status-only**, and discovery skips removable/USB disks
  such as a boot stick: they answer neither temperature nor SMART, so a sensor for
  them could only be red. A temperature check is created only when the disk reports
  a temperature.
- **Sensors created from a template wear its icon**, and MOS's discovered checks
  carry their own: disk, CPU, thermometer, container, monitor, database. Templates
  created from the dashboard icon set look like what they monitor.
- **The template catalogue carries the vendor marks.** QNAP, Synology, UniFi,
  APC, MikroTik, pfSense, Proxmox, TrueNAS, MOS, Home Assistant, Pi-hole,
  AdGuard, Grafana, Portainer, Jellyfin, Uptime Kuma, Node-RED, Mosquitto and
  Shelly are drawn with their bundled brand icon (Dashboard Icons, Apache-2.0).
  Sensors created from a template keep that icon unless the check brings its own.
- **The template dialog closes after creating its sensors.** A new sensor goes
  back to the dashboard with the new cards on it, rather than leaving the
  catalogue open over a form that is no longer being filled.
- **The template filters are one button again.** The search box and a single
  **Filter** button; vendor, system and "watches" open in a small panel, and what
  is chosen is shown as removable chips under the bar. The cards themselves say
  the vendor, product and system on one line instead of scattering tag pills under
  every description; the full tag list stays in the template's detail view.

## [0.2.3] - 2026-10-20

### Added

- **The sensor editor stops being a stack of dropdowns.** The type is a button that
  opens a grouped, searchable chooser — **Web**, **Network**, **Services** and
  **Infrastructure** — with a symbol and the shape of each target on every tile. The
  per-type choices below it (method, record type, SNMP version, health, and every
  yes/no) are chips, so a value is one click and visible. "Leave it at the default"
  is a chip of its own, not an empty field somebody has to work out how to clear.
  The group and tag fields are the same picker. Both carry a **New…** entry that
  opens the editor Settings uses, so a group or a tag can be named and given a
  symbol and colour before the first sensor carries it; tags are a multi-select,
  and their chips wear the colour and symbol from Settings. Tags become settings
  of their own — a table, an API, a Settings panel — while remaining the words in
  a sensor's tag list, exactly as groups are. The place picker groups sites by
  kind and still carries the entry that makes a new one; the **Depends on** picker
  lists the other sensors with their current status beside them, a down one first,
  so the choice is made on what is happening rather than on the alphabet.

- **Groups are arranged by dragging.** The group rows in Settings → Monitoring, or
  the group headings on the dashboard itself, can be dragged into the order the
  dashboard should show them in; the whole order is written at once, so a drop
  cannot leave two rows sharing a number. The position number in the editor remains
  as the keyboard path.

- **Escalation.** A sensor can carry a second list of channels and a delay. The
  ordinary channels hear about the outage first; if it is still open and nobody has
  acknowledged it after `escalate_after`, the escalation channels are told once.
  Acknowledgement stops it, and the incident records that the message went out, so a
  restart cannot send it twice. It is set per sensor in the form, and as
  `escalate_after`/`escalate_channels` in the configuration file.

- **The configuration file can be written from the dashboard.** The Backup panel's
  *Configuration file* button writes the sites, channels and sensors as the
  `probeglass.yaml` the daemon already reads, so a setup made in the browser can
  become the versioned one. Accounts, incidents and history stay out — that is what
  the JSON backup is for — and example data is left out unless it is asked for.

- **An open chooser stays inside the window.** The panels are fixed to the viewport
  and clamped to it rather than absolutely placed inside the dialog: a long
  dependency list or a wide panel no longer adds a scrollbar to the form, and a
  panel near an edge opens on the side with room.

- **The static demo's built-in symbols are drawn again.** They were requested from
  the domain root, which is right for the binary and wrong for the demo served from
  a subdirectory; the path is now resolved against the module that defines it.

- **Six more sensor types.** Each answers a question the existing probes could only
  approximate, and each is the smallest protocol exchange that answers it:

  - **SSH banner** (`ssh`) reads the identification line an SSH server sends before
    any handshake — `SSH-2.0-OpenSSH_9.6p1` — so "sshd is answering" is told apart
    from "something is listening on 22".
  - **SMTP greeting** (`smtp`) reads the `220` line an SMTP server sends on connect;
    **IMAP greeting** (`imap`) reads `* OK` or `* PREAUTH`, and refuses `* BYE`.
    Both can speak implicit TLS, so port 465 and 993 are covered too.
  - **UDP port** (`udp`) sends one datagram and, when told what to look for, waits
    for a reply containing it. Without an expectation it says plainly that the
    packet left — UDP has no handshake, and a sent packet is not proof of a listener.
  - **HTTP redirect chain** (`redirect`) walks redirects itself and reports the
    chain and the address it ends on, with an optional limit, an expected final URL
    and the status codes the last page may carry.
  - **ICMP hop** (`hop`) sends one echo with a chosen TTL and names what answered:
    the router a Time Exceeded came from, or the destination itself when the packet
    got that far. It needs `CAP_NET_RAW`, as ping does, and says so when it cannot
    have it.

- **Gotify is a channel.** The self-hosted push server a homelab often already runs:
  the base address and an app token from one of its applications, and the message
  arrives in its phone app the way an ntfy one does. It is the seventh channel, and
  the one the roadmap had been carrying as a suggestion.

  The token travels in the `X-Gotify-Key` header rather than in the query string the
  server also accepts, because a URL is the part of a request that ends up in proxy
  logs. An unset priority follows the event — 8 for a down alert, 6 for a reminder, 4
  for a recovery — and a priority set in the form is used for every event, 0
  included: "this recovery should not make a sound" is a real choice, and a zero that
  meant "unset" could not express it.

- **The example data uploads four pictures, so both kinds of symbol are on the first
  dashboard anybody sees.** Cloudflare, Debian, Docker and TrueNAS, worn by the sensors
  that are about them: the public resolver, the package mirror, the NAS's web interface
  and its container. A fresh installation starts the scheduler with them, and a
  `seed --demo` gives the rest of the set the same treatment.

  The demo data set is what the documentation's pictures and the website's demo are
  drawn from, and until now it could only use the drawings that ship in the binary: an
  uploaded icon is a row in somebody's database, and a dataset that ships inside the
  binary had no database to name one in. It has one now — seeding puts the pictures in
  the icons table first and writes `upload:<id>` into the sensors that wear them.

  They are brand marks of their projects, taken from Dashboard Icons (Apache-2.0) and
  already reduced to the size and weight this program keeps an icon at. Removing the
  example data leaves the pictures in the picker: an icon is a picture and not a
  sensor, and one of them may already be worn by something somebody made.

  The website's demo is a folder of static files and draws an uploaded icon with
  `<img src="/api/icons/7">` — a route only a server can answer. There the recording
  uses the drawing each picture stands for instead, through `demo.RecordBuiltinIcons`.

- **The example data arranges two maintenance windows, so the feature is visible
  without setting one up.** One is in force on *Example website* — the badge on its
  card is what a window looks like from the dashboard — and one is weekly, Sunday
  02:00 to 04:00 in Berlin, which is what the settings list shows. Both are marked as
  example data and go with the rest of it, including the weekly one: it names a group
  rather than a sensor, so it has no row to cascade from and is removed by that mark.

  And a card that carried two pills beside its name overflowed its own border. The
  cards are a grid whose one column sized to its widest row, so a longer heading set
  the width and the name had nothing left to give up. That column may shrink now, and
  a name that does not fit ends in an ellipsis with the whole of it in the tooltip.

## [0.2.2] - 2026-10-18

### Added

- **Dependencies between sensors — a power cut is one outage, not thirty.** A sensor
  can name another as the one it sits behind, and while that one is down the sensor is
  **excused**: it opens no incident of its own, sends no alert, and the checks it fails
  in the meantime do not count against its uptime. Its status still says what the probe
  found, and its card carries a badge saying the downtime is a consequence rather than
  an outage of its own.

  - **Chains work**: router → switch → the things on the switch. Circles and a parent
    that does not exist are refused when the sensor is saved.
  - **Only a confirmed outage counts.** A parent that is merely flaky — one lost ping —
    excuses nothing, or a single dropped packet on the router would quietly stop thirty
    sensors from alerting.
  - **The chain is walked, not just the first link**, so a sensor two levels down is
    excused by the same router.
  - A sensor behind a parent that recovers while it is still failing **earns its own
    incident back**: it uses up its retries again and the new outage is dated from
    after the parent came back.
  - Set in the sensor editor, where the picker leaves out the sensor being edited, and
    in the configuration file as `depends_on`. A file may declare them in any order.

  This is the same mechanism as a maintenance window and not a second one. A window is
  something a person said in advance; a parent that is down is something that happened
  to the sensor rather than by it, and both mean the same thing to the arithmetic. So
  the marker became the general one: `results.maint` is `results.excused` now, with
  `excused_reason` beside it, and the rollup counts are named for what they are. The
  boundary handling is shared, which is most of why this is not a parallel copy of the
  window feature.

- **A symbol on every sensor, place and group — one of ours, or a picture of your own.**
  A sensor, a site and a group's heading can each carry an icon, drawn beside the name
  in every view: the cards, the list, the tree and the wall, and in the heading of the
  sensor's own window. In the sensor editor the symbol stands in the name row itself,
  as a button the size of the field: choosing one is a click on the picture that will
  be drawn, not a trip to a field called "Icon" further down the form.

  - **Two kinds of symbol.** Twenty-six drawings ship inside the binary — router,
    switch, server, NAS, container, cloud, printer, camera — and they follow the theme,
    so one file is right in both themes and under all six accents. Beyond those, an
    image can be uploaded: PNG, JPEG or GIF, at most 64 KiB and 512 pixels a side.
  - **Uploaded images live in the database, not on disk.** "One SQLite file, and
    copying it is the whole backup" is a promise this project makes in three places,
    and an icon in a directory beside the database would quietly make it false — the
    copy would come back without them. They are small: a hundred at the cap are six
    megabytes.
  - **No SVG, and that is not a preference.** An SVG is not an image but a document:
    it may carry script and external references, and it would be served from this
    dashboard's own origin, where that script would run with the session cookie of
    whoever looked at it. What an upload may be is decided by reading the file, never
    by its name or the type the browser claimed for it.
  - **An image that something is still wearing cannot just disappear.** Deleting one
    is refused while sensors, sites or groups point at it, and the refusal says how
    many. Saying "clear" as well takes it off them and deletes it in the same
    transaction.
  - **Groups have settings, and are still not rows.** A group exists because somebody
    typed its name on a sensor: no foreign key, no create step, and nothing to keep in
    step when a sensor is renamed. What is stored *about* a group is its symbol, the
    colour of its heading and its place in the order. Forgetting a group removes those
    and leaves every sensor exactly where it was.
  - **A group's colour is one of six names, never a hex value**, for the reason the
    accent setting is one: every colour in this interface exists twice, once per
    theme, and a stored `#f59e0b` would be the dark theme's amber drawn on somebody's
    white page.
  - **Built-in drawings are drawings and nothing else.** They are the files that ship
    in the binary and are served from the dashboard's own origin, so a test holds every
    one of them to that: no `<script>`, no `onload`, no external reference.
  - The configuration file takes both, as `icon: builtin:router` on a sensor or a site.
    It has to be able to: a sensor the file declares is written from the file whole on
    every start, so a column the file could not name would be cleared each time the
    daemon came up. `probeglass doctor` checks the same references, because a file
    doctor accepts has to be a file serve applies.
  - The backup carries all of it — the columns, the group settings, and the uploaded
    images with their bytes. The image id is the one row in that document that keeps
    its identity through a restore: a sensor's icon column holds `upload:7` as plain
    text with no foreign key behind it, so an image that came back under a different
    number would leave every reference pointing at whatever else landed there.
  - The four example sensors a fresh installation starts with carry a symbol, and so
    does the demo data set, so the feature is on the first dashboard anybody looks at
    rather than in a settings page nobody opened.

  Sensors and sites got a column each and groups a small table of their own; both are
  migration 9 and apply themselves on the next start. A group was deliberately *not*
  turned into a row with sensors pointing at it: that would make creating a group a
  step of its own and reach into the configuration file, the backup, the example data
  and every delete path, and a heading somebody typed is not worth that.

- **The picker searches [Dashboard Icons](https://dashboardicons.com/) when the symbol
  you want is not one of the twenty-six.** A field at the top of the picker takes a
  name, and the results come back with their pictures beside them.

  Choosing one **imports** it: the picture is downloaded once and stored as an ordinary
  uploaded icon — same table, same limits, same backup. That is the whole design and it
  is deliberate. Drawing it from the CDN would put somebody else's server in the path
  of every page load and take the image out of the one SQLite file a backup copies, so
  what is taken from the catalogue is a picture, once, and nothing after that depends
  on it being reachable.

  - **The server asks, the browser never does.** The catalogue's index is fetched and
    cached in memory by the daemon and revalidated with a conditional request on every
    search, so an icon added upstream is found by the search looking for it and the
    usual search costs an empty response rather than 1.2 MB. The previews are served
    through this installation's own route, which is also why the content policy still
    allows images from this origin and nowhere else.
  - **A container with no route out says so.** The answer to a search carries an
    offline flag rather than failing, and the field says the catalogue cannot be
    reached and points at the site. The built-in drawings and everything already
    uploaded keep working.
  - **Only PNGs are taken**, and they go through the same decode an upload does — no
    SVG, ever, and a picture that would be refused as an upload is refused here too.
  - **A picture larger than an icon may be is made to fit, not refused.** The
    catalogue's logos are not all 512 square — a wide one is 623×512, one is
    1123×512 — and the limit that keeps uploads small would otherwise turn a search
    result into an error. An imported picture is reduced to what the dashboard draws
    (an area average, never enlarged); a file somebody picked themselves is still
    refused when it is too big, because shrinking their own file without asking
    would be a surprise. The same goes for a picture over the byte limit: it is read
    from the CDN whole and reduced until it fits, rather than cut off at the icon
    limit — a file cut in half cannot be decoded at all, and the refusal then quotes
    the length of the cut instead of the picture's.

  The catalogue is the work of [Dashboard Icons](https://github.com/homarr-labs/dashboard-icons)
  (Apache-2.0); the pictures are the logos of the projects they belong to, and the
  picker credits both.

### Changed

- **The settings say which channel the installation is on.** Settings → Help and
  maintenance → About has a **Channel** row: **release** for a published version, **dev**
  for a build of the branch under development, **local** for one built from a working
  tree. `probeglass version` says the same thing below the build stamp.

  This is a fact a container cannot work out for itself. Docker does not tell a
  container which tag it was pulled by, so two containers on one machine — one following
  `latest` and one following `dev` — look identical from the inside: the same version,
  the same commit, the same date whenever both follow the same push. It comes from
  somebody running both and having no way to tell which was which.

- **`in_maintenance` is `excuse` on a sensor.** It was a boolean; it is now the reason
  — `maintenance` or `dependency`, empty when the status is the sensor's own business.
  One field rather than two overlapping ones, because a card carries one badge.

- **The `:dev` image says which build it is.** It reports the release it follows and
  how far past it — `0.2.1-4-g9c1f0ab` — instead of the literal `0.1.0-dev`, which was
  true when it was written and went on being said by every development build for two
  minor versions afterwards. The one image somebody pulls to test the newest work was
  the one image that could not say which work it was.
- **A tag with a hyphen in it is marked as a pre-release on GitHub.** `v0.3.0-rc.1`
  already left `latest` alone in the registry; the release page said nothing about it.

- **The dashboard has two scales instead of a number per rule.** Spacing is six steps
  — 2, 4, 8, 12, 16, 24, 32 — and type is five, and every rule in the stylesheet now
  names one of them.

  Nothing was wrong with any single rule; there were just fifteen distinct font sizes
  and eighteen distinct paddings, so two things that are the same kind of thing were
  never quite the same size. The steps are named for what they are for — `--text-2` is
  the name of a thing, `--text-4` the second sentence about it — so a new rule picks
  the step that matches its job rather than the one that looks right on its own. The
  three old names (`--pad`, `--pad-sm`, `--gap`) still work and mean the steps they
  always meant. One height, `--ctl-h`, is shared by everything that is pressed or
  typed into, so a button beside an input is the same box as the field next to it.

- **Group headings are the same heading in all four views.** The cards, the list, the
  tree and the wall now draw a group's symbol, its colour and how many sensors are in
  it — and how many of those are down — in one shape, from one rule. The card and list
  views used to have a plain lowercase word where the tree had a dressed heading.

- **A card reads as information first.** The head is one line — status, name, badges —
  and the row of actions underneath (move, refresh, pause, copy) is dimmed until the
  pointer is on the card or the keyboard is in it. The card's name carries a tooltip,
  so a name that does not fit is still readable.

- **What the header shows is a settings category of its own, and its list is in it
  once.** **Widgets** is the sixth category, holding the list of widgets and the way the
  **clock** is written. It used to be a corner of Monitoring, which had grown to four
  panels about things that are not what is being monitored.

  The list itself was drawn twice — in that corner and in the new category, both writing
  the same preference — so a setting that exists once appeared in two places, and which
  of the two was "the real one" was left to whoever found them. It is now in the Widgets
  category alone, and the Dashboard defaults panel under Monitoring is what it says it
  is: what this installation has stored and how long it keeps it.

  The **clock** writes its time the way the interface language does, and that is right
  until it is not: somebody who reads English but grew up with a 24-hour clock is not
  asking for `2:30 PM`. So there is a choice of **Automatic**, **24-hour** and
  **12-hour** beside the list. It is a per-account preference like the theme and it
  travels the same road, and the server refuses anything else — `clock=99` is a 400
  rather than a stored value nothing can draw.

- **Empty and loading states look like states, not like sentences.** A table with no
  rows, a list with nothing in it and a form with nothing to show all get the same
  box, where before some were an empty box and some were a muted sentence in the
  middle of the page. Loading does the same, in the shape of what is coming.

  The dashboard's "nothing here" was also one sentence for two situations: an
  installation with no sensors at all, and a search that filtered everything out. The
  second says what it is now, and names the search.

### Fixed

- **A toast no longer swallows the click under it.** Notifications are fixed to the
  bottom-right corner, which is exactly where a dialog's **Save** button is — so
  uploading an icon and then pressing Save, three seconds apart, did nothing at all,
  and the toast disappeared a moment later as though the click had never happened.
  Toasts take no pointer events now; they were never a thing to press.

- **The wall's heartbeat strip was a black bar.** The marks are painted green or red
  by `.heartbeat .beat`, and the strip was returned as a bare `<svg>`, which matched
  none of that and came out in the SVG default colour. It has been black since the wall
  was written: the empty case beside it had the wrapper, so the one place the colours
  were exercised was the one place the wrapper was not needed.

- **An excused sensor's row came apart in the tree.** The tree lays a row out as a
  five-column grid, and the status pill and its "maintenance" or "dependency" badge
  were two elements where the first column expected one — so everything after them
  moved a column along and the last wrapped onto a second line. A row now carries its
  pill and its badge in one box. This was the row somebody is looking at precisely when
  something is wrong.

## [0.2.1] - 2026-10-16

### Added

- **A sensor inside a maintenance window says so.** Its card and its row carry a
  violet **Maintenance** badge beside the status, the card is ringed in the same
  colour, and the tree, the wall and the map pick it up too. The status itself does
  not change: a box that is not answering is still red, because that is true — what
  the badge adds is the sentence somebody needs in order to decide whether to get up.

  This was a gap rather than a change of mind. 0.2.0's API published the flag from the
  day the feature landed and the dashboard used it in one place, the "running now" mark
  in the window list, and nowhere on a sensor — so a window did everything it promised
  and nothing on screen admitted it. Reported by the first person to use the feature.

### Fixed

- **Switching the language no longer races the settings page.** The page is rebuilt
  when the language changes, and the redraw was not waited for, so a control could be
  replaced under whoever was using it. The rebuild is awaited now.
- **Long sensor names are no longer broken mid-word.** The card's header wraps the
  badges onto a second line rather than squeezing the name until "Internet gateway"
  comes out as "Inter net gate way".

[0.2.1]: https://github.com/j000k3r/probeglass/compare/v0.2.0...v0.2.1

## [0.2.0] - 2026-10-16

### Added

- **Maintenance windows.** Tell the installation in advance that a box is allowed
  to be away — the nightly snapshot, the Saturday the disk gets swapped — and the
  checks that fail in that period open no incident, send no alert, and are left out
  of the uptime figure.

  - A window covers **one sensor or a whole group**, repeats **weekly on chosen
    days** or covers **one stretch of time**, and is created in Settings →
    Monitoring → Maintenance windows. A weekly window is read in the zone the
    person making it is in; the form fills that in from the browser, because a
    container has no idea where its operator lives.
  - **The probe keeps running.** A window is not a pause: the result is still
    recorded, so a box that comes back early shows up in its chart as having come
    back. What is suppressed is the claim that somebody needs to do something.
  - **The arithmetic follows the marker into the rollups.** A sample left out of
    today's figure has to stay left out of next month's too, and the raw rows are
    gone by then — so the count is carried into the hourly and daily tables by the
    rollup itself, in two columns rather than one, because a window is a period and
    not a promise and the box may come back early.
  - An outage that was **already open** when a window begins is closed with a note
    saying why, and nothing is sent — the message that opened it already went out.
    If the box stays away, a new outage opens once it has failed its way past the
    retries again, dated from after the window. A recovery inside a window is
    silent, because no "down" was ever sent.
  - A window applies **from the moment it is saved**. The checks already recorded
    are not rewritten, which is stated in the documentation rather than left to be
    discovered.

- **A second interface language, and the switch for it.** German joins English.

  Every word the dashboard draws now comes out of a message catalogue rather than
  out of the markup, so the next language is **one file and one entry in a list**,
  and no code at all:

  - `internal/web/assets/i18n.<code>.js` beside the English reference in
    `i18n.js`, holding the same keys in the same order with the same placeholders.
    A test compares the two key by key, in order, and compares both against the
    keys the interface actually asks for — in `index.html` and in every `t('…')`
    call in the scripts — so a missing word and a mistyped one both fail the build
    rather than reaching the screen.
  - The code in the two lists that already say what the interface offers: the Go
    whitelist that validates a stored preference, and the `available` list the
    picker is drawn from.

- **The switch**, in Settings and in the account menu, taking effect on the view
  you are already looking at rather than on the next page load. Each language is
  named in its own language — "Deutsch", not "German" — because the reader who
  needs that entry is the one who cannot read the rest of the list.

- **Dates, numbers and relative times follow it too.** `Intl` reads the browser's
  language rather than the document's, so every formatter is now handed the chosen
  one explicitly, and "5 min ago" under a card is a catalogue message like any
  other word.

### Changed

- **A stored language follows the account**, like the theme, the accent, the
  density and the layout, rather than the installation. An installation that has
  never chosen one behaves exactly as before and reads English.

### Removed

- **The binary releases for darwin, windows, freebsd and linux.** 0.1.0 shipped
  seven zip files; a release now ships the container image and nothing else. The
  archives promised a second way to run ProbeGlass that no document described,
  and two things would not have survived it: the `docker` sensor talks to the
  Docker Engine over a unix socket, which Windows does not have, and `doctor`
  tells you to run it with `docker exec`. What is gone is the publishing of other
  platforms, not the ability to compile — `make build` still builds for whatever
  machine you are on.

  The `checksums.txt` file and its Sigstore signature went with the archives,
  because there was nothing left to check a download of. The release workflow no
  longer requests an OIDC token at all. The image itself is not signed; if that
  is wanted it would be a small addition of its own.

### Notes

Three surfaces are deliberately still English, and are meant to stay that way:

- the **watchdog's own page**, which has to be readable in exactly the situation
  where the dashboard's scripts did not load;
- the **`doctor` findings**, which are the CLI's output;
- the **channel types and their setup recipes**, which the server sends as its own
  text.

The probe messages the server writes into the history — `HTTP 200 OK`, `no such
host` — are data rather than interface and read the same in every language.

## [0.1.0] - 2026-10-12

The first release: a working foundation that monitors, records, and alerts, with
the structure in place to grow. What follows is the whole of it — the feature set
it lands with, and every change made on the way to the tag.

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
- **Configuration** — YAML file, `PROBEGLASS_*` environment variables, and
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

- **The examples a fresh installation starts with stand at two places.** The four
  invented sensors carried a week of past but nowhere to be, so the map — the one
  screen a new installation had nothing at all to say about — was empty behind the
  button that draws it. They stand on the two places the demo data set uses, a
  "Homelab" and a rented server somewhere else, each with real coordinates, because
  a pin in the Gulf of Guinea is what a missing coordinate pair would look like and
  is the one thing on that map that reads as a bug. Both carry the example mark with
  the sensors, so the removal button takes them along rather than leaving two empty
  pins behind, and the panel that counts the example data counts them now: a place
  the removal would leave holding a sensor of your own stays, and the number on the
  button is that same number. The names and the coordinates are asked of the demo
  data set rather than copied from it, which matters the first time somebody loads
  that data after a fresh install: the loader leaves a place that is already there
  alone, so the two sets fold into one pin each instead of drawing a second one on
  top of the first. The four examples are split by where they stand — the two about
  this machine on the homelab, the two about something out there on the server — and
  the Sites table marks the example places as examples the way sensor cards
  already did.
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
- **`probeglass doctor`.** One command that says what is wrong with an
  installation, built for the container: `docker exec probeglass probeglass
  doctor`. It reports the configuration, the data directory, the database's size,
  integrity and schema version, whether the stored sensors and channels still
  validate, and whether the daemon is answering — and exits non-zero if any of it
  is fatal. It opens the database read-only and never sends an alert, because a
  diagnostic that quietly repairs what it was asked to report, or fires a real
  message into a chat room, is worse than no diagnostic at all. It replaces the
  `probeglass config check` the documentation had been promising.
- **A fresh installation is not empty.** First-run setup creates four example
  sensors — the dashboard's own health endpoint, a gateway ping, a public
  resolver, and a public website — grouped under *Examples* and marked
  `(example)` in the name. They are ordinary sensors: editable, pausable,
  deletable, and attached to no channel, so a new installation never sends a
  notification before its owner has decided where notifications should go. The
  first one points at ProbeGlass itself, which means the dashboard is never
  entirely red on a machine that has no route to the internet. They are created
  only when the sensor table is empty and the configuration file declares no
  sensors — an installation driven from YAML is deliberate and is left alone, and
  a setup request repeated in the window before the account is stored does not
  produce a second set.
- **Those four arrive with a week of history.** Seven days of invented probes at
  five-minute spacing, folded into the rollups, so the sparklines, the uptime bars
  and the 24h/7d/30d columns are populated the moment somebody first looks at the
  dashboard instead of after a week of running. Two of the four are deliberately
  not green, because a dashboard where nothing has ever happened demonstrates
  nothing: the example website carries one closed outage a few hours back, which
  is what the incident log and the red stretch in the chart are there to show, and
  the gateway's most recent probe failed, which is what the flaky status looks
  like. The gateway points at `192.168.1.1`, which answers on most home networks
  and not on many others, so that is a true statement either way — and it is one
  click to point it at a gateway that does answer.
- **The map draws its own coastlines, out of the binary.** The dashboard's slippy
  map no longer needs a tile server to have something under its pins: the world's
  land is a public-domain outline (Natural Earth 1:50m) committed to the
  repository, embedded in the binary and decoded in the browser by
  `internal/web/assets/outline.js`. It is a single SVG path in the map's own world
  coordinates, moved and scaled by one transform, with
  `vector-effect: non-scaling-stroke` so the coastline is a pixel wide at every zoom
  — a wheel click is a transform on one element rather than a few thousand
  coordinates rewritten. `cmd/worldgen` generates the file from a GeoJSON input it
  is handed rather than fetching, and writes the source URL, the input's SHA-256 and
  the tolerance into the file's own header, so what shipped can be reproduced from
  the data it came from. The layers are ordered coastlines, tiles, pins: an operator
  who configures a tile server sees their tiles and never notices this, and one
  whose server stops answering sees the world instead of a grey rectangle. It is
  coarse — continents are recognisable to about zoom 6 — and the tile URL field is
  what an operator with a tile server of their own reaches for. The demo dataset's
  two places are also an ocean apart now, Berlin against the American east coast,
  because the map opens on the closest zoom that holds every marker: two places in
  one country gave a view of one country, with the coastlines off the edge and
  nothing to show for them.
- **A sensor below its retry threshold is yellow, not invisible.** `warn` is the
  fifth status: the last probe failed, the configured `retries` are not used up
  yet, so the sensor is on its way down or already on its way back and there is
  no honest way to tell those apart yet. It opens no incident and sends no alert
  — the incident log is for outages, and a message per blip is noise. Before
  this, the same probe left a sensor that had been up looking green, and after a
  restart the same sensor read as *Pending*, which is the word for a sensor
  nothing has ever been heard from. The dashboard's list, cards, wall, tree, map
  and status donut all carry it, in the colour the uptime bars have used all
  along.
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
  runs the same checks `probeglass doctor` runs on the command line and shows
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
  dataset — more sensors than the setup writes, in their groups, with an invented
  week of history — because somebody who deleted everything and asked for it back
  wants a populated dashboard, not an empty one. The call is idempotent: a name
  already in the table
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
  that is not a `probeglass-backup`, or one from a newer schema, is refused and
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
- **The map draws its own world, borders and names included.** The map used to draw
  coastlines and nothing else, so a dashboard monitoring two hosts in two countries
  showed two pins on a silhouette. Three more public-domain Natural Earth data sets are
  now embedded in the binary and decoded in the browser: the world's first-order
  administrative borders — states, provinces, German Bundesländer and everything else at
  that level — the names of the countries, and the names of those provinces. All of it
  is drawn locally and **nothing is requested to draw it**, exactly as the coastlines
  always were: there is no geocoding service being asked where Niedersachsen is, and the
  map on a network with no internet is now a map rather than a coastline. The coastlines
  themselves move from Natural Earth's 1:110m set to its 1:50m one, which is honest to
  about zoom 6 instead of about zoom 4. The borders and the names arrive with the zoom,
  in the bands Natural Earth's own authors put them in: country outlines and country
  names at a world view, province borders from about zoom 5, province names from about
  zoom 7 — four and a half thousand of them at once would be a grey smear, not a map.
  Names that would land on a name already placed are dropped rather than drawn over it,
  countries first.
- **The map is drawn finer, and a country now has districts and cities.** The map's
  coastlines, country borders, lakes and rivers move from Natural Earth's 1:50m sets to
  its 1:10m ones — which is four times the detail on the water: the Bodensee carries 6
  points in the files this map was drawn from before and 14 now, and the same for every
  lake and coastline on it. The map's data grows from 1.1 MB to 2.0 MB, and the
  simplification stays where it was (about two kilometres, under a pixel to about zoom
  6), so the honest limit of the picture is unchanged even though there is more of it.
  With the finer sources come two things the map did not have at all:
  - **Austria's political districts** — the 94 Bezirke and the 23 statutory cities as
    117 filled areas, drawn from zoom 8, under the province lines and dimmer than both
    them and the country lines, because a Bezirk sits inside a Bundesland. This is the
    one layer on the map that is neither Natural Earth's nor public domain: it comes
    from **Statistik Austria** under **CC BY 4.0**, which asks to be named when the work
    is shown. The header of `internal/web/assets/districts.js`, `SECURITY.md`,
    `README.md` and the documentation all say so, because it is the one obligation this
    project passes on to whoever runs it — and it is also the one source URL here that
    carries a year and has to be refreshed. Everywhere outside Austria the district band
    is empty, and that is honest rather than lazy: Natural Earth's only second-order
    layer is the counties of the United States.
  - **The larger cities**, 7295 of them from Natural Earth's populated places, as a third
    list of names behind the countries and the provinces. They appear at the zoom Natural
    Earth drew each of them for — about zoom 3 for a capital, about zoom 10 for a small
    town — and they lose a collision to a province and a country above them. The names
    are the data's own, and the data is not of one habit: "Zürich" keeps its umlaut and
    Vienna arrives as "Vienna". Nothing here renames them.
- **Europe's water, in a second and finer copy.** Natural Earth publishes Europe again
  at 1:10m, with the middle-sized lakes and rivers the world files leave out — the
  Zürichsee, the Walensee, the Salzach, the Enns, the Traun. They are drawn into the same
  layer and the same colour, from about zoom 6, so nothing about the picture says which
  file a lake came from. `cmd/worldgen` grows a `-minus <geojson>` flag for them: the two
  copies overlap on 8 lakes and 72 rivers, and a shape whose name the other file already
  draws is dropped rather than drawn a hair's width apart from it. An empty name never
  matches, which is what keeps the 610 unnamed lakes of the world file in it.
- **A switch for the borders and the names**, under **Settings → Look → Map**, on by
  default. Off, the map draws the coastlines alone — and the six data files that carry
  the borders and the names are not sent to the browser at all, so the switch saves the
  megabyte it looks like it should save. Missing row means on, like every other
  yes/no setting here, and a viewer sees the state without being able to change it.
- **A site can be made while the sensor is made, in the real site dialog.** The sensor
  form's place picker ended with the places that already existed, so the choice when a
  sensor stood somewhere new was to leave the half-filled form, declare the place under
  Settings and come back — or to give up on the place. The picker's last entry is **New
  site…**, and choosing it now opens **the same window Settings → Sites opens**: name,
  kind **and coordinates**, with the sensor form left standing behind it and saying so.
  The old route could not set a coordinate at all, which was the real cost — making a
  sensor is usually the moment somebody first knows where the thing stands. The place is
  made **when the site window is saved**, not when the sensor is, so a sensor form
  abandoned afterwards leaves the place behind: that is the price of one dialog rather
  than two, and it is the honest one. The saved name is put straight into the sensor's
  place picker, and a name that already exists is used rather than made twice.
- **The map draws the borders between countries, and the water.** The layer added above
  drew the *wrong* borders. It comes from Natural Earth's first-order administrative
  lines and keeps the one class in that file that is a boundary between two provinces of
  the same country — which is fine, and is why a Bundesland has an edge. It is also why
  **Austria had no outline**: the class is not a national border, and no amount of
  zooming produces one from it, because the file does not contain one. The borders
  between countries are a data set of their own, `ne_50m_admin_0_boundary_lines_land`,
  and it is embedded and drawn now, at 1:50m like the coast, in a lighter colour than the
  province lines so the hierarchy reads at a glance. Its other classes — Disputed,
  Indefinite, Line of control, Indeterminant frontier — are **counted and named in the
  file's header rather than drawn or quietly swallowed**: 358 of its 393 lines are
  international boundaries, and the header says so. Alongside it come the water, which
  the map did not have at all: **lakes as filled rings and rivers as lines**, both from
  Natural Earth's 1:50m sets, the rivers cut off at rank 3 so that the 275 largest
  systems are drawn and the file does not become a hairball — which does mean the Rhine
  and the Elbe are in the data set and not in the file, since they are rank 4 and 5. The
  **Caspian Sea** needed a fix of its own: it is not in the lakes file, it is one hole in
  the land outline, and the outline was throwing holes away, so the largest lake on earth
  was being drawn as land. Every ring is kept now (1393 of them) and SVG's `evenodd`
  fill subtracts it without a line of browser code. The layering is coastlines, water,
  country borders, province borders, tiles, names, pins.
- **The water is ground, not a layer.** `map.borders` turns the borders and the names
  off; it does not turn the lakes and rivers off, because a map with water and a switch
  that removes it is a map of nothing, and the switch's job is the megabyte of geometry
  nobody asked for. Off, the four files behind the borders and the names are still never
  sent to the browser — the water is a fifth file and it is still sent, which is the
  point. The map's data is 1.1 MB now rather than 965 KB, and only the first zoom bands
  of it are built at any given zoom, exactly as before.
- **The districts have names.** `districts.js` carried the 117 shapes and no words, so a
  map zoomed onto Austria showed where the Bezirke are and not what they are called. The
  file now carries a name stream beside its geometry — the same packing the province
  names use, with the delta chain anchored on the centre of each district's largest ring
  — and `cmd/worldgen` reads `g_name` where it used to read it and throw it away. The
  names are drawn dim and at half opacity so they read as lying on the area rather than
  over it, and the areas themselves gained a faint grey fill, which is what makes a
  district visible as a district when the lines alone are lost in the province grid. The
  header says the anchor is a centroid and not an official administrative seat, because
  it is not one.
- **The map has towns, not only cities.** Natural Earth's populated places stop at about
  7300 names, and the middle of a country is where that runs out: a map of Austria drew
  Vienna, Graz and Linz and no Krems an der Donau, no Wiener Neustadt and no Wels. The
  GeoNames `cities5000` dump is embedded as a twelfth data set — 41 850 places of 10 000
  people and up — and the two files are drawn as one layer, so nothing about the picture
  says which file a name came from. The two overlap on 5060 names, which the browser
  drops as a duplicate where the anchor is the same place rather than trying to resolve
  it here; GeoNames carries no zoom, so the generator derives one from the population
  (zoom 4 at five million down to 11 under twenty thousand) and the header says that it
  computed it. The file is **CC BY 4.0** like the districts and is the second obligation
  this project passes on; a settlement that is a district or a quarter of a city rather
  than a city is dropped, which is what keeps Vienna's own twenty-two districts from
  arriving as twenty-two towns and no Vienna.
- **The Settings screen has a way out of it.** The last panel ends, and so did the page:
  no link to the manual, the website or the source, on the one screen somebody is on
  when they are stuck. A footer under the panels now carries four links — the project
  website, the documentation, the source on GitHub and the MIT licence — with one
  sentence saying what they are for. It lives outside the element the panels are
  rewritten into, so switching category does not take it with it.

### Changed

- **The project is called ProbeGlass.** It was Weatherglass, and that read like a
  weather app, which is the one thing it is not: it answers whether the things in
  a homelab are alive. What it does is unchanged; the name changes everywhere at
  once — the module path, the binary and with it every documented command, the
  container image, all `PROBEGLASS_*` environment variables, the data files
  (`probeglass.db`, `probeglass.yaml`), the session cookie, the issuer an
  authenticator app shows, the webhook headers (`X-ProbeGlass-Event`,
  `X-ProbeGlass-Signature`) and the backup envelope. The old name is read nowhere:
  there is no opener for a `weatherglass.db`, no importer for a
  `weatherglass-backup-*.json` and no listener for the old header. Upgrading is
  therefore a short list of hand-steps, and two of them fail quietly — a stale
  compose file goes on setting `WEATHERGLASS_*`, which is now not read at all, and
  a data file left under its old name is not found, so the new version starts with
  an empty database. The rest: rename the data file in the volume, switch webhook
  receivers to the new headers, and issue the recovery codes again, because the
  salt that hashes them is derived from the name.

- **The settings icon is a gear.** It was a circle with eight strokes around it, which
  is a sun. Settings is not a brightness control, and in a bar whose other three icons
  are also drawn by hand the one icon nobody could name was the one that mattered most.
  The gear is an eight-toothed path on the same 16×16 grid as its three neighbours, so
  the tab keeps its width and the bar keeps its one line.
- **The web assets carry an ETag, so a second visit is a 304.** The dashboard's files
  are embedded in the binary and served `no-cache`, which is right — they change with
  the version — but `embed.FS` has no modification time and no validator, and with no
  validator to revalidate against, `no-cache` means every page load downloads every
  file again. With the map's data that is now a megabyte and a half of re-download per
  visit. A SHA-256 of each embedded file is computed once at start-up from the bytes
  already in the binary — no build step, nothing to keep in sync — and served as the
  file's ETag; `If-None-Match` is answered with a `304` and no body. The cache-control
  is unchanged, so a changed file is still picked up on the next request.
- **The map no longer points at OpenStreetMap's tile servers; the field is empty and
  that is the off switch.** A fresh installation drew its map from
  `tile.openstreetmap.org`, and that address is no longer one this project may use:
  OSM's usage policy does not permit third-party applications, and their answer to one
  is not an error but an `HTTP 200` carrying a picture that says the app is blocked —
  which is what a dashboard was showing instead of a coastline. There is no comparable
  free tile server left, so the default is now **no tile server at all**: `map.tile_url`
  and `map.attribution` start empty, the map draws the coastlines that ship in the
  binary, and the browser is not asked for anything. `img-src` stays at `'self' data:`
  and the content security policy names no third party, which is the part that can be
  checked from outside. An operator running a tile server of their own — the answer
  that has actually improved — types its address into **Settings → Map**; the field,
  the validation and the attribution fallback are unchanged and still there, and a
  tile server entered there is fetched exactly as before. What changed is which of the
  two is the default, and what an empty field means: it used to mean "put the standard
  one back", and now it means "fetch nothing".
- **The container no longer runs as a fixed uid 500; it starts as root, takes its data
  directory over and drops to `PUID:PGID`.** A bind mount keeps the ownership of the host
  folder, and the platforms ProbeGlass is installed on create that folder as root, so a
  container that started as 500 could not write in it and could not fix it either — only
  root can change who owns a file. That is why a first install needed `chmod 777` on the
  appdata folder, and why every comparable image on those platforms works without a
  `chown`: they start as root, claim the folder and leave root. `PUID`, `PGID` and `UMASK`
  (defaults 500, 500 and 022, the identity every other image there uses) decide who it
  becomes; `internal/privdrop` does the handover and the drop before the database is
  opened, verifies the change and refuses to continue if it is still root. `PUID=0` keeps
  root, which is documented and not recommended. With `--user`, or on a runtime that will
  not run the process as root, the three are ignored and the daemon says so in its log.
  `CAP_NET_RAW` is carried across the drop on purpose, so a ping sensor keeps working; if
  that fails the daemon reports the lost capability and carries on. [SECURITY.md](SECURITY.md)
  states what the root moment costs.
- **The startup log line and `probeglass doctor` now name the uid.** The daemon logs the
  `uid` and `gid` it actually ended up as, and doctor reports both its own identity and the
  daemon's side by side, because under `docker exec` those two numbers differ and the
  answer to "why can it not write" depends on which one is meant.
- **A data-directory failure names the real causes.** SQLite reports every reason it cannot
  open a file as `unable to open database file (14)`, so the message now distinguishes a
  directory that is not writable by the daemon's user from a mount that is read-only or full
  — which needs a different fix than a `chown`.
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
  `PROBEGLASS_ADMIN_PASSWORD` still bootstraps an account named `admin`, now
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

- **The map's lines are no longer angular when you zoom in.** Every layer here was
  simplified with a tolerance of 0.02° — about two kilometres — and the layers that are
  drawn deepest are the ones that showed it: a country border or a coastline that is
  honest at a world view is a polygon by zoom 12, and zooming into one was the one thing
  the map did badly. The tolerance is 0.005°, about 550 m, for every layer but the
  districts, which stay at 0.002° because they are the only shapes drawn at zoom 8 and
  nothing else. The obvious fix — simplify only the deep zoom bands finely — would have
  fixed almost nothing, and the reason is worth remembering: a band is the zoom a shape
  is *first* drawn at and every band up to the current zoom is drawn, so a border filed
  in band 0 is still on screen at zoom 12 and still carries whatever tolerance band 0 was
  written with. The tolerance is a property of the layer, not of a band. The map's data
  grows from 2.0 MB to 3.9 MB for it, and the lines are now under a pixel out to about
  zoom 8 rather than zoom 6.
- **The demo data shows three pins in three colours.** Every sensor on a place was
  paused, and a paused sensor outranks a running one, so the clickable demo drew one grey
  pin per place and demonstrated nothing about what the map is for. The seven public
  checks now stand on a third place, Frankfurt — they are enabled and they answer, so
  that pin is green; the four homelab addresses stay paused, so that pin is grey; and the
  rented server's web check is enabled against a name that genuinely does not resolve,
  with an outage in its invented history that has not ended, so that pin is red. It is a
  real failure rather than a staged one: the demo and an installation running on a real
  network tell the same story about that target. The consequence is that the one enabled,
  permanently failing example sensor opens an incident in a real installation and
  notifies on it if a channel is configured — which is why the example data is one click
  from gone, and why the docs say so where the data set is described.
- **A demo history that ends mid-outage is no longer closed on the way in.** The writer
  closed every outage at its last failing probe, so an outage running up to the newest
  probe in the window was recorded as recovered by a probe that never happened — and the
  sensor restored from that history read as up rather than as down. It is now left open,
  which is what the red pin above stands on and what a restored installation would really
  see.
- **The setup screen creates the places its examples stand on, and not every place the
  demo data set knows.** It was asking for the whole set, so a fresh installation drew a
  named pin for Frankfurt with nothing on the pin — a place that exists for a check that
  is not there yet. It now takes the names off its own example sensors.
- **The Settings panels have one kind of heading.** Eleven panels opened with a bare
  `h2` and their explanation in a paragraph below it, at the panel's own spacing, so
  every panel had a slightly different gap under its title and a sentence that read like
  a second heading. They share a head now — title and one sentence at 4 px, separated
  from the body by the panel gap — and the About panel's hardcoded `Version` goes through
  the translator like every other label on the screen.

### Fixed

- **The arm64 and arm/v7 images contained an x86-64 binary.** `ARG TARGETARCH=amd64`
  is not a default that buildx overrides, it is a value it keeps: all three platforms
  compiled amd64, so the images carried the right labels over the wrong executable. On
  a Pi that is a container that dies with `exec format error` before it can say
  anything, and on an arm64 desktop one that runs at emulator speed and crashes in the
  garbage collector now and then. BuildKit folded the three platform builds into a
  single step, which is why every published image shared one build ID. The platform
  arguments are declared without a value now, and the build ends by reading the machine
  type out of the ELF header of the binary it has just made and stopping unless it is
  the platform the image is for — nothing had ever looked at that binary, which is why
  it lasted. The smoke test's log check had the same kind of quiet defect: `docker logs
  … | grep -q` under `set -o pipefail` reports a line as missing whenever the log has
  more to write after the match, so its PUID checks went red now and then on containers
  that had done exactly what was asked, and the job that watches the privilege drop
  failed for reasons that were its own.
- **An installation that had ever saved its map settings kept asking OpenStreetMap.**
  The map form writes the value it is *showing* into the settings row, so pressing Save
  on a freshly installed dashboard was enough to store `tile.openstreetmap.org` as
  though an operator had chosen it — and emptying the field later could not undo it,
  because the default put it straight back. Changing the default therefore does nothing
  for those installations on its own: the address sits in the database and would go on
  being fetched. Migration 5 deletes that exact row, and only that exact row — an address
  somebody chose, a LAN tile server, a `{z}/{x}/{y}` template on another host, all stay
  untouched. The same check runs on every start rather than only at the migration, so a
  backup taken before the upgrade and restored afterwards does not put the retired
  address back; `model.IsRetiredTileURL` is the one place that decides what "retired"
  means, so the migration and the start-up sweep cannot drift apart.
- **The example sensors the setup screen created were never probed.** The four rows
  were written and the running scheduler was never told about them. `Reconcile` is
  called after every other change to the sensor list — a sensor added, the example
  data removed, a backup restored — and this one path simply did not call it, so the
  daemon went on probing the set of sensors it had read at startup. The four examples
  sat in the table unprobed, each reading *Pending*, until the container was
  restarted or somebody edited an unrelated sensor. Together with the entry above,
  which is why the first-run dashboard used to be an empty grey grid.

- **`seed --demo` wrote incidents for outages its own history did not contain.** The
  synthetic outages in the demo dataset are described as "so many hours before now",
  and the incidents used to be written straight out of those offsets no matter how
  long the history was. `seed --demo --days 1` therefore produced incidents dated
  before the window it had just written, with no failing probe anywhere near them,
  and the incident list and the uptime bars told two different stories about the same
  day. An incident is now written from the probes that really failed — the first one
  to the one that found the service back — so an outage the window does not reach
  produces nothing. The incident writer also moved out of `internal/demo`'s private
  table and became a public `History`/`Write` pair, which is where the setup screen
  gets its week from: there is still exactly one thing in the product that invents
  probe history.

- **`Reconcile` started probe loops on a scheduler that had never been started, and
  parented them to the caller's HTTP request.** The API handlers call `Reconcile`
  after every change to the sensor list — a sensor added, the example data removed,
  a backup restored — and the context they hand it is the request's. A loop that
  takes that context as its parent dies the moment the response is written, partway
  through its first probe, and the one probe that lands before then writes a row
  into a table the same request has just counted. Nothing of this reached a user,
  because the daemon starts the scheduler before it serves anything; in the API's
  own tests it made the example-data restore fail in CI now and then, answering
  `Results: 26209` where the restore itself had said `26208`. `Reconcile` now
  rebuilds the sensor state — which is what the dashboard reads from it — and stops
  there, and the loops belong to `Start`, which calls `Reconcile` on the way up. The
  test suite had been probing the public internet from unit tests without saying
  so; it no longer does, which is about fifteen seconds of the test job.

- **The test for a silent ping probe asked the network to be honest about it.** It
  pinged `198.51.100.7`, an address reserved for documentation and routed nowhere,
  and insisted that nothing answer. On a network where a middlebox does answer —
  inside a container, for instance — the test went red and named the code for a
  fault that was in the path. The judgement it was making is now a function of its
  own, tested directly and in every branch: a host that did not answer, a packet
  that never left, a probe that was stopped before it could send, and an answer.
  Reading one of those as another is what the test exists to prevent, and none of it
  needs a network any more.
- **The browser test pinned the size of the example dataset.** It insisted the
  restore button produce exactly five sensors, because five was the number when the
  test was written; the dataset is thirteen now, so the test failed over a number
  that had been raised on purpose. It now asserts the relationship it actually cares
  about — the button loads more than the setup writes, so the two paths cannot merge
  — and leaves the exact count to the API test, which asks `len(demo.Sensors())`
  rather than writing the number down. The browser test lives in its own Go module
  and cannot import that package without pulling the store, and sqlite with it, into
  a `go.sum` that exists to stay small.
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

### Security

- Channel credentials are never returned by the API — the UI receives a marker
  instead and sending the marker back preserves the stored value.
- The Telegram bot token is stripped from error messages before they reach a log.
- `insecure_skip_verify` is available only as an explicit per-sensor opt-in.
- The session cookie is `HttpOnly` and `SameSite=Strict` and deliberately omits
  `Secure`, because homelab installs commonly serve plain HTTP on the LAN.

[Unreleased]: https://github.com/j000k3r/probeglass/compare/v0.2.3...HEAD
[0.2.3]: https://github.com/j000k3r/probeglass/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/j000k3r/probeglass/compare/v0.2.1...v0.2.2
[0.2.0]: https://github.com/j000k3r/probeglass/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/j000k3r/probeglass/releases/tag/v0.1.0
