// The search box in the top bar.
//
// One field, and it looks in everything. It used to be called "Search sensors"
// and it did exactly that, which is the wrong promise: somebody who types a word
// into a field labelled "Search" on a monitoring dashboard is asking where that
// word is, and an answer that quietly leaves out the channel that stopped sending
// and the probe message from last Tuesday is worse than no answer at all. What is
// searched, and what is deliberately never searched — probe options, channel
// configuration, every credential column — is decided by the server, in
// internal/store/search.go. This file is the box and the list.
//
// It is a leaf module, like user.js: it imports the catalogue and the escape
// helper and nothing else. Everything it needs from the dashboard — the state,
// opening a sensor, changing the view — arrives as an option, which is what keeps
// the dependency arrows one-way and the module testable on its own.

import { t } from './i18n.js';
import { esc } from './charts.js';

// The same bounds the server applies. They are repeated rather than fetched: the
// browser has to decide on every keystroke whether to ask at all, and a round trip
// to be told "too short" is a round trip per character.
const MIN_RUNES = 2;
const MAX_RUNES = 64;

// A quarter of a second. Shorter and a fast typist sends a request per character;
// longer and the list lags visibly behind the cursor.
const DEBOUNCE_MS = 250;

// The panels of the settings page, keyed by the `data-panel` each one carries,
// with the category it stands in and the name it is shown under. Settings are the
// one kind of hit that is not a database row: a panel is a part of a page, so this
// table is the whole of what there is to search, and it lives here rather than on
// the server because the server has no opinion about where a page is divided.
//
// The category is here because the settings page shows one of them at a time: a
// hit for Backup is a hit on a panel that is not in the document until the Backup
// tab has been chosen, so knowing where a panel lives is part of knowing how to
// get to it.
const SETTINGS_PANELS = [
  ['appearance', 'look', 'settings.appearance'],
  ['dashboard', 'monitor', 'settings.dashboard'],
  ['widgets', 'widgets', 'settings.widgets'],
  ['maintenance', 'monitor', 'windows.title'],
  ['sites', 'monitor', 'sites.title'],
  ['map', 'look', 'settings.map'],
  ['examples', 'monitor', 'examples.title'],
  ['access', 'access', 'settings.security'],
  ['users', 'access', 'users.title'],
  ['backup', 'backup', 'settings.backup'],
  ['help', 'help', 'settings.help'],
  ['diagnostics', 'help', 'settings.diagnostics'],
  ['about', 'help', 'settings.about'],
];

// One search box at a time, module-wide.
let current = null; // { input, panel, opts, timer, seq, hits, index, query, local }

/**
 * mount wires the field and its result list. Called once, from app.js.
 *
 * `opts` carries the dashboard's own state and the things a hit can do:
 * `state()` returns the live state object, `openDetail(id)` opens a sensor,
 * `setRoute(route)` changes the view, `can(role)` answers a permission question
 * and `showCategory(key)` opens a category of the settings page. Nothing here
 * reaches into app.js.
 */
export function mount(input, panel, opts) {
  if (!input || !panel) return;
  current = {
    input,
    panel,
    opts,
    timer: null,
    seq: 0,
    hits: [],
    local: [],
    index: -1,
    query: '',
    truncated: false,
    error: '',
  };

  input.addEventListener('input', onInput);
  input.addEventListener('keydown', onKeyDown);
  // Re-opening on focus, without a keystroke, is what makes the box feel like a
  // search rather than a form field: click away to read something, click back,
  // and the list you were reading is still there.
  input.addEventListener('focus', () => {
    if (current.query.length >= MIN_RUNES && current.hits.length) open();
  });
  panel.addEventListener('click', onPanelClick);
  // Keeping the field focused while the list is clicked matters: the click would
  // otherwise blur it, and a blur handler that closed the list would close it in
  // the act of choosing from it.
  panel.addEventListener('mousedown', (event) => event.preventDefault());

  document.addEventListener('click', (event) => {
    if (current && !event.target.closest('.search')) close();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && !panel.hidden) {
      close();
      input.focus();
    }
  });
  window.addEventListener('resize', () => {
    if (current && !panel.hidden) render();
  });
}

/** The query as it should be sent: trimmed, and cut at the server's ceiling. */
function normalise(raw) {
  return String(raw || '').trim().slice(0, MAX_RUNES);
}

function onInput() {
  const query = normalise(current.input.value);
  current.query = query;
  current.index = -1;
  current.error = '';
  current.truncated = false;
  clearTimeout(current.timer);

  if (query.length < MIN_RUNES) {
    // Nothing typed yet, or nothing worth asking about: show the hint rather than
    // an empty box, and do not send a request that can only come back empty.
    current.hits = [];
    current.local = [];
    render();
    return;
  }

  // What is already in the browser is shown at once — the sensors, sites,
  // channels, accounts and settings on this page need no round trip — and the
  // server's answer replaces the list a moment later. The list a person reads
  // while typing is therefore never empty, and never waits.
  current.local = localHits(query);
  current.hits = current.local.slice();
  render();
  current.timer = setTimeout(() => void ask(query), DEBOUNCE_MS);
}

/**
 * ask sends the query and replaces the list with the answer.
 *
 * The counter is what makes a late answer harmless. Two requests are in flight
 * whenever somebody types faster than the server answers — "rou" and "router" —
 * and without a sequence number the older one can land second and put the answer
 * to a prefix on screen under a cursor that has moved on. Only the newest answer
 * is drawn; every other one is dropped on the floor.
 */
async function ask(query) {
  if (!current) return;
  const seq = ++current.seq;
  try {
    const answer = await current.opts.search(query);
    if (!current || seq !== current.seq || current.query !== query) return;
    // The local list is kept and the server's kinds are replaced wholesale rather
    // than merged: a sensor that the server found is the same sensor the browser
    // derived, and showing both would be a list where every sensor appears twice.
    // Settings are not database rows and never come from the server, so that half
    // of the local list always survives.
    const settings = current.local.filter((hit) => hit.kind === 'setting');
    current.hits = (answer.hits || []).concat(settings);
    current.truncated = Boolean(answer.truncated);
    render();
  } catch (err) {
    if (!current || seq !== current.seq) return;
    // The local half of the list is still worth reading, so the failure does not
    // clear it — it is said underneath it instead.
    current.error = err.message;
    render();
  }
}

// ---------------------------------------------------------------------------
// what the browser already knows
// ---------------------------------------------------------------------------

/**
 * localHits is everything searchable without asking anybody.
 *
 * The fields are the ones the dashboard's own filter uses, so that typing in the
 * box narrows the cards and fills this list with the same set of sensors. The
 * live status is thrown in as well: it is the one thing a row here can say that
 * the database cannot, because it lives in the scheduler's memory.
 */
function localHits(query) {
  const state = current.opts.state() || {};
  const needle = query.toLowerCase();
  const has = (...parts) => parts.filter(Boolean).join(' ').toLowerCase().includes(needle);

  const sensors = (state.sensors || [])
    .filter((s) => has(s.name, s.target, s.type, s.group, s.site, ...(s.tags || [])))
    .map((s) => ({
      kind: 'sensor',
      ref: String(s.id),
      title: s.name,
      sub: [s.target, s.site].filter(Boolean).join(' · '),
      state: s.status,
    }));

  const sites = (state.sites || [])
    .filter((s) => has(s.name, s.kind))
    .map((s) => ({ kind: 'site', ref: s.name, title: s.name, sub: s.kind }));

  const channels = (state.channels || [])
    .filter((c) => has(c.name, c.type))
    .map((c) => ({
      kind: 'channel',
      ref: String(c.id),
      title: c.name,
      sub: c.type,
      state: c.enabled ? '' : 'paused',
    }));

  // Accounts are an administrator's list. The dashboard only ever holds them for
  // an administrator, so a viewer's state.users is empty and this is a no-op —
  // but the check is written out rather than left implicit, because "it is empty
  // anyway" is the kind of reasoning that stops being true.
  const users = current.opts.can('admin')
    ? (state.users || [])
      .filter((u) => has(u.username, u.role))
      .map((u) => ({
        kind: 'user',
        ref: u.username,
        title: u.username,
        sub: t(`users.role.${u.role}`),
        state: u.disabled ? 'disabled' : '',
      }))
    : [];

  const settings = SETTINGS_PANELS
    .map(([panel, group, key]) => ({ panel, group, title: t(key) }))
    .filter((s) => s.title.toLowerCase().includes(needle))
    .map((s) => ({ kind: 'setting', ref: s.panel, group: s.group, title: s.title }));

  return sensors.concat(sites, channels, users, settings).slice(0, 40);
}

// ---------------------------------------------------------------------------
// drawing
// ---------------------------------------------------------------------------

/** The order the sections appear in: what was asked for, not what was found. */
const KIND_ORDER = ['sensor', 'site', 'channel', 'incident', 'result', 'user', 'setting'];

function open() {
  if (current.panel.hidden) {
    current.panel.hidden = false;
    current.input.setAttribute('aria-expanded', 'true');
  }
}

function close() {
  if (!current || current.panel.hidden) return;
  current.panel.hidden = true;
  current.input.setAttribute('aria-expanded', 'false');
  current.index = -1;
}

function render() {
  const { panel, query } = current;
  // Nothing typed: the field is not a place to explain the product, so the panel
  // stays closed rather than becoming a permanent tutorial.
  if (query.length < MIN_RUNES) {
    close();
    return;
  }
  open();

  const groups = KIND_ORDER
    .map((kind) => ({ kind, hits: current.hits.filter((hit) => hit.kind === kind) }))
    .filter((g) => g.hits.length);

  if (!groups.length && !current.error) {
    panel.innerHTML = `<p class="search-note">${esc(t('search.empty', { query }))}</p>`;
    return;
  }

  let index = -1;
  const body = groups.map((group) => `
    <div class="search-group" role="group" aria-label="${esc(t(`search.group.${group.kind}`))}">
      <p class="search-group-head">${esc(t(`search.group.${group.kind}`))}</p>
      ${group.hits.map((hit) => {
        index += 1;
        return hitMarkup(hit, index);
      }).join('')}
    </div>`).join('');

  const note = current.error
    ? `<p class="search-note form-error">${esc(current.error)}</p>`
    : (current.truncated ? `<p class="search-note">${esc(t('search.more'))}</p>` : '');

  panel.innerHTML = body + note;
  // Clamping after the list changed: the cursor was on an index that may not
  // exist any more, and an index pointing past the end would make the next Enter
  // do nothing at all.
  const total = current.hits.length;
  if (current.index >= total) current.index = total - 1;
  if (current.index >= 0) mark();
}

function hitMarkup(hit, index) {
  const label = stateOf(hit.state);
  const state = label ? `<span class="pill ${label.cls}">${esc(label.text)}</span>` : '';
  // A hit that is a probe message or an outage has no screen of its own: both are
  // read in the context of the sensor they belong to. The row says "Open sensor"
  // instead of promising a precision it does not have.
  const action = hit.kind === 'incident' || hit.kind === 'result' ? t('search.openSensor') : '';
  return `
    <button type="button" class="search-hit${index === current.index ? ' is-active' : ''}"
            data-index="${index}" role="option" aria-selected="${index === current.index}">
      <span class="search-hit-main">
        <span class="search-hit-title">${esc(hit.title)}</span>
        ${hit.sub ? `<span class="search-hit-sub muted">${esc(hit.sub)}</span>` : ''}
      </span>
      <span class="search-hit-side">
        ${state}
        ${action ? `<span class="muted small">${esc(action)}</span>` : ''}
      </span>
    </button>`;
}

/**
 * stateOf turns what a row knows about itself into a pill, or nothing.
 *
 * "Up" gets no pill. Every working sensor in the installation is up, and a badge
 * on almost every row is a badge that stops being read; what the list is for is
 * finding the ones that are not. The rest are named, and the class is the one the
 * dashboard already uses for that meaning — an outage is `st-down` and a resolved
 * one is neutral, not "up", because a closed outage is history rather than health.
 */
function stateOf(state) {
  switch (state) {
    case 'down': return { text: t('status.down'), cls: 'st-down' };
    case 'warn': return { text: t('status.warn'), cls: 'st-warn' };
    case 'paused': return { text: t('status.paused'), cls: 'st-paused' };
    case 'unknown': return { text: t('status.unknown'), cls: 'st-unknown' };
    case 'disabled': return { text: t('users.disabled'), cls: 'st-muted' };
    case 'open': return { text: t('incidents.ongoing'), cls: 'st-down' };
    case 'resolved': return { text: t('incidents.resolved'), cls: 'st-muted' };
    default: return null;
  }
}

// ---------------------------------------------------------------------------
// keyboard and clicks
// ---------------------------------------------------------------------------

function onKeyDown(event) {
  if (current.panel.hidden) return;
  const total = current.hits.length;
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    if (!total) return;
    event.preventDefault();
    const step = event.key === 'ArrowDown' ? 1 : -1;
    // Wrapping, so that a list with the cursor on the last row does not swallow
    // the next press. Starting from -1 in either direction lands on an end.
    current.index = current.index < 0
      ? (step > 0 ? 0 : total - 1)
      : (current.index + step + total) % total;
    mark();
    return;
  }
  if (event.key === 'Enter') {
    // Enter with nothing chosen takes the first row, which is what the box is
    // for: type three letters, press Enter, be there.
    const hit = current.hits[current.index >= 0 ? current.index : 0];
    if (!hit) return;
    event.preventDefault();
    go(hit);
  }
}

/** Move the highlight without rebuilding the list on every arrow press. */
function mark() {
  for (const button of current.panel.querySelectorAll('.search-hit')) {
    const active = Number(button.dataset.index) === current.index;
    button.classList.toggle('is-active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
    if (active) button.scrollIntoView({ block: 'nearest' });
  }
}

function onPanelClick(event) {
  const button = event.target.closest('.search-hit');
  if (!button) return;
  const hit = current.hits[Number(button.dataset.index)];
  if (hit) go(hit);
}

// ---------------------------------------------------------------------------
// jumping
// ---------------------------------------------------------------------------

/**
 * go takes the person to what they picked.
 *
 * The list closes first, in every case: whatever happens next — a dialog over the
 * dashboard, a different tab — the search box is not where they are any more, and
 * a panel left hanging over the page it just navigated away from is a stray
 * element nobody can dismiss.
 */
function go(hit) {
  const { openDetail, setRoute } = current.opts;
  close();
  current.input.blur();

  switch (hit.kind) {
    case 'sensor':
    case 'incident':
    case 'result':
      // An outage or a probe message opens the sensor it belongs to. That is where
      // the message is read in its context, and the incident view has no anchor
      // for a single row to hang from.
      openDetail(Number(hit.ref));
      return;
    case 'channel':
      // Channels live at the top of the Alerts tab; there is no per-channel anchor,
      // and the list is short enough to scan in one look.
      setRoute('channels');
      return;
    case 'site':
      setRoute('settings');
      scrollToPanel('sites', 'monitor');
      return;
    case 'user':
      // Accounts are an administrator's panel; everyone else is sent to the one
      // they can see, which is the same category.
      setRoute('settings');
      scrollToPanel(current.opts.can('admin') ? 'users' : 'access', 'access');
      return;
    case 'setting':
      setRoute('settings');
      scrollToPanel(hit.ref, hit.group);
      return;
    default:
      return;
  }
}

/**
 * scrollToPanel waits for the settings page to be drawn, switches to the
 * category the panel lives in, and rings it.
 *
 * It has to wait: changing the view loads what the page needs over the network
 * before it draws anything, so the panel is not in the document at the moment the
 * click happens. Rather than guess a fixed delay — wrong on a slow server and
 * wasteful on a fast one — the panel is looked for frame by frame, with a bound
 * so that a page that never draws does not leave a loop running behind it.
 *
 * Switching goes through `showCategory`, which is the dashboard's own function
 * and not something local: it is what remembers the category on the state, and
 * the next redraw of the settings page reads that. Hiding the panel here would
 * hold until the next poll and then quietly put the person back on Look, with the
 * hit they chose invisible again.
 */
function scrollToPanel(panel, group, attempts = 40) {
  const found = document.querySelector(`[data-panel="${panel}"]`);
  if (found) {
    current.opts.showCategory?.(group);
    found.scrollIntoView({ block: 'start', behavior: prefersReducedMotion() ? 'auto' : 'smooth' });
    found.classList.add('is-flash');
    setTimeout(() => found.classList.remove('is-flash'), 1200);
    return;
  }
  if (attempts <= 0) return;
  requestAnimationFrame(() => scrollToPanel(panel, group, attempts - 1));
}

function prefersReducedMotion() {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}
