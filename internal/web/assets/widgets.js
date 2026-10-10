// The dashboard header, built out of widgets.
//
// The header used to be one fixed strip: a ring with four counts and four
// figures. That is a good header, but it is only one of them — the person who
// wants a clock, the last few outages, or the three slowest sensors had nowhere
// to put them. So the strip became a list of widgets, and every one of them is
// the same object: an id, a drawing, and an optional live part.
//
// It is a leaf module, like search.js and tour.js. It imports the catalogue, the
// chart helpers and the map, and never app.js — everything it needs from the
// dashboard (the state, opening a sensor, changing a view, saving the list)
// arrives as an option. That keeps the dependency arrows one-way and lets this
// file be read without the other two thousand lines.
//
// Which widgets are shown, and in what order, is one value: the list of ids. It
// is stored on the account as a comma-separated string, so selection and order
// travel together — there is no way to have one without the other, which is
// exactly right, because they are the same decision.

import { t } from './i18n.js';
import * as C from './charts.js';
import * as mapUI from './map.js';

const esc = C.esc;

// ---------------------------------------------------------------------------
// the registry
// ---------------------------------------------------------------------------

/**
 * A figure with a label — the shape the four numbers have always had.
 *
 * A figure tile is wider than one number needs, so the width is spent on the two
 * things a bare number cannot say: whether it is normal (`sub`, the same measure
 * over a week) and which way it has been going — `spark`, the last day drawn as a
 * curve, or `band`, the same day drawn as one bar per hour. Both are optional, and
 * a tile without either is still a whole tile: "monitoring since" has nothing to be
 * compared with.
 *
 * The drawing has a line of its own rather than sitting beside the number, which is
 * where it was first put: at two columns a big number plus a drawing is wider than
 * the tile, and the one that has to give way is the drawing. Underneath, it is as
 * wide as the tile and reads as the shape of the number above it, which is what it
 * is there to say.
 *
 * The label carries the window ("Uptime 24h", not "Uptime"), because a number
 * whose period is only in the reader's head is a number that will be misread once
 * and distrusted afterwards.
 */
function figure(labelKey, value, { sub = '', spark = '', band = '', small = false } = {}) {
  const draw = spark || band;
  return `<div class="stat">
    <span class="stat-label">${esc(t(labelKey))}</span>
    <span class="stat-value${small ? ' small' : ''}">${esc(value)}</span>
    ${sub ? `<span class="stat-sub muted">${esc(sub)}</span>` : ''}
    ${draw ? `<span class="stat-spark" aria-hidden="true">${draw}</span>` : ''}
  </div>`;
}

/**
 * weekLine is the comparison under a figure: the same measure over a week.
 *
 * It is built here rather than by each widget so that all three read alike, and
 * it disappears rather than printing a zero when the week is not known — a week
 * that could not be read is not a week of nothing.
 */
function weekLine(format, value) {
  if (!(value > 0)) return '';
  return `${t('summary.weekShort')} ${format(value)}`;
}

/**
 * One measure of the header's own day, as a curve. '' when there is no series.
 *
 * It is drawn stretched: the width it is handed is a shape, not a scale, and the
 * curve is meant to fill the tile it is put in rather than sit in the middle of it
 * at whatever width it was drawn at. The one thing that must not stretch is a
 * number, and no number is in here.
 */
function dayCurve(state, pick) {
  const series = day(state);
  if (!series) return '';
  return C.sparkline(series.map(pick), { width: 120, height: 24, stretch: true, fit: true });
}

/**
 * The same day as a band of hours, one bar each, coloured by how the hour went.
 *
 * Uptime is the one measure in the header that a curve cannot draw: it is a
 * percentage that sits at a hundred nearly all the time, and a line pinned to the
 * top edge of the tile is a line that says nothing — it reads as a border, or as a
 * widget that failed to draw. A band of hours says the same thing and says it at a
 * glance: a row of green is a day with nothing wrong in it, and one red bar is one
 * bad hour, visible without being looked for. It is the same picture the sensor
 * cards use for the same reason, so the header and the cards cannot teach two
 * different readings of the same day.
 */
function dayBand(state) {
  const series = day(state);
  if (!series) return '';
  const points = series.map((p) => ({
    ts: p.ts,
    total: p.probes,
    ok: Math.round((p.uptime / 100) * p.probes),
    uptime_pct: p.uptime,
  }));
  return C.uptimeStrip(points, { width: 120, height: 24 });
}

/** The header's own day, or null when there is too little of it to draw. */
function day(state) {
  const series = (state.summary && state.summary.series) || [];
  return series.length < 4 ? null : series;
}

/**
 * A short list: a heading and rows. Used by the three list widgets so that they
 * are one piece of markup rather than three that drift apart.
 *
 * A row may be a button when there is somewhere to go. It is a button rather
 * than a div with a handler because a row that navigates is a control, and a
 * control that cannot be reached with the Tab key is a control half the people
 * cannot use.
 */
function listBlock(titleKey, rows, emptyKey) {
  const body = rows.length
    ? `<ul class="widget-list">${rows.join('')}</ul>`
    : `<p class="muted small">${esc(t(emptyKey))}</p>`;
  return `<div class="widget-card">
    <p class="widget-head">${esc(t(titleKey))}</p>
    ${body}
  </div>`;
}

function row(target, id, left, right, sub) {
  const inner = `
    <span class="widget-row-main">
      <span class="widget-row-title">${esc(left)}</span>
      ${sub ? `<span class="widget-row-sub muted">${esc(sub)}</span>` : ''}
    </span>
    ${right ? `<span class="widget-row-side muted">${esc(right)}</span>` : ''}`;
  if (!id) return `<li class="widget-row">${inner}</li>`;
  return `<li><button type="button" class="widget-row" data-widget-sensor="${id}">${inner}</button></li>`;
}

// The widgets, in the order the picker offers them.
//
// `size` is how much of the strip a tile takes, out of the twelve columns the
// strip is divided into (see `.widgets` in style.css). Three sizes, and they are
// about what is inside the tile rather than about taste: a figure is a number and
// a label and needs no more than two columns; a hero is the ring, which is the one
// tile whose whole job is to be looked at first, and takes four; everything with a
// list, a map or a row of buttons in it takes four because a list of five rows
// needs the width to stay five rows.
//
// `build` draws the body and is called again on every refresh, so it must be
// pure — no listeners, no side effects, nothing kept between calls. Anything
// that needs to stay (the map, the second hand) is mounted by `refresh` from
// outside the body, which is why the bodies are replaceable at all.
const WIDGETS = [
  {
    id: 'status',
    size: 'hero',
    icon: '<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M8 2a6 6 0 0 1 0 12Z" fill="currentColor"/></svg>',
    build({ state }) {
      const s = state.summary;
      if (!s) return `<div class="summary-card"><p class="muted">${esc(t('widgets.waiting'))}</p></div>`;
      const donut = C.donut([
        { value: s.up, cls: 's-ok', label: t('status.up') },
        { value: s.down, cls: 's-crit', label: t('status.down') },
        { value: s.unknown, cls: 's-unknown', label: t('status.unknown') },
        { value: s.paused, cls: 's-paused', label: t('status.paused') },
      ], { size: 96, thickness: 12 });
      const counts = [
        ['status.up', s.up, 's-ok'],
        ['status.down', s.down, 's-crit'],
        ['status.unknown', s.unknown, 's-unknown'],
        ['status.paused', s.paused, 's-paused'],
      ].filter(([, value]) => value > 0)
        .map(([key, value, cls]) => `<li class="count ${cls}"><span class="count-n">${value}</span> ${esc(t(key).toLowerCase())}</li>`)
        .join('');
      return `<div class="summary-card">
        <div class="summary-ring">${donut}</div>
        <ul class="summary-counts">${counts || `<li class="muted">${esc(t('common.none'))}</li>`}</ul>
      </div>`;
    },
  },
  {
    id: 'uptime',
    size: 'fig',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.5 8.5 6 12l7.5-8"/></svg>',
    build: ({ state }) => figure('summary.uptime',
      state.summary ? C.fmtPct(state.summary.uptime_24h) : '—', {
        sub: state.summary ? weekLine(C.fmtPct, state.summary.uptime_7d) : '',
        band: dayBand(state),
      }),
  },
  {
    id: 'latency',
    size: 'fig',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2"/><path d="M8 8l3-2.4"/></svg>',
    build: ({ state }) => figure('summary.latency',
      state.summary ? C.fmtLatency(state.summary.avg_latency_ms) : '—', {
        sub: state.summary ? weekLine(C.fmtLatency, state.summary.avg_latency_7d_ms) : '',
        spark: dayCurve(state, (p) => p.latency_ms),
      }),
  },
  {
    id: 'probes',
    size: 'fig',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M1.5 8h3l2-4.5 3 9 2-4.5h3"/></svg>',
    // No drawing, and deliberately: how many probes an hour is decided by the
    // schedule and not by the world, so the curve is a straight line on every
    // installation that is running — and a straight line in a header reads as a
    // widget that failed to draw. The figure and the week beside it are the whole
    // of what there is to say about a number that does not move.
    build: ({ state }) => figure('summary.probes',
      state.summary ? String(state.summary.probes_24h) : '—', {
        sub: state.summary && state.summary.probes_7d
          ? `${t('summary.weekShort')} ${state.summary.probes_7d.toLocaleString()}` : '',
      }),
  },
  {
    id: 'since',
    size: 'fig',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" aria-hidden="true"><rect x="1.8" y="3" width="12.4" height="11" rx="1.6"/><path d="M1.8 6.6h12.4M5 1.8v2.6M11 1.8v2.6"/></svg>',
    build: ({ state }) => figure('summary.monitoringSince',
      state.summary && state.summary.since ? C.fmtDateTime(state.summary.since) : '—',
      { small: true }),
  },
  {
    id: 'clock',
    size: 'fig',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2"/><path d="M8 4.4V8l2.6 1.8"/></svg>',
    // The body carries the value already, so the first paint is not a dash that
    // corrects itself a moment later. The second hand is put on from outside —
    // see startClock.
    build: () => `<div class="widget-clock">
      <span class="clock-time" data-clock="time">${esc(clockTime())}</span>
      <span class="clock-date muted" data-clock="date">${esc(clockDate())}</span>
    </div>`,
  },
  {
    id: 'recent',
    size: 'wide',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2"/><path d="M8 4.6V8l2.4 1.6"/></svg>',
    build: ({ state }) => {
      const rows = (state.recentIncidents || [])
        .slice()
        .sort((a, b) => new Date(b.started_at) - new Date(a.started_at))
        .slice(0, 5)
        .map((inc) => row(null, inc.sensor_id,
          inc.sensor_name || `#${inc.sensor_id}`,
          C.fmtAgo(inc.started_at),
          inc.cause || ''));
      return listBlock('widgets.recent', rows, 'widgets.noIncidents');
    },
  },
  {
    id: 'slowest',
    size: 'wide',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="M2 3v10h12"/><path d="M4.5 10.5 7.5 7l2.5 2 4-4.5"/></svg>',
    build({ state }) {
      const rows = ranked(state, (s) => s.avg_latency_ms, (v) => v > 0)
        .map((s) => row(null, s.id, s.name, C.fmtLatency(s.avg_latency_ms), s.target));
      return listBlock('widgets.slowest', rows, 'widgets.noData');
    },
  },
  {
    id: 'flakiest',
    size: 'wide',
    icon: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M1.6 8h2.6l1.6-3.4L9 11.6l1.4-3.6h4"/></svg>',
    build({ state }) {
      // Ranked by the worst 30-day uptime rather than by a count of outages: the
      // uptime is already computed per sensor and already the number the rest of
      // the dashboard shows, so this list cannot disagree with the cards.
      const rows = ranked(state, (s) => s.uptime_30d, (v) => v < 100, true)
        .map((s) => row(null, s.id, s.name, C.fmtPct(s.uptime_30d), s.target));
      return listBlock('widgets.flakiest', rows, 'widgets.noData');
    },
  },
  {
    id: 'mini-map',
    size: 'wide',
    icon: '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><path d="M8 1.5a4.2 4.2 0 0 0-4.2 4.2c0 3 4.2 8.8 4.2 8.8s4.2-5.8 4.2-8.8A4.2 4.2 0 0 0 8 1.5Zm0 5.8a1.6 1.6 0 1 1 0-3.2 1.6 1.6 0 0 1 0 3.2Z"/></svg>',
    // The body is an empty host: the map is a live element with its own
    // listeners and a running viewport, and replacing the markup around it would
    // leave all of that pointing at a container that is gone. See ensureMap.
    build: () => '<div class="widget-card widget-map-card"><p class="widget-head">'
      + `${esc(t('widgets.mini-map'))}</p><div class="widget-map" data-widget-map></div></div>`,
    // Never redrawn wholesale — the map is only re-coloured, the way a probe
    // re-colours a pin and moves nothing.
    keep: true,
  },
  {
    id: 'shortcuts',
    size: 'wide',
    icon: '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><path d="M9 1 3 9h3.6L6.4 15 13 7H9.2Z"/></svg>',
    build({ state, can }) {
      const items = [
        ['incidents', t('incidents.title')],
        ['channels', t('channels.title')],
        ['settings', t('settings.title')],
      ].map(([route, label]) =>
        `<button type="button" class="btn btn-small" data-widget-route="${route}">${esc(label)}</button>`);
      if (can && can('admin')) {
        items.unshift(`<button type="button" class="btn btn-small" data-widget-run="new-sensor">+ ${esc(t('sensor.new'))}</button>`);
      }
      items.push(`<button type="button" class="btn btn-small" data-widget-run="live">${esc(t('widgets.live'))}</button>`);
      return `<div class="widget-card"><p class="widget-head">${esc(t('widgets.shortcuts'))}</p>
        <div class="widget-links">${items.join('')}</div></div>`;
    },
  },
];

const BY_ID = new Map(WIDGETS.map((w) => [w.id, w]));

/** The ids, in registry order — the order the picker lists them in. */
export const WIDGET_IDS = WIDGETS.map((w) => w.id);

/**
 * The header every account starts with, and the one an installation that has
 * never touched it still has. It is the ring and the four figures, so a
 * dashboard nobody has customised looks exactly as it did before the strip
 * existed. It mirrors model.DefaultWidgets on the server; this copy is what a
 * browser uses when it is talking to a build that has no such setting yet.
 */
export const DEFAULT_IDS = ['status', 'uptime', 'latency', 'probes', 'since'];

/**
 * parse reads a stored widget list.
 *
 * The value is an ordered, comma-separated list of ids, so parsing it is also
 * filtering it: an id this file does not know — a widget from a newer version, a
 * value somebody typed into the database — is dropped rather than drawn as an
 * empty tile, and a repeat is dropped because a list of ids is a set with an
 * order.
 */
export function parse(value) {
  const out = [];
  for (const part of String(value || '').split(',')) {
    const id = part.trim();
    if (id && BY_ID.has(id) && !out.includes(id)) out.push(id);
  }
  return out;
}

/**
 * ranked picks the top few sensors by some measure.
 *
 * A sensor that has never been probed is left out rather than ranked last: it
 * has no measurement to be ranked by, and a list of the slowest sensors that
 * opens with three that have never answered is a list about something else.
 * Paused sensors are left out too — they are not being measured on purpose.
 */
function ranked(state, measure, keep, ascending = false) {
  return (state.sensors || [])
    .filter((s) => s.status !== 'paused' && s.last_sensor && keep(measure(s) || 0))
    .sort((a, b) => (ascending ? measure(a) - measure(b) : measure(b) - measure(a)))
    .slice(0, 5);
}

function clockTime() {
  return new Date().toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

function clockDate() {
  return new Date().toLocaleDateString(undefined, { weekday: 'short', day: '2-digit', month: 'short' });
}

// ---------------------------------------------------------------------------
// module state
// ---------------------------------------------------------------------------

const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));

let host = null;
let tools = null;
let dialog = null;
let opts = {};
// ids is the list as it should be on screen; signature is what was last built
// from it. The two are compared on every set, which is what lets the callers
// hand over the stored value as often as they like — on every poll, on every
// theme change — without the strip being taken apart underneath the person
// reading it.
let ids = [];
let signature = null;
let editing = false;
let mapHandle = null;
let clockTimer = null;

/**
 * mount wires the strip. Called once, from app.js, with the same options idea as
 * the other leaf modules: everything this file needs from the dashboard is in
 * `opts`, and nothing here reaches into app.js.
 *
 *   state()          the live state object
 *   can(role)        the role check, for the one button that needs it
 *   openDetail(id)   open a sensor
 *   setRoute(route)  change the view
 *   newSensor()      open the sensor editor
 *   onChange(ids)    save a new list — the whole of what this file remembers
 *   onArranging(on)  arranging started or stopped — the top bar has one other
 *                    control that is only in the way while it is going on
 *
 * `toolsEl` is empty in the markup and filled here. It sits in the top bar beside
 * the layout switcher, which is where the one other control that is about the
 * dashboard's own appearance lives — and, more practically, at a fixed place that
 * does not move when the strip changes height. It used to be a row of its own
 * under the strip, which cost a line of the page and looked like a button
 * somebody had left behind.
 */
export function mount(hostEl, toolsEl, options) {
  host = hostEl;
  tools = toolsEl;
  opts = options || {};
  host?.addEventListener('click', onClick);
  tools?.addEventListener('click', onClick);
  drawTools();
  buildPicker();
  startDrag();
}

/**
 * drawTools fills the one control in the top bar.
 *
 * It says what pressing it does rather than what state the page is in: the sliders
 * offer to start arranging, the tick offers to stop. While arranging it also grows
 * an "add" button beside it, which opens the picker — the two things a person
 * arranging a header wants, in the place they are already looking, rather than one
 * of them hiding at the far end of the strip.
 *
 * It is drawn as a segmented control with an icon and a word, the same shape as the
 * layout switcher beside it, because it is the same kind of thing: a control about
 * how this dashboard is put together. The word is dropped below the width where the
 * switcher's own words go, for the reason given in style.css — the top bar has one
 * line, and a button that costs it a second line is a button that costs more than it
 * is worth. The icon and the title stay, and they say the same thing.
 */
const WIDGET_ICONS = {
  customise: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="M2 5h5.4M11.4 5H14M2 11h2.4M8.4 11H14"/><circle cx="9.4" cy="5" r="2"/><circle cx="6.4" cy="11" r="2"/></svg>',
  done: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 8.6 6.4 12 13 4.4"/></svg>',
  add: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M8 3.4v9.2M3.4 8h9.2"/></svg>',
};

function drawTools() {
  if (!tools) return;
  const editLabel = t(editing ? 'widgets.done' : 'widgets.customise');
  const add = editing
    ? `<button type="button" class="seg-btn" data-widget-pick
         title="${esc(t('widgets.add'))}" aria-label="${esc(t('widgets.add'))}">`
      + `${WIDGET_ICONS.add}<span>${esc(t('widgets.add'))}</span></button>`
    : '';
  tools.innerHTML = `<div class="segmented">${add}
    <button type="button" class="seg-btn" data-widget-edit aria-pressed="${editing ? 'true' : 'false'}"
            title="${esc(editLabel)}" aria-label="${esc(editLabel)}">`
    + `${WIDGET_ICONS[editing ? 'done' : 'customise']}<span>${esc(editLabel)}</span></button>
    </div>`;
}

/** Apply a stored list. Rebuilds only when it actually differs from what is up. */
export function setWidgets(list) {
  const next = (list || []).filter((id) => BY_ID.has(id));
  const sig = next.join(',');
  if (sig === signature) {
    refresh();
    return;
  }
  ids = next;
  signature = sig;
  draw();
}

/**
 * Draw the whole strip. The one place the markup of the strip itself is made.
 *
 * Only the widgets that are on it are drawn. What else there is to choose lives
 * behind the "add" button, in a window of its own — it used to be drawn into the
 * strip as dashed tiles, which read as a row of broken widgets rather than as an
 * offer, and took the grid apart while it did.
 */
function draw() {
  if (!host) return;
  // The map is the one thing in here that cannot survive its markup being
  // replaced, so it is stopped first and started again by the refresh below.
  stopMap();

  const tiles = ids.map(tileMarkup).join('');
  const empty = !ids.length
    ? `<p class="muted widget-empty">${esc(t('widgets.empty'))}</p>` : '';
  host.innerHTML = tiles + empty;

  drawTools();
  drawPicker();
  refresh();
}

/** One tile: the widget, and — while editing — the handle that moves it. */
function tileMarkup(id) {
  const widget = BY_ID.get(id);
  const classes = ['widget', `widget-${id}`, `widget-${widget.size || 'wide'}`];
  if (editing) classes.push('is-editing');
  return `<article class="${classes.join(' ')}" data-widget="${id}">
    ${editing ? barMarkup(widget) : ''}
    <div class="widget-body">${body(widget)}</div>
  </article>`;
}

/**
 * The bar a tile wears while the strip is being arranged.
 *
 * The arrows are not decoration: dragging is a pointer gesture, and a
 * reorderable list that can only be reordered with a mouse is a list some people
 * cannot reorder at all. They move the tile one place, which is the whole of
 * what dragging does, in a way that works from the keyboard.
 */
function barMarkup(widget) {
  return `<div class="widget-bar">
    <span class="widget-grip" aria-hidden="true">⠿</span>
    <span class="widget-name">${esc(t(`widgets.${widget.id}`))}</span>
    <span class="widget-bar-space"></span>
    <button type="button" class="icon-btn" data-widget-move="-1"
            title="${esc(t('widgets.moveLeft'))}" aria-label="${esc(t('widgets.moveLeft'))}">◀</button>
    <button type="button" class="icon-btn" data-widget-move="1"
            title="${esc(t('widgets.moveRight'))}" aria-label="${esc(t('widgets.moveRight'))}">▶</button>
    <button type="button" class="icon-btn" data-widget-remove
            title="${esc(t('widgets.remove'))}" aria-label="${esc(t('widgets.remove'))}">✕</button>
  </div>`;
}

/** A widget that is not on the strip, as a row in the picker. */
function pickRow(widget) {
  return `<li class="widget-pick">
    <button type="button" class="widget-pick-label" data-widget-add="${widget.id}">
      <span class="widget-pick-icon">${widget.icon}</span>
      <span>${esc(t(`widgets.${widget.id}`))}</span>
    </button>
  </li>`;
}

/**
 * buildPicker wires the "add a widget" window.
 *
 * The rows are built from the same registry the strip is built from, so the
 * window cannot offer a widget the strip does not know or leave one out. It is
 * filled from the list of what is *not* on the strip yet, because that is the
 * question the window answers; and when the answer is "nothing", it says so
 * rather than opening onto an empty page.
 *
 * It is a <dialog> rather than a corner of the strip for a reason: it is about the
 * strip, it is modal, and a thing you are being asked about should cover what it is
 * asking about. The window is also why the ghost tiles are gone — a dashed tile in
 * the middle of the header was an offer drawn as a broken widget.
 */
function buildPicker() {
  dialog = document.getElementById('widget-picker');
  if (!dialog) return;
  // The dialog's own click handler, so that a row in it behaves like a row
  // anywhere else in this file — the picker is not part of the dashboard and would
  // not be reached by a listener on the strip.
  dialog.addEventListener('click', onClick);
  drawPicker();
}

/** Fill the picker's rows with what could still be added. */
function drawPicker() {
  const list = document.getElementById('widget-picker-list');
  if (!list) return;
  const rest = WIDGETS.filter((w) => !ids.includes(w.id));
  list.innerHTML = rest.length
    ? rest.map(pickRow).join('')
    : `<li class="widget-pick"><span class="muted small">${esc(t('widgets.allOn'))}</span></li>`;
}

/** Open the picker, with the rows as they stand now. */
function openPicker() {
  if (!dialog) return;
  drawPicker();
  if (!dialog.open) dialog.showModal();
}

/**
 * Close the picker. Called when the dashboard is left: a modal window about the
 * header, left standing over the settings page, is a window about something that
 * is not on screen.
 */
export function closePicker() {
  if (dialog?.open) dialog.close();
}

function body(widget) {
  try {
    return widget.build(context());
  } catch {
    // A widget that cannot draw is one broken tile, not a blank dashboard. The
    // alternative — letting this throw out of renderDashboard — is caught by
    // refresh() and swallowed, and the whole header would go quiet.
    return `<p class="muted small">${esc(t('widgets.failed'))}</p>`;
  }
}

function context() {
  const state = opts.state ? opts.state() : {};
  return { state, can: opts.can };
}

/**
 * refresh redraws every tile's body, and leaves every tile where it is.
 *
 * This is the live path — a probe arrives, a poll lands, the view is redrawn —
 * so it is deliberately the cheap one. The tiles themselves are never replaced:
 * that would close the handle a drag is in the middle of, and take the map's
 * viewport with it once a minute, forever. Same rule as the sensor nodes.
 */
export function refresh() {
  if (!host) return;
  for (const tile of $$('#summary > [data-widget]')) {
    const widget = BY_ID.get(tile.dataset.widget);
    if (!widget || widget.keep) continue;
    const slot = tile.querySelector('.widget-body');
    if (slot) slot.innerHTML = body(widget);
  }
  ensureMap();
  syncClock();
}

// ---------------------------------------------------------------------------
// the two live parts: the second hand, and the map
// ---------------------------------------------------------------------------

/**
 * One timer for the whole strip, not one per drawing.
 *
 * draw() runs on every preference change and refresh() on every poll, so a timer
 * started inside build() would be a timer per minute per tick, for a clock that
 * needs exactly one. It is started when a clock is on the strip and stopped when
 * the last one leaves.
 */
function syncClock() {
  const wanted = ids.includes('clock');
  if (!wanted) {
    if (clockTimer) clearInterval(clockTimer);
    clockTimer = null;
    return;
  }
  // Only the two spans are touched. Redrawing the tile every second would be
  // work where nothing changed, and would fight the drag.
  const tick = () => {
    const time = host?.querySelector('[data-clock="time"]');
    const date = host?.querySelector('[data-clock="date"]');
    if (time) time.textContent = clockTime();
    if (date) date.textContent = clockDate();
  };
  tick();
  if (!clockTimer) clockTimer = setInterval(tick, 1000);
}

/**
 * ensureMap starts the mini map once there is somewhere to put it.
 *
 * The timing matters and is the whole reason this is not in build(). map.js
 * measures its host and falls back to 640×400 when the host has no size, and it
 * is never told to measure again — so a map mounted while the dashboard is
 * hidden, or while the app is still behind the loading screen, would stay at the
 * fallback size for the rest of the session. It waits for a host that is really
 * laid out, and it is called from refresh(), which runs again when the view
 * becomes the one on screen.
 */
function ensureMap() {
  const tile = host?.querySelector('[data-widget="mini-map"]');
  if (!tile) {
    stopMap();
    return;
  }
  if (mapHandle) return;
  const target = tile.querySelector('[data-widget-map]');
  if (!target || !target.clientWidth || !target.clientHeight) return;
  const settings = opts.mapSettings ? opts.mapSettings() : {};
  const state = context().state;
  mapHandle = mapUI.mount(target, {
    sites: state.sites || [],
    sensors: state.sensors || [],
    tileURL: settings.tileURL || '',
    attribution: settings.attribution || '',
    onOpenSensor: opts.openDetail,
  });
  // The map is a picture here, not a control. map.js makes its canvas focusable
  // and gives it arrow-key panning, which in the full view is right and in a
  // header is a tab stop that captures the arrow keys on the way past. Nothing
  // is lost by taking it out: the pins are still clickable, and the full map is
  // one press away in the layout switcher.
  const canvas = target.querySelector('canvas');
  if (canvas) {
    canvas.tabIndex = -1;
    canvas.removeAttribute('role');
    canvas.setAttribute('aria-hidden', 'true');
  }
}

/** Stop the mini map. Safe to call when none is running. */
export function stopMap() {
  if (mapHandle) {
    mapHandle.unmount();
    mapHandle = null;
  }
}

// ---------------------------------------------------------------------------
// arranging
// ---------------------------------------------------------------------------

/**
 * Start or stop arranging, and say so to whoever drew the controls around the
 * strip. app.js is told rather than asked: it owns the layout switcher, and the
 * switcher has to give up its place while the header is being arranged — the
 * header only exists in the card view, so the control that leaves it is not a
 * control the person arranging can use, and it is the width of exactly the button
 * that takes its place. Hiding is a state of the page, so it is asked for once,
 * when it changes, and not on every redraw.
 */
function setArranging(on) {
  editing = on;
  if (opts.onArranging) opts.onArranging(editing);
  draw();
}

/** Whether the strip is currently being arranged. The top bar has to know. */
export function isArranging() {
  return editing;
}

/**
 * Stop arranging, if it is going on. Used by the tour, which walks somebody through
 * the top bar and needs the controls there standing where it describes them.
 */
export function stopArranging() {
  if (editing) setArranging(false);
}

function onClick(event) {
  const edit = event.target.closest('[data-widget-edit]');
  if (edit) {
    setArranging(!editing);
    return;
  }
  const pick = event.target.closest('[data-widget-pick]');
  if (pick) {
    openPicker();
    return;
  }
  const add = event.target.closest('[data-widget-add]');
  if (add) {
    const id = add.dataset.widgetAdd;
    // The window shuts as soon as one is chosen. It is a question with one answer
    // at a time, and what happens next happens to the strip — the new tile lands at
    // the end, and putting it where it belongs means dragging it, which needs the
    // strip uncovered. Adding it before closing would leave the new tile behind a
    // window, which reads as nothing having happened.
    if (dialog?.open) dialog.close();
    save(ids.concat([id]));
    return;
  }
  const remove = event.target.closest('[data-widget-remove]');
  if (remove) {
    const id = remove.closest('[data-widget]')?.dataset.widget;
    save(ids.filter((x) => x !== id));
    return;
  }
  const move = event.target.closest('[data-widget-move]');
  if (move) {
    const id = move.closest('[data-widget]')?.dataset.widget;
    moveOne(id, Number(move.dataset.widgetMove));
    return;
  }
  const route = event.target.closest('[data-widget-route]');
  if (route) {
    opts.setRoute?.(route.dataset.widgetRoute);
    return;
  }
  const run = event.target.closest('[data-widget-run]');
  if (run) {
    if (run.dataset.widgetRun === 'new-sensor') opts.newSensor?.();
    else if (run.dataset.widgetRun === 'live') opts.toggleLive?.();
    return;
  }
  const sensor = event.target.closest('[data-widget-sensor]');
  if (sensor) {
    opts.openDetail?.(Number(sensor.dataset.widgetSensor));
  }
}

/** Move one widget one place along, and save the result. */
function moveOne(id, delta) {
  const at = ids.indexOf(id);
  const to = at + delta;
  if (at < 0 || to < 0 || to >= ids.length) return;
  const next = ids.slice();
  next.splice(at, 1);
  next.splice(to, 0, id);
  save(next);
}

/**
 * save hands a new list to the dashboard, which stores it.
 *
 * The strip is redrawn from the list that was asked for, not from the response:
 * the click has to land immediately. If the server refuses, the caller puts the
 * previous value back and calls setWidgets with it, and the strip follows — the
 * same contract persistUI has with every other control.
 */
function save(next) {
  const sig = next.join(',');
  if (sig === signature) return;
  signature = sig;
  ids = next;
  draw();
  opts.onChange?.(next);
}

// ---------------------------------------------------------------------------
// dragging
// ---------------------------------------------------------------------------

/**
 * Drag to reorder, with pointer events rather than HTML5 drag-and-drop.
 *
 * The HTML5 API does not work with a finger, and a homelab dashboard is read on
 * a phone as often as at a desk. Pointer events cover mouse, finger and pen with
 * one code path.
 *
 * Only while editing: the strip is part of the dashboard, and a press on it is
 * otherwise just a press — the two places that click it to dismiss the account
 * menu would start a drag instead.
 */
function startDrag() {
  if (!host) return;
  let held = null; // { tile, pointer, startX }
  let moved = false;

  host.addEventListener('pointerdown', (event) => {
    if (!editing || event.button !== 0) return;
    const tile = event.target.closest('#summary > [data-widget]');
    if (!tile) return;
    // The bar's own buttons are controls, not handles. A press on one of them is
    // a press on that button, and must not become a drag of the tile behind it.
    if (event.target.closest('button')) return;
    held = { tile, pointer: event.pointerId, startX: event.clientX };
    moved = false;
  });

  // Listened for on the document rather than on the tile: the pointer leaves the
  // tile almost immediately — that is what moving it means — and the capture is
  // what keeps the events coming to us once it has.
  document.addEventListener('pointermove', (event) => {
    if (!held || event.pointerId !== held.pointer) return;
    if (!moved) {
      if (Math.abs(event.clientX - held.startX) < 6) return;
      moved = true;
      held.tile.classList.add('is-dragging');
      held.tile.setPointerCapture?.(held.pointer);
    }
    const over = document.elementFromPoint(event.clientX, event.clientY)
      ?.closest('#summary > [data-widget]');
    if (!over || over === held.tile) return;
    const box = over.getBoundingClientRect();
    const after = event.clientX > box.left + box.width / 2;
    over.parentNode.insertBefore(held.tile, after ? over.nextSibling : over);
  });

  const drop = (event) => {
    if (!held || event.pointerId !== held.pointer) return;
    const { tile } = held;
    held = null;
    tile.classList.remove('is-dragging');
    if (!moved) return;
    // Read back from the document rather than tracking it in a variable: the
    // strip is the truth, and this is the one moment the two could disagree. The
    // selector is what keeps the "add a widget" tile and the toolbar out of the
    // list — an entry that is not a widget would be refused by the server, and a
    // refused save is a silent spring back.
    save($$('#summary > [data-widget]').map((el) => el.dataset.widget));
  };
  document.addEventListener('pointerup', drop);
  document.addEventListener('pointercancel', drop);
}
