// Internationalisation.
//
// English ships first and is the reference catalogue: every key in every other
// language must exist here. Adding a language means dropping a new dictionary
// into `catalogues`, giving it a name in `names` and adding its code to
// `available` — nothing else in the dashboard needs to change, because all text
// goes through t() or the data-i18n attributes. `catalogue_test.go` is what holds
// the other dictionaries to that promise; a partial one is caught by the test
// rather than by a reader who does not speak the language it fell back to.
//
// Each language lives in its own file beside this one, and its keys are in the
// order they appear here, so that the two can be read against each other line by
// line — a key missing from a translation then looks like a hole instead of like
// a stray English sentence.
//
// Note for translators: keys are grouped by the view they appear in, and
// {placeholders} are substituted at call time.

import { de } from './i18n.de.js';

const en = {
  'boot.loading': 'Loading…',
  'boot.failed': 'The dashboard could not start',
  'boot.help': 'The server answered, but the dashboard could not use the answer. Reloading often helps; if it does not, this text is the detail to report.',
  'boot.retry': 'Reload',

  'auth.username': 'Username',
  'auth.password': 'Password',
  'auth.confirm': 'Repeat password',
  'auth.signIn': 'Sign in',
  'auth.continue': 'Continue',
  'auth.finish': 'Finish setup',
  'auth.back': 'Back',
  'auth.setupTitle': 'Create the administrator account for this dashboard.',
  'auth.setupHint': 'You can also set PROBEGLASS_ADMIN_PASSWORD and skip this step.',
  'auth.loginHint': 'Sign in to see your dashboard.',
  'auth.mismatch': 'The two passwords do not match.',
  'auth.tooShort': 'Use at least 8 characters.',
  'auth.usernameTooShort': 'Choose a username: one to 64 characters, no spaces.',
  'auth.working': 'Working…',
  'auth.notSignedIn': 'The password was accepted, but the browser did not keep the session. Cookies have to be allowed for this site.',
  'auth.totpTitle': 'Enter the code from your authenticator app.',
  'auth.totpCode': 'Code',
  'auth.totpHint': 'Lost the device? One of your recovery codes works here too.',
  'auth.modeTitle': 'How much do you want to see?',
  'auth.mode.simple': 'Simple',
  'auth.mode.simpleBody': 'Sensors and their status, and nothing else. The advanced fields stay hidden — nothing is switched off, and you can change this in Settings whenever you like.',
  'auth.mode.advanced': 'Advanced',
  'auth.mode.advancedBody': 'Everything: retries, groups, tags, per-type probe options and alert channels, all on screen at once.',
  'auth.modeHint': 'You can change this later in Settings.',

  'nav.dashboard': 'Dashboard',
  'nav.incidents': 'Incidents',
  'nav.channels': 'Alerts',
  'nav.settings': 'Settings',
  'nav.search': 'Search',
  'nav.layout': 'Dashboard layout',
  // The ⋮ that stands in for the bar's controls when the window is too narrow to
  // hold them on one line. It opens a panel, and a screen reader announcing it as
  // "More" is announcing exactly what the three dots mean.
  'nav.more': 'More',
  'nav.signOut': 'Sign out',

  // The live indicator beside the account. Three states, not two: on and
  // connected, on and waiting to reconnect, and off. The waiting state is the
  // reason the indicator exists — a stream that has dropped looks exactly like a
  // system where nothing has gone wrong.
  'live.on': 'Live: checks appear as they happen',
  'live.waiting': 'Live: connection lost, retrying',
  'live.off': 'Live updates are off',
  'live.label': 'Live',
  'live.labelOff': 'Live off',

  // The global search. One field in the middle of the top bar that looks in
  // everything, so the wording is the wording of a search and not of a filter:
  // "no match" rather than "no sensors", and it says which part of the answer
  // came from the server and is therefore still arriving.
  'search.title': 'Search',
  'search.empty': 'Nothing found for "{query}".',
  'search.hint': 'Type at least {min} characters. Sensors, sites, channels, accounts and settings; incidents and probe messages as well.',
  'search.more': 'More results than shown — narrow the search.',
  'search.loading': 'Looking…',
  'search.openSensor': 'Open sensor',
  'search.group.sensor': 'Sensors',
  'search.group.site': 'Sites',
  'search.group.channel': 'Channels',
  'search.group.incident': 'Incidents',
  'search.group.result': 'Probe messages',
  'search.group.user': 'Accounts',
  // A settings hit is titled with the heading of the panel it opens — the words
  // `settings.*`, `sites.title`, `users.title` and `examples.title` already carry
  // — rather than with a second name for the same thing. What the row says is
  // what the panel is called once the person gets there.

  'status.up': 'Up',
  'status.down': 'Down',
  'status.warn': 'Flaky',
  'status.paused': 'Paused',
  'status.unknown': 'Pending',

  'summary.up': 'up',
  'summary.down': 'down',
  'summary.warn': 'flaky',
  'summary.paused': 'paused',
  'summary.pending': 'pending',
  'summary.uptime': 'Uptime 24h',
  'summary.latency': 'Latency 24h',
  'summary.probes': 'Probes 24h',
  'summary.monitoringSince': 'Monitoring since',
  // The three comparison figures under a sensor card: the same uptime over a day,
  // a week and a month, so that a percentage has something to be read against.
  //
  // They are keys rather than a literal in the card's markup because "7d" is not a
  // word in any language — it is a shorthand, and German writes it "7 T". The card
  // carried the literal until the interface was translated, which is how a
  // German dashboard came to say "7d" in the one strip where three abbreviations
  // sit side by side. The header's comparison line borrows weekShort, so the two
  // still read the same.
  'summary.dayShort': '24h',
  'summary.monthShort': '30d',
  'summary.weekShort': '7d',

  // The header is a strip of widgets the person arranges themselves, so every
  // one of them needs a name: it is what the picker offers and what the tile
  // says while the strip is being arranged.
  'widgets.status': 'Status',
  'widgets.uptime': 'Uptime 24h',
  'widgets.latency': 'Latency',
  'widgets.probes': 'Probes 24h',
  'widgets.since': 'Monitoring since',
  'widgets.clock': 'Clock',
  'widgets.recent': 'Recent incidents',
  'widgets.slowest': 'Slowest sensors',
  'widgets.flakiest': 'Most outages',
  'widgets.mini-map': 'Map',
  'widgets.shortcuts': 'Shortcuts',
  'widgets.customise': 'Customise',
  'widgets.done': 'Done',
  'widgets.add': 'Add a widget',
  'widgets.addHint': 'Anything you add goes on at the end of the header. Settings → Monitoring has the same list with arrows, if you would rather put it in its place straight away.',
  'widgets.allOn': 'Every widget is already in the header.',
  'widgets.remove': 'Remove',
  'widgets.moveLeft': 'Move left',
  'widgets.moveRight': 'Move right',
  'widgets.empty': 'No widgets. Press Customise to put some back.',
  'widgets.waiting': 'Waiting for the first reading…',
  'widgets.failed': 'This widget cannot be drawn.',
  'widgets.noData': 'Nothing to rank yet.',
  'widgets.noIncidents': 'No incidents recorded.',
  'widgets.live': 'Live',
  'widgets.reset': 'Reset to the default header',

  'empty.title': 'No sensors yet',
  'empty.body': 'Add your first sensor to start monitoring.',

  'sensor.new': 'New sensor',
  'sensor.edit': 'Edit sensor',
  'sensor.name': 'Name',
  'sensor.type': 'Type',
  'sensor.typeSearch': 'Search types',
  'sensor.typeSearchPlaceholder': 'Search types',
  'sensor.typeNoMatch': 'No type matches "{query}".',
  'sensor.siteSearch': 'Search sites',
  'sensor.siteSearchPlaceholder': 'Search sites',
  'sensor.siteNoMatch': 'No site matches "{query}".',
  'sensor.parentSearch': 'Search sensors',
  'sensor.parentSearchPlaceholder': 'Search sensors',
  'sensor.parentNoMatch': 'No sensor matches "{query}".',
  'sensor.parentOthers': 'Other sensors',
  'sensor.parentEmpty': 'There are no other sensors to depend on.',
  'sensor.groupNone': 'No group',
  'sensor.groupSearch': 'Search groups',
  'sensor.groupSearchPlaceholder': 'Search or name a group',
  'sensor.groupNoMatch': 'No group matches "{query}".',
  'sensor.groupHeading': 'Groups',
  'sensor.groupEmpty': 'No groups yet — use New group to make one.',
  'sensor.groupCount': '{count} sensor(s)',
  'sensor.target': 'Target',
  'sensor.interval': 'Interval (s)',
  'sensor.timeout': 'Timeout (s)',
  'sensor.retries': 'Retries before down',
  // A sensor that sits behind another one. While the parent is down this one is
  // excused: no incident of its own, no alert, and the checks it fails in there do not
  // count against its uptime. A power cut is one outage and not thirty.
  'sensor.parent': 'Depends on',
  'sensor.parentNone': 'Nothing — this one stands on its own',
  'dependent.badge': 'Dependent',
  'dependent.badgeHint': 'Something this sensor sits behind is down, so this is a consequence rather than an outage of its own. It is still being checked and the result is still recorded.',
  'sensor.group': 'Group',
  'sensor.site': 'Site',
  'sensor.tags': 'Tags',
  'sensor.channels': 'Alert channels',
  'sensor.escalateAfter': 'Escalate after (minutes)',
  'sensor.escalateChannels': 'Escalation channels',
  'sensor.reminder': 'Remind every (s)',
  'sensor.enabled': 'Enabled',
  'sensor.test': 'Test',
  'sensor.testing': 'Testing…',
  'sensor.testOk': 'Test passed',
  'sensor.testFail': 'Test failed',

  // The sensor catalogue: the dialog the type field opens, and the device
  // templates in it. A template is a set of checks; the words for its checks are
  // keys like the rest, so a translation cannot silently fall back to English.
  'catalog.title': 'Sensor catalogue',
  'catalog.sensors': 'Single sensor',
  'catalog.templates': 'Templates',
  'catalog.search': 'Search the catalogue',
  'catalog.searchPlaceholder': 'Search sensors and templates',
  'catalog.noMatch': 'Nothing matches "{query}".',
  'catalog.templateHint': 'A template adds a set of sensors for one device. Untick what you do not want; every sensor stays editable afterwards.',
  'catalog.checks': '{count} sensors',
  'catalog.apply': 'Add {count} sensor(s)',
  'catalog.deviceName': 'Device name',
  'catalog.address': 'Address',
  'catalog.group': 'Group',
  'catalog.groupHint': 'Leave empty for no group.',
  'catalog.credentials': 'SNMP credentials',
  'catalog.noneSelected': 'Choose at least one check.',
  'catalog.missing': '{field} is required.',
  'catalog.added': '{count} sensor(s) added.',
  'catalog.back': 'Back to the catalogue',
  'catalog.templateNote': 'OIDs can differ by model and firmware. Check the first results and edit a sensor if a value looks wrong.',
  'catalog.system.nas': 'NAS',
  'catalog.system.server': 'Server',
  'catalog.system.router': 'Router',
  'catalog.system.firewall': 'Firewall',
  'catalog.system.wireless': 'Wireless',
  'catalog.system.ups': 'UPS',
  'catalog.system.printer': 'Printer',
  'catalog.tag.availability': 'Availability',
  'catalog.tag.cpu': 'CPU',
  'catalog.tag.temperature': 'Temperature',
  'catalog.tag.storage': 'Storage',
  'catalog.tag.power': 'Power',
  'catalog.filterVendor': 'Vendor',
  'catalog.filterSystem': 'System',
  'catalog.filterTag': 'Watches',
  'catalog.filters': 'Filters',
  'catalog.clearFilters': 'Clear filters',
  'catalog.all': 'All',
  'catalog.checkDevice': 'Check device',
  'catalog.checking': 'Checking…',
  'catalog.deviceOk': 'Device answers as: {device}',
  'catalog.deviceHint': 'Check the device before adding the sensors: the address and its SNMP credentials are used for one real request.',
  'catalog.noTemplates': 'No template matches these filters.',
  'catalog.source': 'Source',
  'catalog.version': 'Version',
  'catalog.system.application': 'Application',
  'catalog.system.home': 'Smart home',
  'catalog.system.media': 'Media',
  'catalog.system.dns': 'DNS',
  'catalog.tag.web': 'Web',
  'catalog.tag.home': 'Smart home',
  'catalog.tag.media': 'Media',
  'catalog.tag.dns': 'DNS',
  'catalog.tag.network': 'Network',
  'catalog.check.web': 'Web interface',
  'catalog.check.health': 'Health endpoint',
  'catalog.check.port': 'Port open',
  'catalog.check.reachable': 'Reachable (ICMP)',
  'catalog.credential.token': 'API token',
  'catalog.credential.tokenHelp': 'Create an admin token in MOS (Settings -> API tokens, or POST /api/v1/auth/admin-tokens) and paste it here. It is stored with the sensors and masked in the API.',
  'catalog.device': 'Device',
  'catalog.changeDevice': 'Change device',
  'catalog.discovered': '{count} more found on the device.',
  'catalog.discoveryNotes': 'Some lists could not be read: {notes}',
  'catalog.template.generic.name': 'Generic SNMP device',
  'catalog.template.generic.description': 'Uptime and CPU load from HOST-RESOURCES-MIB. Works on most servers, NAS boxes and appliances.',
  'catalog.template.generic.uptime': 'Uptime',
  'catalog.template.generic.cpu_load': 'CPU load (%)',
  'catalog.template.qnap.name': 'QNAP NAS',
  'catalog.template.qnap.description': 'CPU, system and first-disk temperature, plus uptime. The OIDs depend on the model; check the first values.',
  'catalog.template.qnap.cpu_temp': 'CPU temperature',
  'catalog.template.qnap.system_temp': 'System temperature',
  'catalog.template.qnap.disk1_temp': 'Disk 1 temperature',
  'catalog.template.qnap.disk2_temp': 'Disk 2 temperature',
  'catalog.template.qnap.disk3_temp': 'Disk 3 temperature',
  'catalog.template.qnap.disk4_temp': 'Disk 4 temperature',
  'catalog.template.qnap.uptime': 'Uptime',
  'catalog.template.synology.name': 'Synology DiskStation',
  'catalog.template.synology.description': 'System temperature and status, first-disk temperature, uptime.',
  'catalog.template.synology.system_temp': 'System temperature',
  'catalog.template.synology.system_status': 'System status',
  'catalog.template.synology.disk1_temp': 'Disk 1 temperature',
  'catalog.template.synology.disk2_temp': 'Disk 2 temperature',
  'catalog.template.synology.disk3_temp': 'Disk 3 temperature',
  'catalog.template.synology.disk4_temp': 'Disk 4 temperature',
  'catalog.template.synology.uptime': 'Uptime',
  'catalog.template.unifi.name': 'Ubiquiti UniFi',
  'catalog.template.unifi.description': 'Uptime from the standard MIB and reachability. UniFi\'s own MIB has no CPU, memory or temperature objects.',
  'catalog.template.unifi.uptime': 'Uptime',
  'catalog.template.unifi.reachable': 'Reachable (ICMP)',
  'catalog.template.apc.name': 'APC UPS',
  'catalog.template.apc.description': 'Battery charge and temperature, output load, uptime.',
  'catalog.template.apc.battery_charge': 'Battery charge (%)',
  'catalog.template.apc.battery_temp': 'Battery temperature',
  'catalog.template.apc.output_load': 'Output load (%)',
  'catalog.template.apc.uptime': 'Uptime',
  'catalog.template.mikrotik.name': 'MikroTik RouterOS',
  'catalog.template.mikrotik.description': 'CPU load, board temperature, uptime and reachability.',
  'catalog.template.mikrotik.cpu_load': 'CPU load (%)',
  'catalog.template.mikrotik.temperature': 'Board temperature',
  'catalog.template.mikrotik.uptime': 'Uptime',
  'catalog.template.mikrotik.reachable': 'Reachable (ICMP)',
  'catalog.template.pfsense.name': 'pfSense / OPNsense',
  'catalog.template.pfsense.description': 'CPU load from HOST-RESOURCES, uptime and reachability. Works on pfSense CE/Plus and OPNsense.',
  'catalog.template.pfsense.cpu_load': 'CPU load (%)',
  'catalog.template.pfsense.uptime': 'Uptime',
  'catalog.template.pfsense.reachable': 'Reachable (ICMP)',
  'catalog.template.proxmox.name': 'Proxmox VE',
  'catalog.template.proxmox.description': 'CPU load, uptime and reachability for the host itself.',
  'catalog.template.proxmox.cpu_load': 'CPU load (%)',
  'catalog.template.proxmox.uptime': 'Uptime',
  'catalog.template.proxmox.reachable': 'Reachable (ICMP)',
  'catalog.template.truenas.name': 'TrueNAS CORE / SCALE',
  'catalog.template.truenas.description': 'CPU load, uptime and reachability from HOST-RESOURCES. Works on CORE and SCALE.',
  'catalog.template.truenas.cpu_load': 'CPU load (%)',
  'catalog.template.truenas.uptime': 'Uptime',
  'catalog.template.truenas.reachable': 'Reachable (ICMP)',
  'catalog.template.printer.name': 'Generic printer',
  'catalog.template.printer.description': 'Page count, uptime and reachability from the Printer MIB. Works on printers that expose it.',
  'catalog.template.printer.page_count': 'Pages printed',
  'catalog.template.printer.uptime': 'Uptime',
  'catalog.template.printer.reachable': 'Reachable (ICMP)',
  'catalog.template.mos.name': 'MOS NAS OS',
  'catalog.template.mos.description': 'The MOS API on /api/v1: system load, disks, pools and optional UPS status. Create an admin token in MOS and paste it here.',
  'catalog.template.mos.cpu_load': 'CPU load (%)',
  'catalog.template.mos.cpu_temp': 'CPU temperature',
  'catalog.template.mos.memory': 'Memory used (%)',
  'catalog.template.mos.system_load': 'System load',
  'catalog.template.mos.disks': 'Disks',
  'catalog.template.mos.pools': 'Pools',
  'catalog.template.mos.ups': 'UPS status (optional)',
  'catalog.template.mos.reachable': 'Reachable (ICMP)',
  'catalog.template.homeassistant.name': 'Home Assistant',
  'catalog.template.homeassistant.description': 'Web interface on port 8123 and reachability.',
  'catalog.template.pihole.name': 'Pi-hole',
  'catalog.template.pihole.description': 'Admin interface and reachability. Watch the resolver itself with a DNS sensor for a name it must answer.',
  'catalog.template.adguard.name': 'AdGuard Home',
  'catalog.template.adguard.description': 'Admin interface on the default web port and reachability.',
  'catalog.template.grafana.name': 'Grafana',
  'catalog.template.grafana.description': 'Health endpoint on port 3000 and reachability.',
  'catalog.template.portainer.name': 'Portainer',
  'catalog.template.portainer.description': 'Web interface on port 9000 and reachability. A TLS-enabled Portainer on 9443 can be edited after it is created.',
  'catalog.template.jellyfin.name': 'Jellyfin',
  'catalog.template.jellyfin.description': 'Health endpoint on port 8096 and reachability.',
  'catalog.template.uptimekuma.name': 'Uptime Kuma',
  'catalog.template.uptimekuma.description': 'Web interface on port 3001 and reachability.',
  'catalog.template.nodered.name': 'Node-RED',
  'catalog.template.nodered.description': 'Editor on port 1880 and reachability.',
  'catalog.template.mosquitto.name': 'Mosquitto',
  'catalog.template.mosquitto.description': 'MQTT port 1883 and reachability.',
  'catalog.template.shelly.name': 'Shelly',
  'catalog.template.shelly.description': 'Status endpoint and reachability for Shelly relays.',
  'sensor.runNow': 'Run now',
  'sensor.running': 'Running…',
  'sensor.delete': 'Delete',
  'sensor.deleteConfirm': 'Delete the sensor "{name}" and all of its history?',
  'sensor.managed': 'Managed by the configuration file',
  'sensor.detail': 'Details',
  'sensor.history': 'History',
  'sensor.recent': 'Recent probes',
  'sensor.none': 'No probes recorded in this window yet.',
  'sensor.moveUp': 'Move up',
  'sensor.moveDown': 'Move down',
  'sensor.invalid': 'Please correct the highlighted fields.',

  // The dashboard with nothing to show because the search box is filtering
  // everything out. It is a different sentence from "there are no sensors", and the
  // two used to be the same one — which told somebody with four sensors that they
  // had none.
  'dashboard.noneMatching': 'Nothing matches "{query}".',

  // Icons: the drawings that ship with the program, and the field that picks one.
  //
  // The names below are what the picker offers and what a symbol is announced as
  // wherever it is drawn, so each one says what the drawing shows rather than what
  // its file is called: somebody looking for their router is looking for "Router",
  // and `router.svg` is a detail of the build. A name assembled at call time —
  // t(`icons.name.${name}`) — which is why assets_test.go holds this list against
  // the drawings in internal/icon, the way it holds the sensor types.
  'icons.label': 'Icon',
  'icons.none': 'No icon',
  'icons.choose': 'Choose an icon',
  'icons.builtinGroup': 'Built in',
  'icons.uploadedGroup': 'Uploaded',
  'icons.upload': 'Upload an image',
  'icons.uploading': 'Uploading…',
  'icons.uploadDone': 'Image uploaded.',
  'icons.hint': 'PNG, JPEG or GIF — at most {kb} KiB and {px} pixels a side.',
  'icons.loading': 'Loading…',
  'icons.empty': 'Nothing uploaded yet.',
  'icons.delete': 'Delete',
  'icons.deleteConfirm': 'Delete the image "{name}" from the list?',
  'icons.deleteInUse': 'The image "{name}" is still used. Delete it and take it off everything that wears it?',
  'icons.deleted': 'Image deleted.',
  // The online catalogue at dashboardicons.com. The search reaches it every time,
  // and the sentence for the case where it cannot be reached says where the icons
  // are rather than only that something failed — a homelab with no route out will
  // read it on every search.
  'icons.search': 'Search by name',
  'icons.searchPlaceholder': 'pi-hole, plex, router…',
  'icons.searching': 'Searching…',
  'icons.searchEmpty': 'Nothing in the online catalogue matches that.',
  'icons.searchOffline': 'Online search is not available right now — the icons are on',
  'icons.importDone': '"{name}" was added to your icons.',
  'icons.credit': 'Online icons from',
  'icons.name.router': 'Router',
  'icons.name.switch': 'Switch',
  'icons.name.server': 'Server',
  'icons.name.nas': 'NAS',
  'icons.name.container': 'Container',
  'icons.name.cloud': 'Cloud',
  'icons.name.globe': 'Internet',
  'icons.name.database': 'Database',
  'icons.name.disk': 'Disk',
  'icons.name.cpu': 'Processor',
  'icons.name.wifi': 'Wi-Fi',
  'icons.name.camera': 'Camera',
  'icons.name.printer': 'Printer',
  'icons.name.battery': 'Battery',
  'icons.name.plug': 'Socket',
  'icons.name.thermometer': 'Temp.',
  'icons.name.bulb': 'Light',
  'icons.name.lock': 'Lock',
  'icons.name.certificate': 'Certificate',
  'icons.name.bell': 'Alert',
  'icons.name.mail': 'E-mail',
  'icons.name.phone': 'Phone',
  'icons.name.laptop': 'Laptop',
  'icons.name.monitor': 'Screen',
  'icons.name.clock': 'Clock',
  'icons.name.tag': 'Label',

  'field.time': 'Time',
  'field.result': 'Result',
  'field.latency': 'Latency',
  'field.status': 'Status',
  'field.message': 'Message',
  'field.duration': 'Duration',
  'field.sensor': 'Sensor',
  'field.started': 'Started',
  'field.resolved': 'Resolved',

  'detail.uptime': 'Uptime',
  'detail.avg': 'Average',
  'detail.p95': 'Slowest',
  'detail.incidents': 'Incidents in this window',
  'detail.downtime': 'Total downtime',
  'detail.openSince': 'Down since',
  // The relationships, in the dialog rather than only in the editor: what this sensor
  // sits behind, and what sits behind it. A card says "expected" when a parent is down
  // and says nothing at all when everything is fine, so a green sensor behind another
  // one gave no clue at all until somebody opened the form.
  'detail.dependsOn': 'Depends on {name}',
  'detail.dependedOnBy': '1 sensor sits behind this one',
  'detail.dependedOnByPlural': '{count} sensors sit behind this one',
  'detail.lastProbe': 'Last probe',
  'detail.never': 'never',
  'detail.averageShort': 'avg',
  // The line that offers to catch the open window up. Two forms, because "1 new
  // probes" reads as a program talking to itself.
  'detail.newProbe': '1 new check — show it',
  'detail.newProbes': '{count} new checks — show them',

  // How long ago the last check was, on every card and in two of the widgets.
  // These are the shortest strings in the file and the ones a reader sees most
  // often, and they are the one place where a language changes the shape of the
  // sentence rather than only the words: English puts the amount first and the
  // unit after it, German turns the whole thing around. The number arrives as
  // {n} rather than being glued on in code, so both are a sentence a translator
  // can write, not a prefix with a number under it.
  //
  // Five separate keys rather than one with a unit in it, because the unit is
  // not always an abbreviation of the same word — "min" and "h" are not "Minute"
  // and "Stunde" in any language, and a translator given `{n} {unit}` would have
  // to be handed the same English abbreviations the code happens to hold.
  'time.justNow': 'just now',
  'time.secondsAgo': '{n}s ago',
  'time.minutesAgo': '{n} min ago',
  'time.hoursAgo': '{n} h ago',
  'time.daysAgo': '{n} d ago',

  'range.1h': '1 hour',
  'range.24h': '24 hours',
  'range.7d': '7 days',
  'range.30d': '30 days',
  'range.90d': '90 days',
  'range.all': 'All time',

  // The filter line above the incident list. "All" and "reset" are the two words
  // that make the others undoable, which is why they are named rather than drawn
  // as symbols.
  'filter.status': 'Status',
  'filter.sensor': 'Sensor',
  'filter.period': 'Period',
  'filter.all': 'All',
  'filter.open': 'Open',
  'filter.resolved': 'Resolved',
  'filter.placeholder': 'Cause, sensor or note',
  'filter.reset': 'Reset filter',
  'filter.anySensor': 'All sensors',

  'incidents.title': 'Incidents',
  'incidents.empty': 'No incidents',
  'incidents.emptyBody': 'Nothing has failed in this window. Keep it that way.',
  'incidents.emptyFiltered': 'The filter is hiding everything. Widen the period, pick another sensor, or reset it.',
  'incidents.ongoing': 'ongoing',
  'incidents.resolved': 'resolved',
  'incidents.loading': 'Loading…',

  // Acknowledging an outage. Not the same as pausing: the sensor keeps being
  // probed, and only the reminders stop.
  'ack.action': 'Acknowledge',
  'ack.undo': 'Undo acknowledgement',
  'ack.short': 'acknowledged',
  'ack.badge': 'This outage has been acknowledged; reminders are paused.',
  'ack.title': 'Acknowledge this outage',
  'ack.explain': 'ProbeGlass keeps probing and stops repeating the reminder. The recovery message still arrives, so you will still hear when it is back.',
  'ack.note': 'Note',
  'ack.notePlaceholder': 'known reboot, on it',
  'ack.done': 'Outage acknowledged',
  'ack.undone': 'Acknowledgement removed',

  // Password strength. The words describe; they do not forbid.
  'password.level.weak': 'weak',
  'password.level.fair': 'fair',
  'password.level.good': 'good',
  'password.level.strong': 'strong',
  'password.tip.common': 'This one is in every list of common passwords. Pick something else.',
  'password.tip.longer': 'Longer helps most — aim for twelve characters or more.',
  'password.tip.mix': 'Mix in a digit or a symbol.',
  'password.tip.more': 'Nearly there: a few more characters and it is strong.',
  'password.tip.good': 'Good to go.',

  'channels.templates': 'Message templates',
  'channels.title': 'Alert channels',
  'channels.new': 'New channel',
  'channels.edit': 'Edit channel',
  'channels.empty': 'No channels',
  'channels.emptyBody': 'Add Telegram, Discord, Matrix, ntfy, Slack or a webhook to be told when something breaks.',
  'channels.name': 'Name',
  'channels.type': 'Type',
  'channels.enabled': 'Enabled',
  'channels.usedBy': 'Used by {n} sensor(s)',
  'channels.unused': 'Not used by any sensor',
  'channels.test': 'Send test',
  'channels.testing': 'Sending…',
  'channels.testOk': 'Test message delivered',
  'channels.setup': 'Setting up {type}',
  'channels.deleteConfirm': 'Delete the channel "{name}"?',
  'channels.error': 'Last delivery failed',

  'settings.title': 'Settings',
  // The five categories the settings page is divided into, and the tabs above
  // them. A panel heading stays a panel heading; these are the shelves the panels
  // stand on, and only the chosen one is on screen.
  'settings.group.look': 'Look',
  'settings.group.widgets': 'Widgets',
  'settings.clock': 'Clock',
  'settings.clock.auto': 'Automatic',
  'settings.clock.24': '24-hour',
  'settings.clock.12': '12-hour',
  'settings.clockHint': 'Automatic follows the interface language: German reads 14:30, English reads 2:30 PM. Choose one of the other two if the clock on the wall says otherwise.',
  'settings.group.monitor': 'Monitoring',
  'settings.group.access': 'Access',
  'settings.group.backup': 'Backup',
  'settings.group.help': 'Help and maintenance',
  'settings.tabsAria': 'Settings categories',
  'settings.appearance': 'Appearance',
  // The line under a panel's heading. It says what the panel is for rather than
  // restating its name, which is the one thing a heading cannot do.
  'settings.appearanceHint': 'How this dashboard looks to you and how much it shows. These are your own choices — they are stored with your account and change nothing for anybody else.',
  'settings.theme': 'Theme',
  'settings.theme.auto': 'Match system',
  'settings.theme.dark': 'Dark',
  'settings.theme.light': 'Light',
  'settings.accent': 'Accent',
  'settings.accent.azure': 'Azure',
  'settings.accent.violet': 'Violet',
  'settings.accent.emerald': 'Emerald',
  'settings.accent.amber': 'Amber',
  'settings.accent.rose': 'Rose',
  'settings.accent.slate': 'Slate',
  'settings.density': 'Density',
  'settings.density.compact': 'Compact',
  'settings.density.comfortable': 'Comfortable',
  'settings.layout': 'Layout',
  'settings.layout.grid': 'Cards',
  'settings.layout.list': 'List',
  'settings.layout.tree': 'Tree',
  'settings.layout.wall': 'Wall',
  'settings.layout.map': 'Map',
  'settings.language': 'Language',
  'settings.mode': 'Interface mode',
  'settings.mode.simple': 'Simple',
  'settings.mode.advanced': 'Advanced',
  'settings.modeHint': 'The simple mode hides the advanced fields and areas. Nothing is switched off — every sensor and setting keeps working exactly as before.',
  'settings.help': 'Getting started',
  'settings.tourHint': 'Walk through the dashboard again, one thing at a time.',
  'settings.tourStart': 'Start the tour',
  'settings.diagnostics': 'Diagnostics',
  'settings.doctorHint': 'Run the same self-check the command line offers. It re-reads the database, checks the configuration and dials the health endpoint, and takes a few seconds.',
  'settings.doctorRun': 'Run check',
  'settings.doctorRunning': 'Running the check…',
  'settings.doctorOk': 'ok',
  'settings.doctorWarn': 'warn',
  'settings.doctorFail': 'fail',
  'settings.doctorOutput': 'Full command output',
  'settings.dashboard': 'Dashboard defaults',
  'settings.dashboardHint': 'What this installation has stored, and how long it keeps it.',
  // The header of the dashboard is arranged on the dashboard itself; the Widgets
  // category is the same list, spelled out, for anybody who would rather see it as
  // a list than drag tiles around.
  'settings.widgets': 'Header widgets',
  'settings.widgetsHint': 'What is shown at the top of the dashboard, and in what order. The same list is arranged by dragging the tiles themselves.',
  'settings.retention': 'History retention',
  'settings.retentionRaw': 'Raw probes',
  'settings.retentionHourly': 'Hourly rollups',
  'settings.retentionDaily': 'Daily rollups',
  'settings.retentionDailyValue': 'kept forever',
  'settings.security': 'Access',
  'settings.securityHint': 'The account you are signed in with: its password, its second factor, and the API key that lets a script read everything here and change it.',
  'settings.password': 'Change password',
  'settings.currentPassword': 'Current password',
  'settings.newPassword': 'New password',
  'settings.passwordChanged': 'Password changed. Other sessions were signed out.',
  'settings.apiKey': 'API key',
  'settings.apiKeyHint': 'Use it as the X-API-Key header, or as "Authorization: Bearer <key>".',
  'settings.apiKeyNone': 'No API key yet.',
  'settings.rotateKey': 'Generate new key',
  'settings.deleteKey': 'Remove key',
  'settings.copy': 'Copy',
  'settings.copied': 'Copied',
  'settings.copyManual': 'The browser would not copy for you. The text is selected — press Ctrl+C (⌘C on a Mac).',

  // Backup and restore. The two scopes are spelled out as sentences rather than
  // left as the words "config" and "history": what somebody needs to know before
  // they choose is whether their outage log is in the file.
  'settings.backup': 'Backup and restore',
  'backup.hint': 'Write everything this installation knows into one file, or read such a file back. For a byte-exact copy of the whole installation, copy the database file itself — a backup stays a file copy; see the documentation.',
  'backup.scope': 'What to include',
  'backup.scope.config': 'Without history',
  'backup.scope.history': 'With history',
  'backup.scope.configHint': 'Sensors, sites, alert channels, accounts and settings. Outages and probe results are left out, and a restore leaves the ones already here untouched.',
  'backup.scope.historyHint': 'All of the above, plus outages and the hourly and daily figures for the chosen period. The individual probe messages of the last few days are not included — the search will not find them after a restore.',
  'backup.days': 'History of the last',
  'backup.days.7': '7 days',
  'backup.days.30': '30 days',
  'backup.days.90': '90 days',
  'backup.days.all': 'Everything kept',
  'backup.download': 'Download backup',
  'configExport.title': 'Configuration file',
  'configExport.hint': 'Write the sites, channels and sensors this installation holds as a probeglass.yaml. Accounts, incidents and history stay out of it, and channel secrets are in clear text.',
  'configExport.download': 'Download probeglass.yaml',
  'configExport.downloaded': 'Configuration file written.',
  'metrics.title': 'Metrics',
  'metrics.hint': 'A read-only Prometheus endpoint at /metrics: sensor status, uptime, the last latency and the open incidents. A scraper uses its own token and cannot write anything.',
  'metrics.token': 'Metrics token',
  'metrics.example': 'The endpoint is {url}. Send the token as Authorization: Bearer <token>; a scraper that cannot set that header may send X-API-Key instead.',
  'metrics.off': 'Metrics are off. Generate a read-only token to enable the endpoint.',
  'metrics.enable': 'Generate token',
  'metrics.rotate': 'Rotate token',
  'metrics.disable': 'Disable',
  'metrics.rotateConfirm': 'Rotate the metrics token? A scraper still using the old one starts getting 401.',
  'metrics.disableConfirm': 'Disable /metrics? The token is removed and the endpoint answers 404.',
  'metrics.rotated': 'Metrics token rotated.',
  'metrics.disabled': 'Metrics disabled.',
  'backup.downloaded': 'The backup was handed to your browser.',
  'backup.restore': 'Restore from a file…',
  'backup.confirmRestore': 'Restore from "{name}"? Everything in this installation — sensors, channels, accounts and settings — is replaced by what the file says.',
  'backup.confirmRestoreAgain': 'This cannot be undone in the dashboard. A copy of the database is written to the data directory first, and that copy is the way back. Restore now?',
  'backup.restored': 'Restored: {sensors} sensors, {channels} channels. Reloading…',
  'backup.safetyCopy': 'The state from before is kept as {name}, next to the database.',
  'backup.epochNotice': 'This file was written before some sessions were signed out, so those sign-ins are valid again.',
  'backup.secretWarning': 'This file holds the password hashes, the two-factor secrets and the credentials of every alert channel in plain text. It is worth as much as the database itself — keep it somewhere you would keep the database.',
  'backup.epochWarning': 'Sign-ins stay valid across a restore. Restoring an older file brings back sessions that were signed out after it was written.',
  'settings.channel': 'Channel',
  'settings.channelName.release': 'release',
  'settings.channelName.dev': 'dev',
  'settings.channelName.local': 'local',
  'settings.channelAbout.release': 'A published version. This image came from a release tag.',
  'settings.channelAbout.dev': 'A build of the branch being worked on, newer than the last release and not a release itself. It follows the newest push, so it can change under you.',
  'settings.channelAbout.local': 'Built from a working tree, not published anywhere.',
  'settings.about': 'About',
  'settings.aboutHint': 'Which build is running, and where it keeps its files. Both are the first thing anybody asks for when something looks wrong.',
  'settings.version': 'Version',
  'settings.storage': 'Storage',
  'settings.sensors': 'Sensors',
  'settings.channels': 'Channels',
  'settings.probes': 'Stored probes',
  'settings.openIncidents': 'Ongoing incidents',
  'settings.oldest': 'Oldest sample',
  'settings.newest': 'Newest sample',
  'settings.dbSize': 'Database size',
  'settings.baseUrl': 'Base URL',
  'settings.dataDir': 'Data directory',
  'settings.configFile': 'Configuration file',
  'settings.uptime': 'Running for',
  'settings.built': 'Built',
  'settings.logLevel': 'Log level',
  'settings.none': 'not set',

  // The footer under the settings page: the way out to the pages that are not in
  // this dashboard. Every one of them leaves the installation, so each is opened in
  // a new tab rather than replacing the dashboard somebody is in the middle of.
  // The licence is a link and not just a word because "MIT" is a question, and the
  // answer is one click away instead of one search away.
  'settings.linksAria': 'ProbeGlass on the web',
  'settings.link.website': 'Website',
  'settings.link.docs': 'Documentation',
  'settings.link.github': 'Source on GitHub',
  'settings.link.licence': 'MIT licence',
  'settings.linksHint': 'The manual, the project website and the source. The dashboard works without any of them — these are what to read when a check or a setting is not doing what you expected.',

  // ------------------------------------------------------------------ the map
  'settings.map': 'Map',
  'settings.mapHint': 'Where the map gets its tiles, and what is drawn over them. Left empty, the map draws the world that ships inside the program and asks no server for anything.',
  'settings.tileUrl': 'Tile URL',
  'settings.tileAttribution': 'Attribution',
  'settings.mapBorders': 'Draw country, province and district borders',
  'settings.mapBordersHint': 'The dashboard draws these itself, from data inside the program: the coastlines, every country border, the provinces, states and Bundesländer within them, each with its name, the larger cities, and the lakes and rivers underneath. Nothing is fetched from anywhere. The water is always drawn; this switch takes off the borders and the names together, so a country is never left with the lines of its districts still on it. Country borders and their names appear as you zoom in; the provinces follow a little later, the cities after them, and Austria\'s districts — the Bezirke, which only Austria has here — at zoom 8. At world scale all of it would be a grey texture, not information.',
  'settings.tileHint': 'The template for one map tile. {z}, {x} and {y} are required; {s} is optional and is replaced by a, b or c in turn. Leave it empty and the map draws the world that ships with the dashboard — the coastlines, the borders, the lakes and rivers, and the names, all from data inside the program — coarse, but it needs no server and asks nobody anything.',
  'settings.tilePrivacy': 'Your browser fetches the tiles straight from whatever you enter here, so that server sees your IP address, your browser and roughly which part of the map you are looking at. That is fine for a homelab on your own network, and it is the reason OpenStreetMap can no longer be the default: their public tiles are not open to applications any more. An empty field costs nothing and tells nobody.',
  'settings.mapSaved': 'Map settings saved.',

  // ------------------------------------------------------------ the account menu
  'user.menuAria': 'Account menu for {name}',
  'user.unknown': 'Not signed in',
  'user.settings': 'Settings',

  // ------------------------------------------------------------------ the sites
  'sites.title': 'Sites',
  'sites.hint': 'A site is a place your sensors are: the rack in the cellar, the VPS at a hoster. It groups the dashboard, and with coordinates it is drawn on the map. A sensor without a site works exactly as before.',
  'sites.new': 'New site',
  'sites.newOption': 'New site…',
  'sites.nameRequired': 'Enter a name for the new site.',
  'sites.name': 'Name',
  'sites.kind': 'Kind',
  'sites.kind.local': 'Homelab',
  'sites.kind.external': 'External',
  'sites.latitude': 'Latitude',
  'sites.longitude': 'Longitude',
  'sites.coordsHint': 'Both coordinates or neither. They are optional: a site without them groups and shows, it just is not drawn on the map.',
  'sites.none': 'No site',
  'sites.sensorCount': '{count} sensor(s)',
  'sites.sensorCountDown': '{count} sensor(s), {down} down',
  'sites.empty': 'No sites yet',
  'sites.emptyBody': 'Add one and the tree will group by place, and the map will have something to draw.',
  'sites.managed': 'This site comes from the configuration file and cannot be changed here.',
  'sites.exampleHint': 'This place is example data, not one of yours. Settings → Example data removes it together with the example sensors.',
  'sites.inUse': 'This site still has {count} sensor(s) on it. Move them first, or delete it and leave them without a site.',
  'sites.deleteConfirm': 'Delete the site "{name}"?',
  'sites.deleteConfirmUnassign': 'Delete the site "{name}" and leave its {count} sensor(s) without a site?',
  'sites.unassign': 'Delete and unassign',
  'sites.created': 'Site added.',
  'sites.updated': 'Site updated.',
  'sites.deleted': 'Site removed.',
  'sites.edit': 'Edit site',
  'sites.nameHint': 'Up to 64 characters. No slashes — the name ends up in a URL.',
  'sites.fromSensorHint': 'This is the same window Settings → Sites opens. The sensor you were filling in stays as you left it, behind this one.',

  // ------------------------------------------------------------------- the map
  'map.aria': 'Map of your sites',
  'map.zoomIn': 'Zoom in',
  'map.zoomOut': 'Zoom out',
  'map.reset': 'Fit all sites',
  'map.legend.local': 'Homelab',
  'map.legend.external': 'External',
  'map.offline': 'The tile server you configured is not answering. The map is drawing its own world instead — coastlines, borders, districts, lakes, rivers and place names — and every place is still in the right position relative to the others.',
  'map.empty': 'No site has coordinates yet. Add them under Sites in Settings and they will be drawn here.',
  'map.unplaced': 'Not drawn: {count} site(s).',
  'map.unplacedHint': 'They have no coordinates yet.',
  'map.noSensors': 'Nothing is being probed here yet.',

  // --------------------------------------------------------------- the wall
  'wall.heartbeat': 'Recent checks',
  'heartbeat.ok': 'up',
  'heartbeat.fail': 'down',
  'heartbeat.none': 'No checks yet',
  'heartbeat.check': 'Check {i} of {n}',


  // ------------------------------------------------------------------ accounts
  'users.title': 'Users',
  'users.panelHint': 'Everyone who can sign in here. An administrator can add an account, set a password for somebody who has lost theirs, and switch an account off without deleting it.',
  'users.hint': 'Everyone with an account can sign in to this dashboard. A viewer reads everything and changes nothing, an operator can also pause sensors and acknowledge outages, and an administrator can change anything.',
  'users.new': 'New user',
  'users.edit': 'Edit user',
  'users.username': 'Username',
  'users.role': 'Role',
  'users.role.admin': 'Administrator',
  'users.role.operator': 'Operator',
  'users.role.viewer': 'Viewer',
  'users.role.adminHint': 'Can change everything: sensors, channels, settings, accounts and the API key.',
  'users.role.operatorHint': 'Can pause and resume sensors, run a probe now, acknowledge an outage and take the acknowledgement back.',
  'users.role.viewerHint': 'Can read everything and change nothing. The right role for a wall display.',
  'users.twoFactor': 'Two-factor',
  'users.twoFactorOn': 'on',
  'users.twoFactorOff': 'off',
  'users.lastLogin': 'Last sign-in',
  'users.never': 'never',
  'users.disabled': 'disabled',
  'users.enable': 'Enable',
  'users.disable': 'Disable',
  'users.reset': 'Reset password',
  'users.resetHint': 'Sets the password without knowing the old one, and signs that account out everywhere. Hand the new password over in person — nothing here sends a message.',
  'users.resetDone': 'Password set. That account has been signed out everywhere.',
  'users.created': 'Account created.',
  'users.updated': 'Account updated.',
  'users.deleted': 'Account removed.',
  'users.deleteConfirm': 'Remove the account "{name}"? Its outages stay in the history.',
  'users.you': 'this is you',
  'users.password': 'Password',
  'users.passwordHint': 'At least 8 characters.',
  'users.usernameTaken': 'That name is already taken.',
  'users.onlyOneAdmin': 'This is the last active administrator. Promote somebody else first.',

  // --------------------------------------------------------------- two-factor
  'totp.title': 'Two-factor authentication',
  'totp.off': 'Off. A password alone gets in.',
  'totp.on': 'On. A code from an authenticator app is required at sign-in.',
  'totp.why': 'The dashboard usually runs over plain HTTP on a home network, where a password can be read off the wire. A code that changes every thirty seconds cannot.',
  'totp.enable': 'Set up',
  'totp.disable': 'Switch off',
  'totp.renew': 'New recovery codes',
  'totp.scan': 'Scan this with your authenticator app.',
  'totp.orType': 'Or type the key in by hand:',
  'totp.code': 'Code from the app',
  'totp.confirm': 'Confirm',
  'totp.confirmFailed': 'That code does not match. Check the clock on the device and try the next one.',
  'totp.enabled': 'Two-factor authentication is on.',
  'totp.disabled': 'Two-factor authentication is off.',
  'totp.passwordPrompt': 'Your password, to confirm it is you:',
  'totp.wrongPassword': 'Wrong password.',
  'totp.codesLeft': '{n} recovery code(s) left',
  'totp.codesTitle': 'Your recovery codes',
  'totp.codesBody': 'Write these down now. Each one signs you in once, in place of the code from the app. This is the only time they are shown — if you lose them, make new ones from Settings.',
  'totp.codesCopy': 'Copy all',
  'totp.codesDone': 'I have written them down',
  'totp.codesRenewed': 'New recovery codes created. The old ones no longer work.',

  // ------------------------------------------------------------- example data
  // Groups. A group is a word typed on a sensor, so this screen is about the words
  // already in use rather than about creating one: there is no "new group" button,
  // and the name in the editor is printed rather than editable for the same reason.
  'groups.title': 'Groups',
  'groups.hint': 'A group exists because a sensor names it. What is set here is what its heading looks like on the dashboard.',
  'groups.empty': 'No groups yet. A group is made by typing its name into the group field of a sensor.',
  'groups.new': 'New group',
  'groups.newOption': 'New group…',
  'groups.newHint': 'Name a group before a sensor carries it. Its symbol and colour can be set here first.',
  'groups.name': 'Group name',
  'groups.nameRequired': 'A group needs a name.',
  'groups.editorTitle': 'Group "{name}"',
  'groups.editorNote': 'This is what the heading looks like. {count} sensor(s) carry this group. The name is written on the sensors and is not changed here.',
  'groups.colour': 'Heading colour',
  'groups.colourNone': 'None — the theme\'s own',
  'groups.order': 'Position in the order',
  'groups.orderHint': 'Lower numbers come first. Groups with the same number stand in alphabetical order.',
  'groups.dragHint': 'Drag to reorder',
  'groups.reordered': 'Group order saved.',
  'groups.forget': 'Forget',
  'groups.forgetConfirm': 'Forget the symbol and colour of "{name}"? Its {count} sensor(s) keep the group and the heading stays.',
  'groups.deleteTitle': 'Delete group "{name}"',
  'groups.deleteHint': 'A group is a word on its sensors. Choose what happens to them.',
  'groups.deleteKeep': 'Keep the sensors and remove the group label',
  'groups.deleteSelected': 'Delete selected sensors',
  'groups.deleteAll': 'Delete all {count} sensors',
  'groups.deleteConfirm': 'Delete group',
  'groups.deleted': 'Group deleted.',
  'groups.deleteNoSensors': 'No sensors carry this group.',
  'groups.deleteManaged': 'configuration file',
  'groups.deleteSelectedNone': 'Choose at least one sensor.',
  'groups.forgotten': 'Group settings removed.',
  'groups.saved': 'Group saved.',
  'tags.title': 'Tags',
  'tags.hint': 'A tag is a word in a sensor\'s tag list. What is set here is its symbol, its colour and its place in the order.',
  'tags.emptyBody': 'No tags yet. A tag is made in the sensor form, or with New tag.',
  'tags.new': 'New tag',
  'tags.newOption': 'New tag…',
  'tags.newHint': 'Name a tag before a sensor carries it. Its symbol and colour can be set here first.',
  'tags.name': 'Tag name',
  'tags.nameRequired': 'A tag needs a name.',
  'tags.editorTitle': 'Tag "{name}"',
  'tags.editorNote': 'This is what the chip looks like. {count} sensor(s) carry this tag. The word is written on the sensors and is not changed here.',
  'tags.colour': 'Chip colour',
  'tags.order': 'Position in the order',
  'tags.orderHint': 'Lower numbers come first. Tags with the same number stand in alphabetical order.',
  'tags.forget': 'Forget',
  'tags.forgetConfirm': 'Forget the symbol and colour of "{name}"? Its {count} sensor(s) keep the tag.',
  'tags.forgotten': 'Tag settings removed.',
  'tags.saved': 'Tag saved.',
  'tags.none': 'No tags',
  'tags.heading': 'Tags',
  'tags.search': 'Search tags',
  'tags.searchPlaceholder': 'Search tags',
  'tags.noMatch': 'No tag matches "{query}".',
  'tags.emptyPicker': 'No tags on any sensor yet.',
  'tags.count': '{count} sensor(s)',

  'examples.title': 'Example data',
  'examples.hint': 'The sensors this dashboard started with are examples: they point at real hosts, but they are not chosen by you and they say nothing about your network. The places they stand on are examples too.',
  'examples.count': '{sensors} example sensor(s), {results} stored probe(s) and {incidents} incident(s), at {sites} example place(s).',
  'examples.remove': 'Remove all example data',
  'examples.confirm': 'Remove {sensors} example sensor(s) together with their stored history and incidents, and the {sites} place(s) they stand on? This cannot be undone.',
  'examples.removed': 'Example data removed: {sensors} sensor(s), {results} probe(s), {incidents} incident(s), {sites} place(s).',
  'examples.none': 'No example data left. Everything on the dashboard was added by you.',
  'examples.restore': 'Load example data',
  'examples.confirmLoad': 'Load the example data? This adds the full example data set — sensors in their groups, at the places that come with it — with a week of invented history. Nothing you created is touched.',
  'examples.restored': 'Example data loaded: {sensors} sensor(s), {results} probe(s), {incidents} incident(s), {sites} place(s).',
  // The maintenance windows: the periods during which a sensor is expected to be
  // down. They sit beside the sites because they are the same kind of decision —
  // something an administrator settles in advance about the installation.
  'windows.title': 'Maintenance windows',
  'windows.hint': 'A sensor inside a window opens no incident and sends no alert, and the checks it fails in there do not count against its uptime. The check itself keeps running, so the chart still shows whether the box came back early.',
  'windows.emptyBody': 'No maintenance windows yet. Nothing is silenced.',
  'windows.new': 'New window',
  'windows.newTitle': 'New maintenance window',
  'windows.editTitle': 'Edit maintenance window',
  'windows.what': 'Applies to',
  'windows.when': 'When',
  'windows.note': 'Note',
  'windows.notePlaceholder': 'Weekly backup, firmware update …',
  'windows.state': 'State',
  'windows.running': 'Running now',
  'windows.waiting': 'Waiting',
  'windows.off': 'Off',
  'windows.managed': 'Declared in the configuration file',
  'windows.scope': 'Applies to',
  'windows.scopeSensors': 'One sensor',
  'windows.scopeGroups': 'A whole group',
  'windows.kind': 'Repeats',
  'windows.kindOnce': 'Once, between two times',
  'windows.kindWeekly': 'Every week, on chosen days',
  'windows.from': 'From',
  'windows.to': 'To',
  'windows.days': 'Days',
  'windows.everyDay': 'every day {span}',
  'windows.groupName': 'Group “{group}”',
  'windows.sensorGone': 'sensor #{id} (deleted)',
  'windows.badge': 'Maintenance',
  'windows.badgeHint': 'A maintenance window is running over this sensor: it is allowed to be away, no incident is open for it and these checks do not count against its uptime.',
  'windows.enabled': 'Active',
  'windows.enabledHint': 'A window that is off silences nothing and stays in the list.',
  'windows.created': 'Maintenance window added.',
  'windows.updated': 'Maintenance window saved.',
  'windows.deleted': 'Maintenance window removed.',
  'windows.deleteConfirm': 'Delete the maintenance window for {what}? The sensor is watched as usual from now on.',

  'examples.badge': 'Example',
  'examples.badgeHint': 'This sensor is example data, not one of yours. Settings can remove it.',

  // Sensor types, and what each one's target field asks for. The two are separate
  // entries because they are separate jobs: `type.<value>` names the kind of check
  // — "TCP port" — while `type.<value>.target` is the label over the box and says
  // what to type into it, which is almost never the same words.
  //
  // What follows `type.` is the name the server stores, so the pair is looked up
  // from a registry rather than written out at each use. TestEverySensorTypeHasAName
  // is what keeps a new type from shipping without one.
  'type.group.web': 'Web',
  'type.group.network': 'Network',
  'type.group.services': 'Services',
  'type.group.infra': 'Infrastructure',
  'type.http': 'HTTP(S)',
  'type.http.target': 'URL',
  'type.tcp': 'TCP port',
  'type.tcp.target': 'Host:port',
  'type.ssh': 'SSH banner',
  'type.ssh.target': 'Host or host:port',
  'type.smtp': 'SMTP greeting',
  'type.smtp.target': 'Host or host:port',
  'type.imap': 'IMAP greeting',
  'type.imap.target': 'Host or host:port',
  'type.udp': 'UDP port',
  'type.udp.target': 'Host:port',
  'type.redirect': 'HTTP redirect chain',
  'type.redirect.target': 'URL',
  'type.hop': 'ICMP hop',
  'type.hop.target': 'Host',
  'type.tls': 'TLS certificate',
  'type.tls.target': 'Host or host:port',
  'type.docker': 'Docker container',
  'type.docker.target': 'Container name or id',
  'type.snmp': 'SNMP',
  'type.snmp.target': 'Host or host:port',
  'type.ping': 'Ping (ICMP)',
  'type.ping.target': 'Host',
  'type.dns': 'DNS',
  'type.dns.target': 'Name',

  // Per-type probe options. An empty label would be a bug, so every option a
  // prober reads has an entry here.
  'opt.expected_status': 'Expected status',
  'opt.method': 'HTTP method',
  'opt.follow_redirects': 'Follow redirects',
  'opt.body_contains': 'Body contains',
  'opt.body_not_contains': 'Body must not contain',
  'opt.body_regex': 'Body matches regex',
  'opt.json_path': 'JSON value (path)',
  'opt.json_present': 'JSON value must not be null',
  'opt.json_match_key': 'Match field (for a list)',
  'opt.json_match_value': 'Match value',
  'opt.json_expect': 'JSON value must contain',
  'opt.json_min': 'JSON minimum',
  'opt.json_max': 'JSON maximum',
  'opt.headers': 'Request headers',
  'opt.expected_headers': 'Expected response headers',
  'opt.cert_warn_days': 'Warn when certificate expires in (days)',
  'opt.bearer_token': 'Bearer token',
  'opt.basic_auth_user': 'Basic auth user',
  'opt.basic_auth_pass': 'Basic auth password',
  'opt.insecure_skip_verify': 'Skip TLS verification',
  'opt.tls': 'Use TLS',
  'opt.server_name': 'TLS server name (SNI)',
  'opt.send': 'Send on connect',
  'opt.expect': 'Expect in reply',
  'opt.count': 'Pings per probe',
  'opt.record_type': 'Record type',
  'opt.resolver': 'Custom resolver',
  'opt.expected': 'Expected answers',
  'opt.expect_all': 'All expected answers must be present',
  'opt.socket': 'Docker socket',
  'opt.expect_running': 'Must be running',
  'opt.expect_health': 'Expected health status',
  'opt.expect_healthOff': 'only check that it runs',
  'opt.max_restarts': 'Restart limit',
  'opt.max_hops': 'Redirect limit',
  'opt.expected_final': 'Final URL contains',
  'opt.payload': 'Datagram payload',
  'opt.ttl': 'TTL',
  'opt.oid': 'Object (OID)',
  'opt.community': 'Community',
  'opt.version': 'SNMP version',
  'opt.versionDefault': '2c (default)',
  'opt.min': 'Minimum value',
  'opt.max': 'Maximum value',

  // SNMP v3. The form shows these only for the version and security level that
  // use them; the keys are here either way because the option reader asks for
  // every field's label.
  'opt.username': 'User name',
  'opt.security_level': 'Security level',
  'opt.securityLevelDefault': 'authPriv (default)',
  'opt.auth_protocol': 'Authentication',
  'opt.auth_pass': 'Authentication passphrase',
  'opt.priv_protocol': 'Privacy',
  'opt.priv_pass': 'Privacy passphrase',
  'opt.context_name': 'Context name',

  // The charts. A chart has no words of its own on screen, so what is translated is
  // its accessible name — read aloud by a screen reader in place of the picture —
  // and the line an empty one shows.
  'chart.uptimeAria': 'Uptime history',
  'chart.latencyAria': 'Response time over time',
  'chart.noData': 'No data',

  // Field help. These no longer sit under the form as paragraphs; they open
  // from the "?" beside each label, with a real value to pattern-match against.
  // The example for a key lives beside it as `<key>Example`.
  'help.what': 'What is this?',
  'help.example': 'Example',

  'help.sensor.name': 'Shown on every screen and in alert messages. Two sensors cannot share a name.',
  'help.sensor.nameExample': 'Living-room switch',
  'help.sensor.type': 'What is probed. Changing it swaps the options below for that kind of sensor.',
  'help.sensor.typeExample': 'http',
  'help.sensor.target': 'The address the probe is aimed at. Its shape depends on the type: a URL for HTTP, host:port for TCP, a host for a certificate, a bare host for ping, and the name of a container for Docker.',
  'help.sensor.targetExample': 'https://example.org/health',
  'help.sensor.interval': 'Seconds between probes. Five is the shortest any sensor may use.',
  'help.sensor.intervalExample': '60',
  'help.sensor.timeout': 'Seconds a single probe may take before it counts as failed.',
  'help.sensor.timeoutExample': '10',
  'help.sensor.retries': 'How many probes in a row may fail before the sensor counts as down and the first alert goes out. One means an alert on the first failure. Until the streak reaches it, a failing sensor reads Flaky.',
  'help.sensor.retriesExample': '3',
  'help.sensor.group': 'Optional. Sensors sharing a group are drawn under one heading on the dashboard.',
  'help.sensor.site': 'Optional. The place this sensor is: a rack, a room, a server at a hoster. It groups the tree and the wall by place, and with coordinates it is drawn on the map. The list is managed under Sites in Settings.',
  'help.sensor.groupExample': 'Network',
  'help.sensor.parent': 'While this sensor is down, the one here is excused: it opens no incident of its own, sends no alert, and the checks it fails in that time do not count against its uptime. The check itself keeps running, so a machine that comes back before its parent still shows it.',
  'help.sensor.tags': 'Comma-separated words you can search for, and that alert templates can use.',
  'help.sensor.tagsExample': 'lan, critical',
  'help.sensor.reminder': 'Seconds between repeat alerts while the sensor stays down. 0 turns reminders off, so only the first alert and the recovery message arrive.',
  'help.sensor.reminderExample': '3600',
  'help.sensor.channels': 'Where the alert goes when this sensor goes down. A sensor with no channel is still monitored and shown, and simply tells nobody.',
  'help.sensor.channelsExample': 'Homelab Telegram',
  'help.sensor.escalateAfter': 'If an incident is still open and unacknowledged after this many minutes, the escalation channels are told once. 0 switches escalation off. An acknowledgement stops it.',
  'help.sensor.escalateAfterExample': '15',
  'help.sensor.escalateChannels': 'Who hears about an outage that nobody acknowledged in time. The ordinary channels already heard about it; this is the second list.',
  'help.sensor.escalateChannelsExample': 'On-call phone',
  'help.sensor.enabled': 'Untick to stop probing. A paused sensor keeps its history and shows grey. Pausing is not acknowledging: acknowledging keeps the probing and stops only the reminders.',
  'help.sensor.enabledExample': 'ticked',

  'help.sites.icon': 'Optional. The symbol for this place, drawn beside its name in the tree and in the settings list. The map keeps the pin its kind gives it.',
  'help.sites.iconExample': 'Server',
  'help.groups.icon': 'Optional. The symbol drawn before the group\'s heading on the dashboard.',
  'help.groups.iconExample': 'Network',
  'help.groups.colour': 'Optional. The colour of the group\'s heading and of its symbol. Left empty, the heading keeps the colour the theme gives it.',
  'help.groups.colourExample': 'Amber',
  'help.tags.icon': 'Optional. The symbol drawn before the tag on a sensor.',
  'help.tags.iconExample': 'Tag',
  'help.tags.colour': 'Optional. The colour of the tag chip and of its symbol. Left empty, the chip keeps the colour the theme gives it.',
  'help.tags.colourExample': 'Amber',

  'help.opt.expected_status': 'Which HTTP status codes count as healthy: one code, a list, or a range.',
  'help.opt.expected_statusExample': '200-299',
  'help.opt.method': 'The HTTP method to use. HEAD asks for the headers only, which is cheaper when all you care about is that the page answers.',
  'help.opt.methodExample': 'GET',
  'help.opt.follow_redirects': 'Whether a redirect counts as success or as a failure. Leave it off to be told when a page starts redirecting and never arrives.',
  'help.opt.follow_redirectsExample': 'true',
  'help.opt.body_contains': 'Text that must appear in the response body — for checking an application is really up rather than merely answering.',
  'help.opt.body_containsExample': '"status": "ok"',
  'help.opt.body_not_contains': 'Text that must not appear. Catches an error page served with a 200.',
  'help.opt.body_not_containsExample': 'Fatal error',
  'help.opt.body_regex': 'A regular expression the body must match, for what a fixed string cannot express.',
  'help.opt.json_path': 'A dotted path into the JSON body, such as cpu.load or temperature.0.value. It is read after the status code; give it a json_expect, json_min or json_max to judge.',
  'help.opt.json_pathExample': 'cpu.load',
  'help.opt.json_present': 'Pass when the path is present and not null. This is the status check for an API that returns an object only while something is running.',
  'help.opt.json_presentExample': 'true',
  'help.opt.json_match_key': 'When the response is a list, the field used to find one item before the JSON path is read.',
  'help.opt.json_match_keyExample': 'name',
  'help.opt.json_match_value': 'The value of the match field that names the item this sensor is about.',
  'help.opt.json_match_valueExample': 'nginx',
  'help.opt.json_expect': 'Fail unless the extracted value contains this text. A boolean or a number is compared as its text.',
  'help.opt.json_expectExample': 'PASSED',
  'help.opt.json_min': 'Fail when the extracted JSON value is below this number.',
  'help.opt.json_minExample': '0',
  'help.opt.json_max': 'Fail when the extracted JSON value is above this number.',
  'help.opt.json_maxExample': '95',
  'help.opt.body_regexExample': '"version":\\s*"1\\.',
  'help.opt.headers': 'Extra request headers, one per line as Key: value. Leave empty to send none.',
  'help.opt.headersExample': 'X-Api-Key: abc123',
  'help.opt.expected_headers': 'Response headers that must be present, one per line as Key: value. Both the name and the value have to match.',
  'help.opt.expected_headersExample': 'Content-Type: application/json',
  'help.opt.cert_warn_days': 'Mark the sensor down when the TLS certificate has fewer days left than this. 0 switches the check off, but a certificate that has already expired is always a failure.',
  'help.opt.cert_warn_daysExample': '14',
  'help.opt.bearer_token': 'Sent as the Authorization: Bearer header.',
  'help.opt.bearer_tokenExample': 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9',
  'help.opt.basic_auth_user': 'Username for HTTP basic authentication. Set it together with the password.',
  'help.opt.basic_auth_userExample': 'monitor',
  'help.opt.basic_auth_pass': 'Password for HTTP basic authentication.',
  'help.opt.basic_auth_passExample': 's3cret',
  'help.opt.insecure_skip_verify': 'Accept a self-signed or otherwise unverifiable certificate. Only for hosts on your own network.',
  'help.opt.insecure_skip_verifyExample': 'false',
  'help.opt.tls': 'Open the connection over TLS. Turn it off for a plain-text service such as a raw Redis or SMTP port.',
  'help.opt.tlsExample': 'true',
  'help.opt.server_name': 'The name to check the certificate against, when it differs from the host you connect to.',
  'help.opt.server_nameExample': 'db.internal',
  'help.opt.send': 'Text to write as soon as the connection opens. Escape sequences such as \\r\\n are honoured.',
  'help.opt.sendExample': 'PING\\r\\n',
  'help.opt.expect': 'Text the reply must contain for the probe to pass. Leave empty to accept any reply.',
  'help.opt.expectExample': '+PONG',
  'help.opt.count': 'How many pings one probe sends. More pings is a steadier latency reading and a slower probe.',
  'help.opt.countExample': '3',
  'help.opt.record_type': 'The DNS record to ask for. The answer has to be of this type.',
  'help.opt.record_typeExample': 'A',
  'help.opt.resolver': 'Ask this server instead of the system resolver, as host:port.',
  'help.opt.resolverExample': '1.1.1.1:53',
  'help.opt.expected': 'Answers that count as correct, comma separated. Empty means any answer passes.',
  'help.opt.expectedExample': '93.184.215.14',
  'help.opt.expect_all': 'Require every expected answer. Otherwise one match is enough.',
  'help.opt.expect_allExample': 'false',
  'help.opt.socket': 'Path to the Docker socket on the host that runs the daemon. Leave empty for /var/run/docker.sock. The socket has to be mounted into the ProbeGlass container, which grants it far-reaching rights — see the security notes.',
  'help.opt.socketExample': '/var/run/docker.sock',
  'help.opt.expect_running': 'Fail the sensor when the container is not running. Set to No to watch a container that is supposed to be stopped: the sensor then alerts when it is up.',
  'help.opt.expect_runningExample': 'true',
  'help.opt.expect_health': 'The health status the container must report: healthy, starting or unhealthy. Most containers have no HEALTHCHECK and can never report healthy, so for those choose "only check that it runs" and the sensor watches whether the container is up instead.',
  'help.opt.expect_healthExample': 'healthy',
  'help.opt.max_restarts': 'Fail the sensor once the container has restarted more often than this. 0 switches the check off. The restart count is in the message either way.',
  'help.opt.max_restartsExample': '3',
  'help.opt.max_hops': 'How many redirects to follow before the probe gives up. A chain longer than this is reported as a failure, because a redirect loop has no final page.',
  'help.opt.max_hopsExample': '10',
  'help.opt.expected_final': 'A substring the address after the last redirect must contain. Use it to pin a domain move: the probe then notices when the chain ends somewhere else.',
  'help.opt.expected_finalExample': 'https://example.org/',
  'help.opt.payload': 'The bytes to send in the datagram. Escape sequences such as \\r\\n are honoured. Leave empty to send an empty datagram.',
  'help.opt.payloadExample': 'PING\\r\\n',
  'help.opt.ttl': 'The time-to-live to send with. 1 asks the first router, a larger number asks the router that many hops away, and a packet that reaches the destination answers as itself.',
  'help.opt.ttlExample': '1',
  'help.opt.oid': 'The object to ask for, as a dotted number. A name such as sysUpTime.0 would need a MIB file, and ProbeGlass does not guess at one.',
  'help.opt.oidExample': '1.3.6.1.2.1.1.3.0',
  'help.opt.community': 'The v2c community string: a shared password that travels unencrypted over the network on every request. It is stored on the server and shown as a marker here afterwards.',
  'help.opt.communityExample': 'public',
  'help.opt.version': '2c is what every device speaks. 1 is only for equipment old enough to refuse 2c. Version 3 adds a user, authentication and encryption; the fields it needs appear when you choose it.',
  'help.opt.versionExample': '2c',
  'help.opt.min': 'Fail the sensor when the value is below this. It needs a number: use an expected text for a value that is a string.',
  'help.opt.minExample': '0',
  'help.opt.max': 'Fail the sensor when the value is above this. Give min, max or an expected text — at least one, or the sensor has nothing to check the value against.',

  'help.opt.username': 'The user name configured on the device for SNMP v3.',
  'help.opt.usernameExample': 'monitor',
  'help.opt.security_level': 'noAuthNoPriv proves nothing, authNoPriv proves who you are, authPriv also encrypts the request.',
  'help.opt.security_levelExample': 'authPriv',
  'help.opt.auth_protocol': 'The authentication hash the device expects. SHA-256 or stronger is the modern choice.',
  'help.opt.auth_protocolExample': 'SHA-256',
  'help.opt.auth_pass': 'The authentication passphrase for that user. It is stored and masked in the API like a community string.',
  'help.opt.auth_passExample': 'a passphrase only the device and ProbeGlass know',
  'help.opt.priv_protocol': 'The encryption protocol for authPriv. AES is what current devices accept; DES is for old ones.',
  'help.opt.priv_protocolExample': 'AES',
  'help.opt.priv_pass': 'The privacy passphrase. It is stored and masked like the authentication passphrase.',
  'help.opt.priv_passExample': 'a different passphrase',
  'help.opt.context_name': 'Only for devices that put their data in a named SNMP context. Leave it empty otherwise.',
  'help.opt.context_nameExample': 'site-a',
  'help.opt.maxExample': '80',

  'help.ack.note': 'A word for whoever reads this later: what the outage was, who is on it, or which ticket covers it. Optional, and it stays with the outage.',
  'help.ack.noteExample': 'Known reboot, ticket 4711',

  // The guided tour. One entry per step; a step whose target is not on screen is
  // shown centred instead, and a step may name the view it belongs to and the
  // topic it answers, so this list can serve a simple dashboard and a full one
  // alike.
  'tour.aria': 'Guided tour',
  'tour.skip': 'Skip',
  'tour.back': 'Back',
  'tour.next': 'Next',
  'tour.finish': 'Done',
  'tour.welcome.title': 'Welcome to ProbeGlass',
  'tour.welcome.body': 'This is the dashboard, in about a minute. Everything on it right now is sample data — no real machine is being watched. Skip at any time; you can start the tour again from Settings.',
  // The same opening for an installation that has no sample data left, which is
  // what a tour restarted from Settings months later will find. Saying "everything
  // here is invented" over somebody's own sensors would be the one kind of wrong
  // this product cannot afford.
  'tour.welcome.bodyOwn': 'This is the dashboard, in about a minute. Skip at any time; you can start the tour again from Settings.',
  'tour.summary.title': 'The summary',
  'tour.summary.body': 'How many sensors are up, down or paused, plus uptime, average latency and probe count for the last day.',
  'tour.exampleIntro.title': 'Let us take one apart',
  'tour.exampleIntro.body': 'Take this card. It is invented, like the others the installation made: no real host was ever asked. It is shaped exactly like a real sensor, which is the point — here is what each part of it means.',
  'tour.exampleIntro.bodyOwn': 'Take this card — one of your own, so everything in it is real. It is shaped exactly like a probe result, and here is what each part of it means.',
  'tour.partName.title': 'Status and name',
  'tour.partName.body': 'The coloured dot is the state from the last probe: green up, yellow flaky, red down, grey paused. The name beside it is yours to choose, and it is what alerts and incident reports will call this sensor.',
  'tour.partTarget.title': 'The target',
  'tour.partTarget.body': 'What the probe actually talks to — a URL, a hostname, an address with a port. ProbeGlass never guesses this from the name; it is the one thing a sensor cannot work without.',
  'tour.partMessage.title': 'The last message',
  'tour.partMessage.body': 'The raw answer from the last probe, kept verbatim: the HTTP status, the resolver answer, the number of replies. When something breaks, this is usually the sentence that says why.',
  'tour.partSpark.title': 'Recent latency',
  'tour.partSpark.body': 'The last few hours of response times as a sparkline. Flat and low is healthy; a step up or a gap is the shape of a problem starting, and it is often visible here before it turns red.',
  'tour.partStats.title': 'Uptime at three ranges',
  'tour.partStats.body': 'Availability over the last day, week and month, and the average response time. A number that is high on 24h and low on 30d means something happened weeks ago and has been quiet since.',
  'tour.partActions.title': 'The buttons',
  'tour.partActions.body': 'Pause the sensor, probe it right now, or edit it. Silencing one of these is what the bell is for when it is down. Buttons you are not allowed to use are greyed out with the reason, never hidden.',
  'tour.detail.title': 'The full history',
  'tour.detail.body': 'A click on the card opens the detail view: a range picker, the response-time curve, a band of uptime strips, and the table of individual results behind the numbers. Nothing here is aggregated away — you can always get back to the single probe.',
  'tour.newSensor.title': 'Adding a sensor',
  'tour.newSensor.body': 'Everything starts here. Choose a type, give it a target and an interval, and press Test before saving — it catches a typo before it becomes a monitored sensor.',
  'tour.tabs.title': 'Four views',
  'tour.tabs.body': 'Dashboard for the current state, Incidents for the outages, Alerts for where the messages go, and Settings for everything else.',
  'tour.layout.title': 'Five ways to look at it',
  'tour.layout.body': 'The same sensors, laid out as cards, as a list, as a tree you can fold away by place and group, as a wall of tiles with the last checks on each one, or on a map. It is one setting — it follows you to every device you sign in from.',
  'tour.userMenu.title': 'You, and where you get out',
  'tour.userMenu.body': 'Top right: who is signed in and with which role. The menu behind it holds the switches people change often — theme, density, language — a way into Settings, and Sign out, which used to be buried at the bottom of the settings page.',
  'tour.settingsExamples.title': 'Removing the sample data',
  'tour.settingsExamples.body': 'This is the one-click way out of the demo: it deletes the sensors that came with the install, together with their history and incidents. It is not reversible, and it only ever touches the sensors ProbeGlass created itself — anything you added stays.',
  'tour.settingsUsers.title': 'People and roles',
  'tour.settingsUsers.body': 'One account per person, each with a role: a viewer may read everything and change nothing, an operator may also acknowledge outages and pause sensors, an admin may do everything. A second factor is set up per account, with single-use recovery codes for the day the phone is gone.',
  'tour.choose.title': 'That is the tour',
  'tour.choose.body': 'If you would like a little more on one of these, pick one. Otherwise carry on with Done.',
  'tour.choose.sensors': 'Sensors',
  'tour.choose.incidents': 'Incidents',
  'tour.choose.alerts': 'Alerts',
  'tour.topicSensors.title': 'Sensors are the unit of everything',
  'tour.topicSensors.body': 'A sensor is one question asked of one target on a fixed interval. History, incidents and alerts all hang off it, and the answer to "is my homelab healthy?" is nothing more than the state of this list.',
  'tour.topicSensorsAdd.title': 'Types worth knowing',
  'tour.topicSensorsAdd.body': 'HTTP for a web page or an API, TCP for a bare port, Ping for reachability, DNS for name resolution — plus the options that go with them: expected status codes, certificate expiry warnings, record types, retries and timeouts.',
  'tour.topicIncidents.title': 'Incidents',
  'tour.topicIncidents.body': 'Every outage with when it began, how long it lasted and why. An ongoing one can be acknowledged, which silences the reminders without pretending it is fixed — and the incident keeps its note and the name of whoever acknowledged it.',
  'tour.topicAlerts.title': 'Alert channels',
  'tour.topicAlerts.body': 'Telegram, Discord, Matrix, ntfy, Slack or a webhook — one channel per place you want to hear about it. The page lists whether each channel last delivered successfully, so a broken notification path is visible rather than silent.',
  'tour.topicAlertsAttach.title': 'Attaching a channel',
  'tour.topicAlertsAttach.body': 'A channel does nothing until a sensor points at it. Add one here, then choose it while editing a sensor; several sensors may share it, and one sensor may use several, each with its own trigger.',
  'tour.done.title': 'That is the whole thing',
  'tour.done.body': 'Remember that all of this was sample data. When you are ready for real sensors, remove the four examples in Settings with one click and add your own — and enjoy the quiet.',

  'common.save': 'Save',
  'common.cancel': 'Cancel',
  'common.generate': 'Generate',
  'common.generatedCopied': 'Generated and copied — store it on the device.',
  'common.generatedManual': 'Generated — copy it from the field and store it on the device.',
  'common.close': 'Close',
  'common.delete': 'Delete',
  'common.retry': 'Retry',
  'common.error': 'Something went wrong',
  'common.timeout': 'The server did not answer in time. Check that ProbeGlass is still running, then try again.',
  'common.saved': 'Saved',
  'common.optional': 'optional',
  'common.required': 'required',
  'common.edit': 'Edit',
  'common.confirm': 'Confirm',
  'common.none': 'None',
  'common.all': 'All',
  'common.seconds': 's',
  'common.yes': 'Yes',
  'common.no': 'No',
  'common.needsRole': 'This needs the {role} role.',
};

const catalogues = { en, de };

// `available` drives the language picker. Add a code here once its catalogue
// above is complete, and give it a name in `names`.
export const available = ['en', 'de'];

// Every language's name in that language, which is what the picker offers.
//
// A reader who cannot read the interface as it stands is exactly the reader who
// needs to find their own language in that list, and "German" is no help to
// somebody who only knows "Deutsch". The code is the fallback, because a picker
// entry with no name at all is worse than an ugly one, and a missing entry here
// is a bug either way — `available` and `names` are written side by side so they
// are hard to let drift apart.
const names = { en: 'English', de: 'Deutsch' };

/** nameOf is what the language picker writes in its options. */
export function nameOf(code) {
  return names[code] || code.toUpperCase();
}

let current = 'en';

export function setLanguage(code) {
  if (catalogues[code]) current = code;
  document.documentElement.lang = current;
}

export function language() {
  return current;
}

/**
 * locale is the tag the browser's own formatters are given — dates, times and
 * numbers.
 *
 * It exists because they do not follow the document's `lang`: `Intl` reads the
 * browser's own language, so a dashboard switched to German on an English
 * machine would translate its sentences and leave every date beside them in
 * English. Handing the chosen language to every formatter is what makes the
 * setting mean the whole interface and not just its words.
 */
export function locale() {
  return current;
}

/**
 * Translate a key. Placeholders in the catalogue are written as {name} and
 * filled from `vars`. A missing key returns the key itself, which makes the gap
 * obvious in the dashboard instead of silently rendering nothing.
 */
export function t(key, vars) {
  const catalogue = catalogues[current] || catalogues.en;
  let text = catalogue[key] ?? catalogues.en[key] ?? key;
  if (vars) {
    for (const [name, value] of Object.entries(vars)) {
      text = text.replaceAll(`{${name}}`, String(value));
    }
  }
  return text;
}

/** Re-render every element carrying a data-i18n* attribute. */
export function applyTranslations(root = document) {
  for (const el of root.querySelectorAll('[data-i18n]')) {
    el.textContent = t(el.dataset.i18n);
  }
  for (const el of root.querySelectorAll('[data-i18n-placeholder]')) {
    const text = t(el.dataset.i18nPlaceholder);
    el.placeholder = text;
    el.setAttribute('aria-label', text);
  }
  for (const el of root.querySelectorAll('[data-i18n-title]')) {
    el.title = t(el.dataset.i18nTitle);
  }
  for (const el of root.querySelectorAll('[data-i18n-aria]')) {
    el.setAttribute('aria-label', t(el.dataset.i18nAria));
  }
}
