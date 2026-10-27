// ProbeGlass dashboard.
//
// Plain ES modules, no framework: the whole UI is a few hundred lines of DOM
// building on top of a REST API and one Server-Sent Events stream. State lives
// in one object, views are pure functions of it, and everything the user can
// type is escaped on the way into the DOM.

import { t, applyTranslations, setLanguage, available, nameOf, locale } from './i18n.js';
import * as C from './charts.js';
import { helpButton, helpButtonText, init as initHelp, overlayHost } from './tooltip.js';
import { mount as mountPasswordMeter } from './password.js';
import { start as startTour, TOUR_VERSION } from './tour.js';
import * as usersUI from './users.js';
import * as layouts from './layouts.js';
import * as mapUI from './map.js';
import * as userUI from './user.js';
import * as searchUI from './search.js';
import * as widgetsUI from './widgets.js';
import * as iconUI from './icons.js';

const esc = C.esc;

/**
 * ACCENTS are the colours this dashboard offers, for its own accent and for a
 * group's heading — one list, because they are one palette and a second copy would
 * be a colour that exists in one place and not in the other. The server sends the
 * same six names along with the groups; this is what a build that has not heard
 * from that route yet draws from.
 *
 * It is defined up here, above the state object that reads it: `const` is not
 * hoisted into usability, and a list declared further down would make the whole
 * module fail at the first line that touches it.
 */
const ACCENTS = ['azure', 'violet', 'emerald', 'amber', 'rose', 'slate'];

// ---------------------------------------------------------------------------
// state
// ---------------------------------------------------------------------------

const state = {
  session: null,
  summary: null,
  sensors: [],
  // recentIncidents is the handful the dashboard summary carries; incidents is
  // the full list the Incidents tab asks for.
  recentIncidents: [],
  incidents: [],
  channels: [],
  channelTypes: [],
  // The sites. They arrive with the summary rather than on their own request, so
  // every view that groups by place already has them by the time it draws.
  sites: [],
  // The groups, and what somebody has said about them: an icon, a colour, a place
  // in the order. A group exists because a sensor names it, so this list is a
  // superset of the headings the dashboard draws and never a requirement for one —
  // a sensor in a group with no row here is the ordinary state of a fresh
  // installation. They arrive with the summary for the same reason the sites do.
  groups: [],
  // The tags and what somebody has said about them, in the same shape as groups:
  // an icon, a colour, a place in the order. A tag exists because a sensor carries
  // it in its comma-separated list, so this is loaded on its own rather than with
  // the summary — the detail window draws the chips and the editor needs the list.
  tags: [],
  // The maintenance windows and the kinds the form may offer. Read on the settings
  // page, like the sites, because nowhere else draws them.
  windows: [],
  windowKinds: [],
  // mapHandle is the running map: it can be told that the sensors moved and it
  // can be torn down. It is a live element with its own listeners, not a string
  // of markup, so nothing else can dispose of it.
  mapHandle: null,
  // The accounts and the roles the server knows, both only ever filled for an
  // administrator. Everyone else reads them as empty lists and sees no user panel.
  users: [],
  roles: [],
  usersLoaded: false,
  // exampleData is the count behind the "Example data" panel: it decides whether
  // the removal button is there at all, and how much it says it will take.
  exampleData: null,
  // metricsToken is the read-only credential a Prometheus scraper uses. Empty
  // means the endpoint is off; it is only ever loaded for an administrator.
  metricsToken: '',
  settings: null,
  // The colours a group may be given, as the server lists them. Defaulted to the
  // same six names so that the editor is complete before the first answer arrives.
  accents: ACCENTS,
  query: '',
  route: 'dashboard',
  detail: null,
  // pendingDetail is the sensor a deep link (#/sensor/12) asked for before the
  // summary that carries the sensors had arrived. Alert messages link straight
  // to the sensor that fired, and on a cold load that URL is the first thing to
  // run, so the request has to survive until there is something to open.
  pendingDetail: null,
  range: '24h',
  // incidentFilter is everything the filter line above the incident list is set
  // to. It is one object rather than four fields because the query is built from
  // it in one place, and because "is the filter doing anything" is a question
  // about the whole of it. The defaults are the un-filtered list.
  incidentFilter: { status: '', sensor: 0, range: 'all', query: '' },
  stream: null,
  // liveOn is what the last syncLive decided, so that turning the stream back on
  // can be told apart from the first decision of a page load. `undefined` means
  // nobody has decided yet.
  liveOn: undefined,
  refreshTimer: null,
  dirty: false,
  // settingsTab is which category of the settings page is on screen. Only the
  // chosen one is shown, so it is a real piece of state rather than a scroll
  // position — and it lives here, not on the account, because it is where the
  // person was reading rather than how they want the dashboard to look. It
  // outlives a renderSettings(), so an event that redraws the page does not
  // throw the reader out of the category they were in.
  settingsTab: '',
};

// mode decides how much of the interface is on screen: "simple" hides the
// advanced fields and areas, "advanced" shows everything. It is chosen at setup
// and switchable in settings, and the server is where it is stored.
const ui = {
  theme: 'auto', accent: 'azure', density: 'comfortable', layout: 'grid',
  language: 'en', mode: 'advanced', live: 'on',
  // The dashboard header. Defaulted here as well as on the server so that a
  // browser talking to a build that has not been rebuilt yet still shows the
  // header it has always shown instead of an empty strip.
  widgets: widgetsUI.DEFAULT_IDS.join(','),
};

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/**
 * when renders a timestamp from the server in the reader's own clock and
 * language, and passes anything it cannot parse through unchanged: a build
 * stamp is read to compare two builds, and "Invalid Date" would compare as well
 * as nothing at all.
 */
function when(value) {
  if (!value) return '—';
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? String(value) : at.toLocaleString(locale());
}

// A request that never comes back is the worst failure mode this dashboard can
// have: the page sits on a spinner and nothing says why. Every call therefore
// carries a deadline, and a call that hits it reports a message the user can act
// on instead of hanging forever.
const API_TIMEOUT_MS = 15000;

/**
 * Fetch JSON from the API, surfacing the server's error message.
 *
 * A FormData body is passed through as it is, with no Content-Type of ours: the
 * one place that happens is an icon upload, and the boundary that separates the
 * parts of a multipart body is chosen by the browser when it builds it. Setting
 * `application/json` here would make the file unreadable on the server, and setting
 * the type by hand is not possible either — the boundary is not known to this
 * function. Everything else is JSON, as it always was.
 */
async function api(path, { method = 'GET', body, timeout = API_TIMEOUT_MS } = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  const form = typeof FormData !== 'undefined' && body instanceof FormData;
  let res;
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: body === undefined || form ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : (form ? body : JSON.stringify(body)),
      signal: controller.signal,
    });
  } catch (err) {
    // An abort here is our own deadline, not a decision by the server.
    if (controller.signal.aborted) throw new Error(t('common.timeout'));
    throw err;
  } finally {
    clearTimeout(timer);
  }
  let data = null;
  const text = await res.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const error = new Error((data && data.error) || `${res.status} ${res.statusText}`);
    error.status = res.status;
    throw error;
  }
  return data;
}

function toast(message, kind = 'ok') {
  const node = document.createElement('div');
  node.className = `toast toast-${kind}`;
  node.textContent = message;
  toastHost().appendChild(node);
  setTimeout(() => node.classList.add('toast-out'), 3200);
  setTimeout(() => node.remove(), 3800);
}

/**
 * toastHost is where a toast is put.
 *
 * A modal <dialog> lives in the top layer and paints its backdrop over the whole
 * page, so a toast appended to the page's own container while a form is open is
 * drawn behind the dialog and never seen — which is exactly when the most
 * important one arrives, the result of the Test button. The toasts move into the
 * open dialog for as long as it is open, the same move the field help makes; the
 * rule for where an overlay goes lives in one place, tooltip.js.
 *
 * Inside a dialog the toasts need a container of their own: #toasts belongs to
 * the page and goes behind the dialog with everything else in it.
 */
function toastHost() {
  const page = $('#toasts');
  const host = overlayHost(page);
  if (host === page) return page;
  let inside = host.querySelector('.toasts');
  if (!inside) {
    inside = document.createElement('div');
    inside.className = 'toasts';
    inside.setAttribute('aria-live', 'polite');
    host.append(inside);
  }
  return inside;
}

function confirmDialog(message) {
  return window.confirm(message);
}

/**
 * sheet builds the controller for one `<dialog>` and its form.
 *
 * Every editor in the dashboard shares a dialog whose four bits of state have to
 * be reset between openings, because getting any of them wrong leaves the next
 * dialog carrying the last one's state. The delete button is the sharpest of
 * them: each editor that offers one injects it into the shared footer, and an
 * editor that does not offer one has to take it away again — otherwise "New
 * channel" opens with a live "Delete sensor" button under it.
 *
 * There are two of them rather than one, and that is not an accident of taste:
 * `showModal()` on a dialog that is already open throws, so a dialog opened from
 * inside another dialog cannot reuse it. Opening the site editor from the sensor
 * form could have closed the sensor form first and rebuilt it afterwards, but a
 * rebuilt sensor form is a form whose type select has to have its change handler
 * run again, whose option fields, channel ticks and open groups all have to be
 * carried over by hand — and every value missed on the way is silent loss. Two
 * dialogs cost one element and keep the form behind them untouched.
 */
function sheet({ dialog, form, title, body, error, save, cancel, x, test, footer }) {
  return {
    open({ title: heading, body: markup, mode, id = '', saveLabel, showTest = false, locked = false }) {
      const el = $(form);
      el.dataset.mode = mode;
      el.dataset.id = id === '' ? '' : String(id);
      el.dataset.locked = locked ? '1' : '';
      $(body).innerHTML = markup;
      $(error).textContent = '';
      $(title).textContent = heading;
      applyTranslations($(dialog));
      // After applyTranslations, not before: the save button carries the generic
      // "Save" in its data-i18n, and this is the per-dialog word for it.
      $(save).textContent = saveLabel || t('common.save');
      if (test) $(test).hidden = !showTest;
      $(cancel).hidden = locked;
      $(x).hidden = locked;
      this.clearDanger();
      $(dialog).showModal();
      el.querySelector('input:not([disabled]):not([type="radio"]), select, textarea')?.focus();
    },

    /** danger puts a destructive action at the far left of the footer. */
    danger(label, onClick) {
      this.clearDanger();
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'btn btn-danger danger-delete';
      button.textContent = label;
      button.addEventListener('click', onClick);
      const el = $(footer);
      el.insertBefore(button, el.firstChild);
    },

    clearDanger() {
      $('.danger-delete')?.remove();
    },

    close() {
      $(dialog).close();
    },
  };
}

const modal = sheet({
  dialog: '#modal',
  form: '#modal-form',
  title: '#modal-title',
  body: '#modal-body',
  error: '#modal-error',
  save: '#modal-save',
  test: '#modal-test',
  cancel: '#modal-cancel',
  x: '#modal-x',
  footer: '#modal .sheet-foot',
});

/**
 * siteModal is the site editor, and it is the second dialog on purpose.
 *
 * Its own element means its own form, its own error line and its own footer, so
 * the sensor form underneath keeps every value it was holding when the window
 * opened over it — see the comment on `sheet`.
 */
const siteModal = sheet({
  dialog: '#modal-site',
  form: '#site-form',
  title: '#site-title',
  body: '#site-body',
  error: '#site-error',
  save: '#site-save',
  cancel: '#site-cancel',
  x: '#site-x',
});

/**
 * groupModal is the group editor, and it is the fourth dialog on purpose: its own
 * element means its own form and its own error line, so whatever was open
 * underneath keeps every value it was holding — see the comment on `sheet`.
 */
const groupModal = sheet({
  dialog: '#modal-group',
  form: '#group-form',
  title: '#group-title',
  body: '#group-body',
  error: '#group-error',
  save: '#group-save',
  cancel: '#group-cancel',
  x: '#group-x',
});

// A locked dialog has no way out but its own button: Escape would close it and
// take the only copy of whatever it is showing with it. The dashboard uses it for
// the recovery codes, which are shown exactly once.
//
// An open sensor-type chooser is the other case: Escape belongs to the panel
// first. Closing both at once would hide the field somebody was in the middle of
// choosing and take the rest of the half-filled sensor form with it.
$('#modal').addEventListener('cancel', (event) => {
  if ($('#modal-form').dataset.locked === '1') {
    event.preventDefault();
    return;
  }
  const panel = document.querySelector('#modal .type-panel:not([hidden]), #modal .pick-panel:not([hidden])');
  if (panel) {
    event.preventDefault();
    if (panel.classList.contains('type-panel')) {
      closeTypePanel(panel.closest('[data-type-field]'), { restoreFocus: true });
    } else {
      const field = panel.closest('[data-pick-field]');
      closePickPanel(field);
      field?.querySelector('[data-pick-open]')?.focus();
    }
  }
});

/**
 * users is the accounts and second-factor screen. It lives in its own module
 * because it is a screen's worth of dialogs that nothing but the settings panel
 * ever opens, and it is wired here rather than importing back into app.js so that
 * the dependency runs one way.
 */
const users = usersUI.mount({
  api,
  toast,
  modal,
  state,
  afterChange: async () => {
    // The session is what carries the signed-in account's own second-factor state
    // and how many recovery codes are left, and both are exactly what changes here.
    // It is also what greys out the API key for everybody below administrator, so
    // it is re-read rather than patched.
    state.session = await api('/api/session');
    Object.assign(ui, state.session.ui || {});
    // And the list itself is re-read, because a new account, a changed role, a
    // reset password and an account switched off are all rows in it. Without this
    // the table would go on showing what was there when the page was opened, and
    // the account somebody just created would not be in it.
    await loadUsers(true);
    await renderSettings();
  },
});

function simpleMode() {
  return (ui.mode || 'advanced') === 'simple';
}

// The roles the server knows, in the order that matters. Kept here so that the
// dashboard and the router agree on what "an operator" means; the server is still
// the one that decides, and a viewer who opens the console and sends the request
// anyway gets a 403.
const ROLE_RANK = { viewer: 1, operator: 2, admin: 3 };

/**
 * can reports whether the signed-in account may do what that role may do.
 *
 * It exists to grey controls out, not to hide them. A button that is present and
 * does nothing is worse than a grey one: the grey one explains itself. And the
 * API key, which has no account, is an administrator — the same rule the server
 * applies.
 */
function can(role) {
  if (!state.session?.authenticated) return false;
  const mine = state.session.user ? ROLE_RANK[state.session.user.role] || 0 : ROLE_RANK.admin;
  return mine >= (ROLE_RANK[role] || 0);
}

/** roleLabel names a role for a person to read. */
function roleLabel(role) {
  return t(`users.role.${role}`);
}

/**
 * denied is what a control a role may not use carries: disabled, with the reason
 * attached so that hovering says who to ask rather than nothing at all.
 */
function denied(role) {
  return can(role) ? '' : ` disabled title="${esc(t('common.needsRole', { role: roleLabel(role) }))}"`;
}

/**
 * applyRoleGating greys out the controls that live outside the parts of the page
 * the render functions rebuild.
 *
 * The cards and the settings panel are drawn from the current role each time they
 * are drawn, so they take care of themselves. The top bar and the empty state are
 * static markup from index.html, and they would keep whatever state they were
 * given at the first paint. Re-checking them after every refresh means a role
 * changed under a running dashboard — or simply a stale tab — corrects itself
 * instead of offering a button whose request comes back 403 for no visible reason.
 */
function applyRoleGating() {
  for (const [el, role, label] of [
    [$('#new-sensor'), 'admin', 'sensor.new'],
    [$('#new-channel'), 'admin', 'channels.new'],
    [$('#dashboard-new-sensor'), 'admin', 'sensor.new'],
  ]) {
    if (!el) continue;
    const blocked = !can(role);
    el.disabled = blocked;
    const why = blocked ? t('common.needsRole', { role: roleLabel(role) }) : t(label);
    el.title = blocked ? why : '';
    el.setAttribute('aria-label', why);
  }
}

/**
 * applyFold collapses the deep detail panels in simple mode and leaves them open
 * otherwise. It is the one place the fold is applied, so a panel added later
 * only needs the class. The hiding of whole fields is pure CSS
 * (`[data-mode="simple"] .adv-hide`) and needs no JavaScript at all — which is
 * what keeps the render functions free of mode checks.
 */
function applyFold() {
  const simple = simpleMode();
  for (const el of $$('details.adv-fold')) el.open = !simple;
}

function applyUIPreferences() {
  const root = document.documentElement;
  root.dataset.theme = ui.theme || 'auto';
  root.dataset.accent = ui.accent || 'azure';
  root.dataset.density = ui.density || 'comfortable';
  root.dataset.layout = ui.layout || 'grid';
  root.dataset.mode = ui.mode || 'advanced';
  if (ui.language) setLanguage(ui.language);
  syncLayoutSwitcher();
  syncLive();
  applyFold();
  // Switching to simple mode hides parts of the settings page, and a category
  // whose panels have all just gone has to go with them.
  showSettingsCategory(state.settingsTab);
  // Last, and cheap: the strip keeps its signature and only redraws when the
  // list really changed, so handing it the stored value on every preference
  // change — a theme, an accent, a language — leaves the tiles, and the map's
  // viewport, exactly where they are.
  widgetsUI.setWidgets(widgetsUI.parse(ui.widgets));
}

// ---------------------------------------------------------------------------
// the layout switcher
// ---------------------------------------------------------------------------

// The five ways of looking at the same sensors. The order is the order of the
// buttons, and it is deliberately the order of growing structure: the plainest
// first, the most annotated last.
const LAYOUTS = ['grid', 'list', 'tree', 'wall', 'map'];

const LAYOUT_ICONS = {
  grid: '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><rect x="1" y="1" width="6" height="6" rx="1.2"/><rect x="9" y="1" width="6" height="6" rx="1.2"/><rect x="1" y="9" width="6" height="6" rx="1.2"/><rect x="9" y="9" width="6" height="6" rx="1.2"/></svg>',
  list: '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><rect x="1" y="2.5" width="14" height="2" rx="1"/><rect x="1" y="7" width="14" height="2" rx="1"/><rect x="1" y="11.5" width="14" height="2" rx="1"/></svg>',
  tree: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><path d="M2 3.5h4M2 8h7M2 12.5h7M8 3.5v9" stroke-linecap="round"/></svg>',
  wall: '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><rect x="1" y="3" width="4" height="3" rx="0.8"/><rect x="6" y="3" width="4" height="3" rx="0.8"/><rect x="11" y="3" width="4" height="3" rx="0.8"/><rect x="1" y="8" width="4" height="3" rx="0.8"/><rect x="6" y="8" width="4" height="3" rx="0.8"/><rect x="11" y="8" width="4" height="3" rx="0.8"/></svg>',
  map: '<svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true"><path d="M8 1.5a4.2 4.2 0 0 0-4.2 4.2c0 3 4.2 8.8 4.2 8.8s4.2-5.8 4.2-8.8A4.2 4.2 0 0 0 8 1.5Zm0 5.8a1.6 1.6 0 1 1 0-3.2 1.6 1.6 0 0 1 0 3.2Z"/></svg>',
};

/**
 * buildLayoutSwitcher fills the control, and rewires it only the first time.
 *
 * The listeners are on the container and delegated, so nothing has to be
 * rewired when a button's pressed state changes — and there is exactly one
 * handler however many times the view is redrawn.
 *
 * The markup, though, is rebuilt on every call. It carries a `title` and an
 * `aria-label` and a visible name per layout, all three from the catalogue, so a
 * control that is filled once and then left alone is a control that goes on
 * naming the layouts in the language the page was loaded in. It is called again
 * on a language switch for exactly that reason. See repaintBuiltControls().
 */
function buildLayoutSwitcher() {
  const host = $('#layout-switcher');
  if (!host) return;
  if (!host.dataset.wired) {
    host.dataset.wired = '1';
    host.addEventListener('click', (event) => {
      const button = event.target.closest('[data-layout]');
      if (button) void persistUI('layout', button.dataset.layout);
    });
  }
  host.innerHTML = LAYOUTS.map((value) => `
    <button class="seg-btn" type="button" data-layout="${value}"
            title="${esc(t(`settings.layout.${value}`))}"
            aria-label="${esc(t(`settings.layout.${value}`))}">
      ${LAYOUT_ICONS[value]}<span>${esc(t(`settings.layout.${value}`))}</span>
    </button>`).join('');
  syncLayoutSwitcher();
}

/** Mark the current view on the control, and leave every other button plain. */
function syncLayoutSwitcher() {
  const host = $('#layout-switcher');
  if (!host) return;
  for (const button of $$('[data-layout]', host)) {
    button.setAttribute('aria-pressed', button.dataset.layout === ui.layout ? 'true' : 'false');
  }
}

/**
 * persistUI writes one preference, and keeps the screen and the server in step.
 *
 * The interface follows the click immediately — a control that waits for a round
 * trip feels broken — and only then does the server hear about it. If the write
 * is refused the previous value goes back everywhere it was shown, including
 * into the control that asked for it, so what is on screen is never a promise
 * the server did not keep. The user menu's quick switches and the settings page
 * share this one path rather than each having their own.
 */
async function persistUI(key, value, control) {
  const previous = ui[key];
  if (previous === value) return;
  // Switching language is the one preference that changes text rather than
  // appearance, and nothing already on screen was drawn for the new one.
  const changedLanguage = key === 'language' && previous !== value;
  ui[key] = value;
  applyUIPreferences();
  if (changedLanguage) await repaintForLanguage();
  if (key === 'layout') renderDashboard();
  try {
    await api('/api/settings', { method: 'PUT', body: { ui: { [key]: value } } });
  } catch (err) {
    ui[key] = previous;
    applyUIPreferences();
    if (changedLanguage) await repaintForLanguage();
    if (control) control.value = previous;
    if (key === 'layout') renderDashboard();
    toast(err.message, 'error');
  }
}

/**
 * repaintForLanguage puts the whole page into the language that was just chosen.
 *
 * Six halves, and none of them is enough on its own. Each one exists because
 * something was found still reading the old language after a switch, and the list
 * is the answer to "where does a word live":
 *
 *  - applyTranslations rewrites the elements that carry a data-i18n attribute:
 *    the shell in index.html, the tab strip, the search box, and the close button
 *    on each of the four dialogs. Skip it and the top bar stays English.
 *  - renderAll redraws the view, which is where most of the words are — they are
 *    built by JavaScript at render time and are not attributes anything can go
 *    back and rewrite. Skip it and every card, row and panel does.
 *  - repaintBuiltControls rebuilds the controls that are poured into a container
 *    once and then left alone: the layout switcher and the incident chips. Those
 *    carry no attribute to rewrite and belong to no view, so neither of the first
 *    two reaches them.
 *  - widgetsUI.repaint does the same for the two controls in the header's own
 *    module — the Customise button, which is in the top bar whatever view is on
 *    screen, and the widget picker's rows.
 *  - applyRoleGating writes a title and an aria-label on the three role-gated
 *    buttons, and it runs when the session is loaded. Those are attributes, not
 *    text, so applyTranslations does not know about them.
 *  - repaintAuth re-words the sign-in screen: its hint, and the strength meter
 *    under the password field, which is mounted once and only ever redrawn when
 *    somebody types. Both are in the document while somebody is signed in, and
 *    both are what the next person to sign out reads.
 *
 * userUI.repaint is the seventh surface and the one place the switch is also
 * offered, so a menu that kept the old language would be read in the voice it has
 * just left.
 *
 * The relative timestamps on the cards are not on this list because they are not
 * a mechanism of their own: fmtAgo reads the catalogue like everything else, so
 * renderAll takes them with it.
 */
async function repaintForLanguage() {
  applyTranslations();
  await renderAll();
  repaintBuiltControls();
  widgetsUI.repaint();
  applyRoleGating();
  repaintAuth();
  userUI.repaint();
}

/**
 * repaintAuth re-words the sign-in screen.
 *
 * It is behind the dashboard rather than on it, which is why it was missed: after
 * a language switch everything in front of you is German, and the English only
 * appears the next time the sign-in screen does — as a stale hint under a correct
 * heading, and as a strength meter still reading "strong / Good to go." from
 * whatever was last typed into it. The meter is mounted once and driven only by
 * the field's own input event, so nothing else has ever redrawn it.
 *
 * The mode is read back off the form rather than passed in: this is called from the
 * preference write, which knows nothing about the sign-in screen. A form that has
 * never been shown has no mode and nothing to re-word.
 */
function repaintAuth() {
  const form = $('#auth-form');
  if (!form || !form.dataset.mode) return;
  $('#auth-hint').textContent = form.dataset.mode === 'setup'
    ? t('auth.setupTitle')
    : t('auth.loginHint');
  authMeter?.();
}

/**
 * repaintBuiltControls rebuilds the controls that are filled once and forgotten.
 *
 * Both are wired to their container rather than to their buttons, so calling them
 * again is safe and leaves exactly one handler in place. See buildLayoutSwitcher
 * and buildIncidentFilter, which is where the reason is written down.
 */
function repaintBuiltControls() {
  buildLayoutSwitcher();
  buildIncidentFilter();
}

// ---------------------------------------------------------------------------
// boot
// ---------------------------------------------------------------------------

// showBootError replaces the loading spinner with something the user can act on.
//
// The spinner is the one screen with no way out: if anything below throws, or if
// a module fails to load, the page just stays on it forever. Anything that can
// fail during boot therefore ends here, with the message, a way to see the
// detail, and a Retry button — never with a screen that does not say what
// happened.
function showBootError(message, detail) {
  const boot = $('#boot');
  boot.hidden = false;
  $('#app').hidden = true;
  $('#auth').hidden = true;
  boot.innerHTML = '';

  const panel = document.createElement('div');
  panel.className = 'boot-error';

  const heading = document.createElement('h2');
  heading.textContent = t('boot.failed');
  panel.append(heading);

  const text = document.createElement('p');
  text.className = 'form-error';
  text.textContent = message || t('common.error');
  panel.append(text);

  if (detail) {
    const help = document.createElement('p');
    help.className = 'muted';
    help.textContent = t('boot.help');
    panel.append(help);

    const pre = document.createElement('pre');
    pre.className = 'boot-detail';
    pre.textContent = detail;
    panel.append(pre);
  }

  const retry = document.createElement('button');
  retry.type = 'button';
  retry.className = 'btn btn-primary';
  retry.textContent = t('boot.retry');
  retry.addEventListener('click', () => location.reload());
  panel.append(retry);

  boot.append(panel);
}

async function boot() {
  applyTranslations();
  // Before anything can draw it: the first applyUIPreferences below already hands
  // the stored widget list to the strip, and a strip that is not wired yet would
  // take the list, remember it as drawn, and never draw it.
  mountWidgets();
  try {
    state.session = await api('/api/session');

    Object.assign(ui, state.session.ui || {});
    applyUIPreferences();

    if (state.session.setup_required) {
      showAuth('setup');
      return;
    }
    if (!state.session.authenticated) {
      showAuth('login');
      return;
    }
    showApp();
  } catch (err) {
    showBootError(err.message || String(err), err && err.stack);
  } finally {
    // Tells watchdog.js to stand down: whatever happened, the user is now
    // looking at a real screen rather than at the spinner.
    window.__probeglassBooted = true;
  }
}

let authMeter = null;

// authChallenge is the short-lived proof that the password was accepted, held
// only in memory while the code step is on screen. It is never written to
// storage: it is a credential, and a credential that outlives the tab is one more
// thing to leak.
let authChallenge = '';

function showAuth(mode) {
  $('#boot').hidden = true;
  $('#app').hidden = true;
  const view = $('#auth');
  view.hidden = false;

  const setup = mode === 'setup';
  const form = $('#auth-form');
  form.dataset.mode = mode;
  form.dataset.step = 'password';
  authChallenge = '';
  $('#auth-hint').textContent = setup ? t('auth.setupTitle') : t('auth.loginHint');
  $('#auth-confirm-row').hidden = !setup;
  $('#auth-password-step').hidden = false;
  $('#auth-totp-step').hidden = true;
  $('#auth-mode-step').hidden = true;
  $('#auth-back').hidden = true;
  $('#auth-password').autocomplete = setup ? 'new-password' : 'current-password';
  $('#auth-username').autocomplete = setup ? 'username' : 'username';
  $('#auth-submit').textContent = setup ? t('auth.continue') : t('auth.signIn');

  // Mounted once: the input element persists across calls, and a second meter
  // would add a second listener to it.
  if (!authMeter) authMeter = mountPasswordMeter($('#auth-password'), $('#auth-meter'));
  // In the setup screen the name is the first thing to fill in; when signing in,
  // the browser has usually remembered it and the password is what is wanted.
  if (setup) $('#auth-username').focus();
  else $('#auth-password').focus();
}

/** Back from the mode step or the code step to the password fields. */
$('#auth-back').addEventListener('click', () => {
  const form = $('#auth-form');
  form.dataset.step = 'password';
  authChallenge = '';
  $('#auth-password-step').hidden = false;
  $('#auth-totp-step').hidden = true;
  $('#auth-mode-step').hidden = true;
  $('#auth-back').hidden = true;
  $('#auth-submit').textContent = t('auth.continue');
  $('#auth-password').focus();
});

$('#auth-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  const error = $('#auth-error');
  error.textContent = '';
  const username = $('#auth-username').value.trim();
  const password = $('#auth-password').value;
  const setup = form.dataset.mode === 'setup';

  // Setup is two steps: the credentials, then how much of the interface to show.
  // The first step validates and moves on without a request, so the mode is
  // stored in the same write that creates the account — there is no window in
  // which an installation is set up but unanswered.
  if (setup && form.dataset.step === 'password') {
    if (!username || username.length > 64 || /\s/.test(username)) {
      error.textContent = t('auth.usernameTooShort');
      $('#auth-username').focus();
      return;
    }
    if (password.length < 8) {
      error.textContent = t('auth.tooShort');
      return;
    }
    if (password !== $('#auth-confirm').value) {
      error.textContent = t('auth.mismatch');
      return;
    }
    form.dataset.step = 'mode';
    $('#auth-password-step').hidden = true;
    $('#auth-mode-step').hidden = false;
    $('#auth-back').hidden = false;
    $('#auth-submit').textContent = t('auth.finish');
    $('#auth-mode-step input[name="ui-mode"]')?.focus();
    return;
  }

  const button = $('#auth-submit');
  const restore = button.textContent;
  button.disabled = true;
  button.textContent = t('auth.working');
  try {
    if (form.dataset.step === 'code') {
      await api('/api/login/totp', {
        method: 'POST',
        body: { challenge: authChallenge, code: $('#auth-code').value.trim() },
      });
    } else {
      const mode = form.querySelector('input[name="ui-mode"]:checked')?.value || '';
      const answer = await api(setup ? '/api/setup' : '/api/login', {
        method: 'POST',
        body: setup ? { username, password, mode } : { username, password },
      });
      // The password was right and a second factor is set: the server answered
      // with a challenge instead of a session, and the code field takes over.
      if (answer && answer.totp_required) {
        authChallenge = answer.challenge;
        form.dataset.step = 'code';
        $('#auth-hint').textContent = t('auth.totpTitle');
        $('#auth-password-step').hidden = true;
        $('#auth-totp-step').hidden = false;
        $('#auth-back').hidden = false;
        $('#auth-back').dataset.i18n = 'auth.back';
        $('#auth-code').value = '';
        $('#auth-code').focus();
        return;
      }
    }

    // Confirm the session before leaving this screen. The cookie has to survive
    // the round trip, and a request that reported success but left the browser
    // unauthenticated would otherwise show an empty dashboard with no
    // explanation — the same silence, one screen later.
    state.session = await api('/api/session');
    if (!state.session.authenticated) {
      error.textContent = t('auth.notSignedIn');
      return;
    }
    // The chosen mode is applied here rather than on the next load, so the very
    // first dashboard a person sees is already the one they asked for.
    Object.assign(ui, state.session.ui || {});
    applyUIPreferences();
    authChallenge = '';
    form.reset();
    showApp();
  } catch (err) {
    error.textContent = err.message;
  } finally {
    button.disabled = false;
    button.textContent = restore;
  }
});

function showApp() {
  $('#boot').hidden = true;
  $('#auth').hidden = true;
  $('#app').hidden = false;
  applyTranslations($('#app'));
  applyRoleGating();

  // The controls that are built rather than written into index.html: the layout
  // switcher and the account menu in the top bar, the search box beside them, and
  // the period chips on the incident filter. Each is built from the state that was
  // just loaded, and each of the lists it is built from — the five layouts, the
  // windows — lives once in this module instead of being spelled out in the markup
  // and read back out somewhere else.
  buildLayoutSwitcher();
  mountUserArea();
  mountSearch();
  buildIncidentFilter();

  // The icon pickers are wired here rather than inside the forms that carry them,
  // because those forms are rebuilt on every open. One delegated listener set on the
  // document is what survives that.
  iconUI.mount({ api, toast, confirm: confirmDialog });

  // The sensor type chooser, for the same reason: its field is rebuilt with the
  // sensor dialog on every open, and one delegated listener set survives that.
  mountSensorTypePicker();

  // The place and dependency pickers in the same form, wired the same way.
  mountPickPanels();

  // Panels are fixed to the window; keep them on their button when it moves.
  mountPanelPlacement();

  // Tag settings are small and drawn in the detail window and the editor; load
  // them with the page rather than at the first click. An installation that has
  // never made one gets an empty list, and the words still render as plain chips.
  loadTags().catch(() => {});

  applyHash();

  // The first load decides whether the dashboard is usable. If it fails, say so
  // on the boot screen rather than showing empty cards behind a toast that has
  // already faded.
  refresh({ background: false, initial: true });
  syncLive();
  startPolling();
}

/**
 * mountUserArea puts the account menu in the top right.
 *
 * Everything it needs is handed in: the module never reaches back into this
 * file, so it stays a leaf the way users.js is. The session is read through a
 * getter rather than passed once, because signing in again replaces the object
 * and the menu must show the account that is actually signed in.
 */
function mountUserArea() {
  userUI.mount($('#user-menu-toggle'), {
    session: () => state.session,
    ui,
    onChange: persistUI,
    onChangeLayout: () => renderDashboard(),
    roleLabel,
    onSettings: () => setRoute('settings'),
    onSignOut: signOut,
  });
}

/**
 * mountSearch puts the global search in the top bar.
 *
 * A leaf module like the account area: it is handed the state, the role check and
 * the two ways of leaving the field — opening a sensor or changing the view — and
 * reaches into nothing else here. The one request it makes is its own, because the
 * answer is a list of six kinds of thing and no view in this file has a shape for
 * that.
 */
function mountSearch() {
  searchUI.mount($('#search'), $('#search-results'), {
    state: () => state,
    can,
    search: (query) => api(`/api/search?q=${encodeURIComponent(query)}`),
    openDetail,
    setRoute,
    // A settings hit is a hit on a panel that is not on screen until its
    // category has been chosen, so the search needs the same function the tabs
    // use. It sets the state as well as the attributes: the next renderSettings
    // reads the state, and hiding the panel here alone would hold until the next
    // poll and then put the person back on the wrong category.
    showCategory: showSettingsCategory,
  });
}

/**
 * signOut ends the session and puts the sign-in screen back.
 *
 * A reload rather than a rebuilt view: it is the one action that must leave
 * nothing of the last account behind, and a fresh document is the only way to be
 * sure of that in a dashboard that keeps live event streams open.
 */
async function signOut() {
  try {
    await api('/api/logout', { method: 'POST' });
  } catch (err) {
    toast(err.message, 'error');
    return;
  }
  state.stream?.close();
  location.reload();
}

// ---------------------------------------------------------------------------
// live updates
// ---------------------------------------------------------------------------

/**
 * syncLive opens or closes the probe stream, according to the preference.
 *
 * Three states, not two: on and connected, on and waiting to reconnect, and off.
 * The middle one is why the indicator beside it exists at all — a stream that has
 * dropped looks exactly like a system where nothing has gone wrong, and until now
 * there was nowhere that said which of the two you were looking at. Off closes the
 * connection rather than ignoring it: "no live updates" that still holds a request
 * open is not what the word means.
 *
 * It is called from applyUIPreferences, from the boot path and from the indicator
 * itself, and it is idempotent so that none of those has to know what the others
 * did.
 */
function syncLive() {
  const wanted = ui.live !== 'off';
  // Not before the dashboard is on screen: the boot and sign-in paths apply the
  // preferences of somebody who is still looking at the login form.
  const ready = !$('#app').hidden;
  const was = state.liveOn;
  state.liveOn = wanted;
  if (wanted && ready && !state.stream) connectEvents();
  if (!wanted && state.stream) {
    state.stream.close();
    state.stream = null;
  }
  // Coming back on after an absence. An event source delivers what happens while
  // it is open and nothing else; whatever the board missed was never sent and will
  // never be sent again. One refresh brings it up to date, rather than leaving
  // somebody who has just asked to be live looking at a board up to a minute stale.
  // The `=== false` is what keeps this from firing on the first call of a page
  // load, where there is an initial request already in flight.
  if (wanted && ready && was === false) scheduleRefresh(0);
  syncLiveChip();
}

/** The indicator beside the account chip: on, waiting, or off. */
function syncLiveChip() {
  const chip = $('#live-chip');
  if (!chip) return;
  const off = ui.live === 'off';
  const connected = !off && state.stream && state.stream.readyState === EventSource.OPEN;
  const state_ = off ? 'off' : connected ? 'on' : 'waiting';
  chip.dataset.live = state_;
  chip.setAttribute('aria-pressed', off ? 'false' : 'true');
  const label = t(off ? 'live.off' : connected ? 'live.on' : 'live.waiting');
  chip.title = label;
  chip.setAttribute('aria-label', label);
  chip.querySelector('.live-word').textContent = t(off ? 'live.labelOff' : 'live.label');
}

// One listener, on the button that is always there, so that turning live updates
// back on does not need the button to be rebuilt first.
$('#live-chip').addEventListener('click', () => {
  void persistUI('live', ui.live === 'off' ? 'on' : 'off');
});

/* ------------------------------------------------- the ⋮ on a narrow window */

/*
 * Below 1133px the four working controls of the top bar do not fit on one line
 * with everything else, and the bar used to wrap instead — three rows of buttons
 * at 600px, four at 440. They now fold into a panel behind a ⋮, and that is
 * entirely a matter of layout: style.css lays the same elements out as a row or
 * as a panel, and this is only the opening and the closing of it.
 *
 * That is deliberate. A menu that rebuilds the controls would carry a second copy
 * of each one, and every copy is a thing that can fall out of step: a layout
 * switcher that does not know the server refused the change, a tools pill that
 * does not know arranging started, a live chip that missed an event. There is one
 * copy of each, and it is the one the rest of the dashboard already keeps up to
 * date.
 *
 * The panel is not a dialog and does not trap anything: it closes on a click
 * anywhere else, on Escape, and when the window changes width — and the control
 * that was pressed inside it does its work first, because the panel is closed by
 * a listener on the document rather than on the way in.
 */
const topbarEl = $('.topbar');
const topbarMore = $('#topbar-more');
const topbarExtra = $('#topbar-extra');

function topbarMenuOpen() {
  return !!topbarEl && topbarEl.classList.contains('menu-open');
}

function closeTopbarMenu() {
  if (!topbarEl || !topbarMenuOpen()) return;
  topbarEl.classList.remove('menu-open');
  topbarMore?.setAttribute('aria-expanded', 'false');
}

function openTopbarMenu() {
  // Already open means already focused: the tour asserts the panel on every step
  // and must not pull the keyboard out of its own card doing it.
  if (!topbarEl || !topbarMore || topbarMenuOpen()) return;
  topbarEl.classList.add('menu-open');
  topbarMore.setAttribute('aria-expanded', 'true');
  // The first row that can take focus. `offsetParent` is null for everything the
  // panel is hiding — a layout switcher on a page that is not the dashboard, the
  // add button while nothing is being arranged — and focusing one of those would
  // leave the keyboard nowhere.
  $$('#topbar-extra button', topbarEl).find((el) => el.offsetParent !== null)?.focus();
}

topbarMore?.addEventListener('click', (event) => {
  // Otherwise the document listener below sees this same click and closes the
  // panel in the act of opening it.
  event.stopPropagation();
  if (topbarMenuOpen()) closeTopbarMenu();
  else openTopbarMenu();
});

document.addEventListener('click', (event) => {
  if (!topbarMenuOpen()) return;
  if (event.target.closest('#topbar-more')) return;
  closeTopbarMenu();
});

// Escape closes the panel and the search overlay with it. The search overlay has
// its own Escape handling inside search.js, which closes the result list first and
// puts the cursor back in the field — so one press shortens the list, the next one
// puts the bar back, and neither press does both at once.
document.addEventListener('keydown', (event) => {
  if (event.key !== 'Escape') return;
  closeTopbarMenu();
  topbarEl?.classList.remove('search-open');
});

// Only a change of width matters here. On a phone the address bar collapsing
// during a scroll fires a resize as well, and a menu that slammed shut every time
// somebody scrolled the page under it would be a menu nobody could use.
let topbarWidth = window.innerWidth;
window.addEventListener('resize', () => {
  if (window.innerWidth === topbarWidth) return;
  topbarWidth = window.innerWidth;
  closeTopbarMenu();
});

// The magnifier, which is the search field's place in the row whenever the field
// itself is too narrow to be one. It is in the bar and not in the ⋮ on purpose: a
// search box that has to be unfolded out of a menu is a search box nobody on a
// phone uses, and the field comes back at the full width of the bar rather than as
// a row of a panel. Above ~620px there is no magnifier — the field is in the row —
// and this handler is simply unreachable.
$('#topbar-search')?.addEventListener('click', () => {
  closeTopbarMenu();
  topbarEl?.classList.add('search-open');
  const field = $('#search');
  if (!field) return;
  field.focus();
  field.select();
});

// Leaving the field puts the bar back: this is a field laid over the bar, not a
// page. The result list keeps the cursor on purpose while it is being clicked
// (search.js), so choosing a hit does not close the overlay a moment too early.
$('#search')?.addEventListener('blur', () => {
  topbarEl?.classList.remove('search-open');
});

function connectEvents() {
  if (state.stream) state.stream.close();
  const stream = new EventSource('/api/events');
  state.stream = stream;

  // A probe result is one number changing: the latency, the status code, the
  // timestamp, and one more point on the curve. It is patched into the node that
  // sensor is already drawn as, quietly — the value moves and nothing flashes,
  // because nothing happened except a check.
  stream.addEventListener('result', (event) => {
    const payload = JSON.parse(event.data);
    patchSensorFromResult(payload);
  });

  // A status change or an incident is a thing that *happened*, and it is the only
  // moment the flash is worth anything. Both carry the new status and the incident
  // with them, so neither has to be asked for again — the row is rebuilt from
  // what arrived.
  for (const kind of ['status', 'incident']) {
    stream.addEventListener(kind, (event) => {
      let payload = {};
      try {
        payload = JSON.parse(event.data);
      } catch {
        // An event whose body cannot be read is still an event: fall back to the
        // full refresh rather than dropping the change on the floor.
        scheduleRefresh();
        return;
      }
      patchSensorFromEvent(kind, payload);
      // The incident log is its own list with its own filter, and a row appearing
      // in it is the whole point of that screen. It is not rebuilt from the stream
      // — it is asked for again, because the filter decides what belongs in it.
      if (state.route === 'incidents') void loadIncidents().then(renderIncidents).catch(() => {});
    });
  }

  // These two change the lists themselves — a sensor added or removed, a channel
  // renamed — so there is no node to patch and the whole view is redrawn.
  for (const kind of ['sensor', 'channel']) {
    stream.addEventListener(kind, () => scheduleRefresh());
  }

  stream.addEventListener('settings', async () => {
    const fresh = await api('/api/session');
    Object.assign(ui, fresh.ui || {});
    applyUIPreferences();
    renderSettings();
  });

  stream.addEventListener('open', () => syncLiveChip());
  stream.addEventListener('error', () => {
    // EventSource reconnects on its own; the periodic refresh covers the gap. The
    // indicator is moved back to "waiting" so that the gap is visible.
    syncLiveChip();
  });
}

/** Coalesce bursts of events into one refresh. */
function scheduleRefresh(delay = 400) {
  clearTimeout(state.refreshTimer);
  state.refreshTimer = setTimeout(() => refresh({ background: true }), delay);
}

// PROBE_WINDOW is how many probes the summary keeps per sensor, and therefore how
// long the sparkline and the heartbeat under a card are. The client extends both
// series between refreshes, and a client holding a different number of points
// would draw a different curve from the one the next refresh replaces it with.
const PROBE_WINDOW = 40;

/**
 * patchSensorFromResult folds one probe result into the model and redraws the one
 * node that sensor is drawn as.
 *
 * A probe result is a number changing, and that is all. Nothing has happened that
 * anybody needs to be told about, so nothing moves but the number: no flash, no
 * reordering, no lost scroll position. It is the difference between a dashboard
 * that is alive and a dashboard that twitches.
 */
function patchSensorFromResult(event) {
  const view = state.sensors.find((c) => c.id === event.sensor_id);
  if (!view || !event.result) return;
  applyResult(view, event.result);
  // The open window is something somebody is reading; see noteDetailProbe.
  if (state.detail === view.id) noteDetailProbe(view);
  patchSensorNode(view, false);
}

/** applyResult copies one probe result onto the sensor it belongs to. */
function applyResult(view, result) {
  view.latency_ms = result.latency_ms;
  view.status_code = result.status_code;
  view.message = result.message;
  view.last_sensor = result.ts;
  view.sparkline = (view.sparkline || []).concat([result.success ? result.latency_ms : 0]).slice(-PROBE_WINDOW);
  view.heartbeat = (view.heartbeat || []).concat([Boolean(result.success)]).slice(-PROBE_WINDOW);
}

/**
 * patchSensorFromEvent is the other half of the stream: the events that say
 * something happened rather than something was measured.
 *
 * A status change and a new incident carry the new status and the outage with
 * them, so the sensor is brought up to date from what arrived instead of by asking
 * again — and then the node is rebuilt and flashed once. That flash is the whole
 * of what makes a dashboard feel alive rather than merely correct, which is why it
 * is spent here and nowhere else.
 */
function patchSensorFromEvent(kind, payload) {
  const view = state.sensors.find((c) => c.id === payload.sensor_id);
  // An event about a sensor this dashboard has never heard of means the list is
  // older than the event — a sensor added in another tab, or one the search box is
  // filtering out. Refetching is the only honest answer; guessing is not.
  if (!view) {
    scheduleRefresh();
    return;
  }
  if (payload.result) applyResult(view, payload.result);
  if (payload.status) moveSummaryCount(view, payload.status);
  if (kind === 'incident') {
    view.open_incident = payload.incident && !payload.incident.resolved_at ? payload.incident : null;
  }
  // The header is redrawn, not rebuilt: the tiles stay where they are and only
  // their contents are replaced. A rebuild here would tear the mini map's
  // viewport away between two probes.
  if (state.route === 'dashboard') widgetsUI.refresh();
  patchSensorNode(view, true);
}

/**
 * moveSummaryCount moves the header's counts when a status changes.
 *
 * Only the four counts, and only by one: the transition is exactly that. The
 * aggregates beside them — uptime, average latency, probes — are computed by the
 * server over every sensor and every stored probe, and a number invented here
 * would be a number nobody measured. They arrive with the next refresh. The counts
 * do not have to wait for it, because the event says which way each of them went.
 */
function moveSummaryCount(view, next) {
  const before = view.status;
  if (before === next) return;
  view.status = next;
  const summary = state.summary;
  if (!summary) return;
  if (typeof summary[before] === 'number') summary[before] = Math.max(0, summary[before] - 1);
  if (typeof summary[next] === 'number') summary[next] += 1;
}

/**
 * patchSensorNode replaces the one node a sensor is drawn as, and nothing else.
 *
 * This is the whole of the live dashboard, and the reason it is not "draw the grid
 * again". Replacing the container would close every <details> somebody had opened,
 * reset the scroll, drop the keyboard focus, and — on the map — throw away the
 * viewport somebody had panned to, once a minute, forever. A probe cannot change a
 * sensor's place or its group, so the node stays where it is and only its contents
 * are replaced.
 *
 * A node that is not on screen is not a failure. The sensor may be filtered out by
 * the search box, the map may be up — it has no per-sensor node at all — or another
 * tab may be showing.
 */
function patchSensorNode(sensor, flash) {
  if (state.route !== 'dashboard') return;
  if (ui.layout === 'map') {
    // The map has no node per sensor: its pins stand for places, and what a probe
    // changes about a place is the colour of its pin. Recolouring touches neither
    // the tiles nor the viewport, which is the whole reason a map is not "drawn
    // again" here — nobody who has panned somewhere wants to be moved back.
    if (state.mapHandle) state.mapHandle.refresh(state.sensors);
    return;
  }
  const node = sensorNodes($('#groups')).find((el) => Number(el.dataset.id) === sensor.id);
  if (!node) return;
  const html = renderSensorNode(sensor);
  if (!html) return;
  // Through a <template>, so that a <tr> stays a <tr>: assigning table markup to a
  // <div> drops the row and leaves its cells.
  const holder = document.createElement('template');
  holder.innerHTML = html.trim();
  const fresh = holder.content.firstElementChild;
  if (!fresh) return;
  node.replaceWith(fresh);
  patchSiteHead(fresh);
  if (flash) {
    // Set after the node is in the document, so the animation runs once, here, for
    // this node — and taken off again when it is over, because a class left behind
    // is a class that plays again on the next unrelated repaint. Reduced motion is
    // handled where every other animation is: the class goes on either way and the
    // stylesheet's own block is what makes it do nothing.
    fresh.classList.add('is-flash');
    setTimeout(() => fresh.classList.remove('is-flash'), 900);
  }
}

/**
 * renderSensorNode draws a sensor the way the layout currently on screen draws it.
 *
 * The same functions the full render uses, which is the point: a patched node and a
 * rendered one are the same markup by construction, rather than by two pieces of
 * code agreeing today and drifting apart in a month.
 */
function renderSensorNode(sensor) {
  switch (ui.layout) {
    case 'list': return renderListRow(sensor);
    case 'tree': return layouts.treeRow(sensor, layoutContext());
    case 'wall': return layouts.tile(sensor, layoutContext());
    case 'map': return '';
    default: return renderCard(sensor);
  }
}

/**
 * patchSiteHead brings the heading above a sensor back in line with it.
 *
 * A tree's place node and a wall's group carry the worst status underneath them and
 * how many of those sensors are down, and the row that has just been replaced is
 * what changes both. A heading left alone would go on saying that everything here
 * is fine above a red row, and quiet reassurance is the one thing a monitoring
 * dashboard must never give. Only the dot and the count are touched — the
 * <details> and whether it is open belong to the reader, not to the data.
 */
function patchSiteHead(node) {
  const section = node.closest('.site-node, .wall-group');
  if (!section) return;
  const sensors = sensorNodes(section)
    .map((el) => state.sensors.find((c) => c.id === Number(el.dataset.id)))
    .filter(Boolean);
  if (sensors.length) layouts.refreshHead(section, sensors);
}

/**
 * sensorNodes returns the nodes that stand for a sensor, and not the buttons
 * inside them.
 *
 * Every action button carries data-id too — it is how the click handler knows
 * which sensor it was that was pressed — so a plain `[data-id]` query counts each
 * row's five buttons as five more sensors. That is not a rounding error: it made a
 * place with four sensors announce twenty-four, and counted the one sensor that is
 * down five times over.
 *
 * A node that stands for a sensor is one with no other such node above it, which is
 * true of a card, a table row, a tree row and a tile alike, and stays true without a
 * list of four class names to keep in step with the markup.
 */
function sensorNodes(root) {
  return $$('[data-id]', root).filter((el) => el.parentElement && !el.parentElement.closest('[data-id]'));
}

function startPolling() {
  // A slow safety net. SSE does the fast path; this catches a dropped stream
  // on a laptop that was asleep.
  setInterval(() => {
    if (!document.hidden) refresh({ background: true });
  }, 60_000);
}

document.addEventListener('visibilitychange', () => {
  if (!document.hidden && state.session?.authenticated) refresh({ background: true });
});

// ---------------------------------------------------------------------------
// data
// ---------------------------------------------------------------------------

async function refresh({ background = true, initial = false } = {}) {
  try {
    const data = await api('/api/summary');
    state.summary = data.summary;
    state.sensors = data.sensors || [];
    state.recentIncidents = data.incidents || [];
    state.sites = data.sites || [];
    state.groups = data.groups || [];
    if (state.route === 'incidents') await loadIncidents();
    renderAll();
    applyRoleGating();
    maybeStartTour();
    if (state.pendingDetail !== null) {
      const pending = state.pendingDetail;
      state.pendingDetail = null;
      void openDetail(pending);
    }
  } catch (err) {
    if (err.status === 401) {
      state.session = { authenticated: false, setup_required: false };
      showAuth('login');
      return;
    }
    if (initial) {
      showBootError(err.message || String(err), err && err.stack);
      return;
    }
    if (!background) toast(err.message, 'error');
  }
}

async function loadChannels(force = false) {
  if (state.channels.length && !force) return;
  state.channels = await api('/api/channels');
}

async function loadChannelTypes() {
  if (state.channelTypes.length) return state.channelTypes;
  state.channelTypes = await api('/api/channel-types');
  return state.channelTypes;
}

async function loadSettings(force = false) {
  if (state.settings && !force) return state.settings;
  state.settings = await api('/api/settings');
  return state.settings;
}

/**
 * loadSites fetches the places, and is only needed when a view was reached
 * without the summary — the Settings page, where the site list is edited. Any
 * route that arrived through the dashboard already has them, counts included.
 */
async function loadSites(force = false) {
  if (state.sites.length && !force) return state.sites;
  const data = await api('/api/sites');
  state.sites = data.sites || [];
  return state.sites;
}

/**
 * loadWindows fetches the maintenance windows, and the list of kinds with them.
 *
 * The kinds come from the server rather than from a second copy here, the same way
 * the site kinds do: a form that offered its own idea of what a window can be would
 * go on offering a value the server had stopped accepting.
 */
async function loadWindows(force = false) {
  if (state.windows.length && !force) return state.windows;
  const data = await api('/api/maintenance');
  state.windows = data.windows || [];
  state.windowKinds = data.kinds || [];
  return state.windows;
}

// ---------------------------------------------------------------------------
// the guided tour
// ---------------------------------------------------------------------------

let tourOffered = false;

/**
 * maybeStartTour offers the walk-through once, on the first render that has
 * something to point at. A dashboard with no sensors is a dead end for a tour,
 * so the welcome card waits until the examples have arrived.
 */
function maybeStartTour() {
  if (tourOffered || state.route !== 'dashboard' || !state.sensors.length) return;
  if (Number(ui.tour_version || 0) >= TOUR_VERSION) return;
  tourOffered = true;
  // Give the first paint a moment to settle, so the ring lands on the finished
  // layout rather than on a card that is still being sized.
  setTimeout(() => void openTour(), 400);
}

/**
 * openTour runs the walk-through from wherever it was asked for.
 *
 * One entry point for the two places that offer it, because it also has to undo
 * one thing when it is over: the steps that take a card apart pin the card
 * layout, and a person who had saved the wall gets their wall back. The pin is
 * for the duration of the tour and is never written to the account.
 */
async function openTour() {
  // The tour has a step about the example data and skips it when there is none;
  // that count is only known once it has been asked for, and asking afterwards
  // would be asking too late.
  await loadExampleData().catch(() => {});
  const previous = ui.layout;
  // The category the settings page is left on is put back for the same reason the
  // layout is: the tour opens the Access category to point at the user list, and
  // without this the next look at Settings would open there rather than where the
  // person actually was.
  const previousCategory = state.settingsTab;
  // Arranging the header is put aside for the tour: it has a step of its own about
  // the layout switcher, and the switcher stands down while the header is being
  // arranged. The tour is read, not used, so it starts from a page that is at rest.
  widgetsUI.stopArranging();
  const repaint = () => {
    if (state.route === 'dashboard') renderDashboard();
  };
  startTour({
    state,
    mode: ui.mode || 'advanced',
    layout: previous,
    setRoute,
    setLayout: (value) => {
      ui.layout = value;
      applyUIPreferences();
      repaint();
    },
    setSettingsCategory: (key) => showSettingsCategory(key),
    // The bar's panel, for the steps that ring a button in it. On a wide window
    // there is no panel and both of these do nothing.
    setPanel: (on) => (on ? openTopbarMenu() : closeTopbarMenu()),
    onFinish: async (reached) => {
      await rememberTour(reached);
      ui.layout = previous;
      applyUIPreferences();
      showSettingsCategory(previousCategory);
      repaint();
    },
  });
}

/**
 * rememberTour stores how far the person got. A failure here is not the user's
 * problem and must not interrupt them: the worst case is being offered the tour
 * once more.
 */
async function rememberTour(reached) {
  ui.tour_version = String(reached);
  try {
    await api('/api/onboarding/tour', { method: 'POST', body: { version: reached } });
  } catch {
    /* the note is a courtesy; the tour already happened */
  }
}

// ---------------------------------------------------------------------------
// routing
// ---------------------------------------------------------------------------

const ROUTES = ['dashboard', 'incidents', 'channels', 'settings'];

/**
 * parseHash reads a location hash into a view and an optional sensor.
 *
 * "#/sensor/12" is the link the alert messages carry. Without this it falls
 * through to the dashboard and the person who was paged lands on a page that
 * does not say which sensor raised the alarm.
 */
function parseHash(hash) {
  const [name, arg] = String(hash || '').replace(/^#\/?/, '').split('/');
  if (name === 'sensor') {
    const id = Number(arg);
    return { route: 'dashboard', sensor: Number.isFinite(id) && id > 0 ? id : null };
  }
  return { route: ROUTES.includes(name) ? name : 'dashboard', sensor: null };
}

/**
 * applyHash is the one place the URL is turned into a view, used both on the
 * first load and on every hashchange.
 */
function applyHash() {
  const { route, sensor } = parseHash(location.hash);
  if (sensor === null) {
    setRoute(route);
    return;
  }
  // Keep the deep link in the address bar rather than rewriting it to
  // "#/dashboard"; the dialog is opened on top of the dashboard.
  setRoute(route, `#/sensor/${sensor}`);
  state.pendingDetail = sensor;
  void openDetail(sensor);
}

/**
 * The two controls in the top bar that belong to the dashboard: the layout
 * switcher and the button that arranges the header. Both are hidden with the
 * `hidden` attribute rather than with CSS — a control that is merely invisible
 * still takes focus and is still read out — and both go away off the dashboard,
 * because neither does anything anywhere else.
 *
 * The switcher also goes away while the header is being arranged, and that is not
 * tidiness: the header only exists in the card view, so the control that leaves it
 * is not one the person arranging it can use. It is also what pays for the button
 * that replaces it. The top bar is one line at every width the dashboard is read
 * at, and the "add" button that appears beside the tick — the second of the two
 * things somebody arranging a header wants — costs exactly what the switcher
 * gives back.
 */
function syncDashboardControls() {
  const here = state.route === 'dashboard';
  const switcher = $('#layout-switcher');
  if (switcher) switcher.hidden = !here || widgetsUI.isArranging();
  const widgetTools = $('#widgets-tools');
  if (widgetTools) widgetTools.hidden = !here;
}

function setRoute(route, hash) {
  // A plain navigation — a tab click or #/dashboard — has no deep link of its
  // own, so any detail dialog left open belongs to the view being left behind.
  if (hash === undefined && state.detail !== null) $('#detail').close();
  state.route = route;
  for (const button of $$('#tabs .tab')) {
    const active = button.dataset.route === route;
    button.classList.toggle('active', active);
    button.setAttribute('aria-selected', active ? 'true' : 'false');
  }
  for (const view of $$('.view')) view.hidden = view.id !== `view-${route}`;
  syncDashboardControls();
  // Leaving the dashboard stops the map. It keeps a running viewport and its own
  // listeners, and leaving them going behind a hidden tab is work nobody asked
  // for — coming back redraws the view anyway.
  if (route !== 'dashboard') {
    unmountMap();
    // And the one in the header, for the same reason. It is a second map with a
    // second set of listeners; nothing else would ever stop it, because the code
    // that owns it is not the code that owns state.mapHandle.
    widgetsUI.stopMap();
    widgetsUI.closePicker();
  }
  const target = hash || `#/${route}`;
  if (location.hash !== target) history.replaceState(null, '', target);
  renderAll();
  void loadRouteData(route);
}

/**
 * Fetch what a view needs before it can draw itself. Channels and incidents are
 * not part of the summary, so visiting those tabs has to load them.
 */
async function loadRouteData(route) {
  try {
    if (route === 'channels') {
      await loadChannels(true);
      renderChannels();
    } else if (route === 'incidents') {
      await loadIncidents();
      renderIncidents();
    } else if (route === 'settings') {
      // Both of these decide whether a panel exists at all, so they have to be
      // there before the panel is drawn rather than filled in afterwards.
      const jobs = [loadUsers(), loadExampleData(), loadSites(), loadGroups(), loadTags(), loadWindows()];
      if (can('admin')) jobs.push(loadMetricsToken());
      await Promise.all(jobs);
      await renderSettings();
    }
  } catch (err) {
    toast(err.message, 'error');
  }
}

/**
 * loadUsers fetches the accounts, and only for somebody who may see them.
 *
 * The route is admin-only, so asking as anybody else would be a guaranteed 403 in
 * the console on every visit to Settings. Skipping the request entirely keeps the
 * network log clean for the people who would only ever be refused.
 */
async function loadUsers(force = false) {
  if (!can('admin')) {
    state.users = [];
    state.roles = [];
    return;
  }
  if (state.usersLoaded && !force) return;
  const data = await api('/api/users');
  state.users = data.users || [];
  state.roles = data.roles || [];
  state.usersLoaded = true;
}

async function loadExampleData(force = false) {
  if (state.exampleData && !force) return;
  state.exampleData = await api('/api/examples');
}

/**
 * loadIncidents fetches the incident list through whatever the filter line is
 * set to.
 *
 * The window is left out when it is "all": the parameter is what the server reads
 * to place a start, and a start that means "everything" is better spelled by not
 * sending one than by sending a year of history the server then has to trust.
 */
async function loadIncidents() {
  const f = state.incidentFilter;
  const params = new URLSearchParams({ limit: '200' });
  if (f.status) params.set('state', f.status);
  if (f.sensor) params.set('sensor', String(f.sensor));
  if (f.range && f.range !== 'all') params.set('range', f.range);
  if (f.query) params.set('q', f.query);
  state.incidents = await api(`/api/incidents?${params}`);
}

$('#tabs').addEventListener('click', (event) => {
  const button = event.target.closest('.tab');
  if (button) setRoute(button.dataset.route);
});

window.addEventListener('hashchange', () => {
  // Always re-apply rather than comparing with state.route: "#/sensor/1" and
  // "#/sensor/2" share the route, so a comparison would swallow the second one.
  if (location.hash !== `#/${state.route}`) applyHash();
});

/**
 * renderAll redraws whichever view is on screen.
 *
 * It is async because one of the four is: the settings page fetches the accounts, the
 * example data, the places and the maintenance windows before it can draw a panel, and
 * it rebuilds #settings-body in one go when that comes back. Callers that do not await
 * it are fire-and-forget and always were — but repaintForLanguage does await it, and
 * that is not tidiness. A repaint that returns before the page underneath has been
 * rebuilt is a repaint that has not happened yet: the next thing the reader does lands
 * on a control that is about to be replaced, which is how a language switch could lose
 * the focus somebody had in the settings form.
 */
async function renderAll() {
  if (state.route === 'dashboard') renderDashboard();
  if (state.route === 'incidents') renderIncidents();
  if (state.route === 'channels') renderChannels();
  if (state.route === 'settings') await renderSettings();
}

// ---------------------------------------------------------------------------
// dashboard
// ---------------------------------------------------------------------------

$('#search').addEventListener('input', (event) => {
  state.query = event.target.value.trim().toLowerCase();
  renderDashboard();
});

function visibleSensors() {
  if (!state.query) return state.sensors;
  return state.sensors.filter((c) => {
    const haystack = [c.name, c.target, c.type, c.group, ...(c.tags || [])].join(' ').toLowerCase();
    return haystack.includes(state.query);
  });
}

function renderDashboard() {
  // The header first, and through the strip's own refresh rather than a rebuild:
  // this runs on every poll, and rebuilding would take the mini map's viewport
  // with it once a minute, forever.
  widgetsUI.refresh();
  const sensors = visibleSensors();
  const groups = $('#groups');
  groups.className = `groups layout-${ui.layout}`;

  $('#dashboard-empty').hidden = state.sensors.length !== 0;
  if (state.sensors.length === 0) {
    unmountMap();
    groups.innerHTML = '';
    return;
  }
  if (sensors.length === 0) {
    unmountMap();
    groups.innerHTML = emptyBlock(t('dashboard.noneMatching', { query: state.query }));
    return;
  }

  // Tear the map down before its container is refilled. It is a live element
  // with its own listeners and a running viewport, and replacing the markup
  // around it would leave all of that pointing at a container that is gone.
  unmountMap();

  switch (ui.layout) {
    case 'list':
      groups.innerHTML = renderByGroup(sensors, renderList);
      break;
    case 'tree':
      groups.innerHTML = layouts.renderTree(sensors, state.sites, layoutContext());
      break;
    case 'wall':
      groups.innerHTML = layouts.renderWall(sensors, state.sites, layoutContext());
      break;
    case 'map':
      groups.innerHTML = '<div id="map-host" class="map-host"></div>';
      void mountMap();
      break;
    default:
      groups.innerHTML = renderByGroup(sensors, (items) => `<div class="cards">${items.map(renderCard).join('')}</div>`);
  }
}

/**
 * emptyBlock is the one shape an empty panel, list or table has.
 *
 * The sentence is the one the panel already had — what would put something there —
 * and this only decides how it is drawn. A table with no rows, a list with nothing
 * in it and a form that has nothing to show all get the same box, so that "there is
 * nothing here" looks the same everywhere rather than looking like a mistake in
 * some places and like a sentence in others.
 */
function emptyBlock(text, { title = '', action = '' } = {}) {
  return `<div class="empty empty-inline">
    ${title ? `<h2>${esc(title)}</h2>` : ''}
    <p>${esc(text)}</p>
    ${action}
  </div>`;
}

/** loadingLine is where something is about to be, in the shape it will have. */
function loadingLine(text) {
  return `<div class="loading loading-inline">${esc(text)}</div>`;
}

/**
 * renderByGroup lays sensors out under the heading of the group they belong to,
 * in the order they arrived. It is what the card and list views have always
 * done; the tree and the wall do their own, deeper, nesting.
 *
 * The heading wears what the settings say about the group — its symbol and its
 * colour — and a group nobody has said anything about keeps the plain heading it
 * has always had. That is the ordinary case, so it is the one the markup falls back
 * to rather than the one it is special-cased for.
 */
function renderByGroup(sensors, render) {
  const byGroup = new Map();
  for (const sensor of sensors) {
    const key = sensor.group || '';
    if (!byGroup.has(key)) byGroup.set(key, []);
    byGroup.get(key).push(sensor);
  }

  // The groups in the order somebody arranged, not the order their first sensor
  // happens to arrive in. The ungrouped sensors stay last: they are not a group
  // somebody can place.
  const order = (name) => {
    if (!name) return Number.MAX_SAFE_INTEGER;
    return iconUI.groupOf(state.groups, name)?.sort_order ?? 0;
  };
  const names = [...byGroup.keys()].sort((a, b) => {
    if (!a || !b) return a === b ? 0 : (a ? -1 : 1);
    return order(a) - order(b) || a.localeCompare(b);
  });

  const admin = can('admin');
  let html = '';
  for (const group of names) {
    const items = byGroup.get(group);
    if (group) {
      const settings = iconUI.groupOf(state.groups, group);
      const down = items.filter((c) => c.status === 'down').length;
      // The same sentence the tree and the wall put on their headings, so that a
      // group says how much is in it the same way in all four views.
      const count = down > 0
        ? t('sites.sensorCountDown', { count: items.length, down })
        : t('sites.sensorCount', { count: items.length });
      html += `<h2 class="group-title"${iconUI.colourAttr(settings)} data-group-heading="${esc(group)}"` +
        `${admin ? ' draggable="true"' : ''}>` +
        `${admin ? '<span class="drag-grip" aria-hidden="true" title="' + esc(t('groups.dragHint')) + '">⠿</span>' : ''}` +
        `${iconUI.groupIcon(settings)}<span class="group-name">${esc(group)}</span>` +
        `<span class="group-count">${esc(count)}</span></h2>`;
    }
    html += render(items);
  }
  return html;
}

/**
 * layoutContext is what the tree and the wall are handed besides the sensors.
 *
 * The callbacks are the same ones the cards already use, so every view opens the
 * same detail and offers the same buttons — the layouts module never learns what
 * any of it does, it only draws. The groups come along as data rather than as a way
 * to ask, so that a layout can draw a heading without knowing where the settings
 * came from.
 */
function layoutContext() {
  return { statusPill, cardActions, sparkline: C.sparkline, openDetail, groups: state.groups };
}

/**
 * mountMap draws the map, with the tile source the settings carry.
 *
 * The settings request is made here rather than at boot because only this one
 * view needs it, and it is made against a re-queried host: the fetch can come
 * back after another refresh has already replaced the container, and mounting
 * into the element that is gone would draw a map nobody is looking at.
 */
async function mountMap() {
  const host = $('#map-host');
  if (!host) return;
  const settings = state.settings || (await loadSettings().catch(() => null));
  const again = $('#map-host');
  if (!again || ui.layout !== 'map') return;
  const map = (settings && settings.map) || {};
  state.mapHandle = mapUI.mount(again, {
    sites: state.sites,
    sensors: state.sensors,
    tileURL: map.tile_url || '',
    attribution: map.attribution || '',
    // "off" is the only value that means off: a missing row, which is every
    // installation that never opened the panel, means the borders are drawn.
    borders: map.borders !== 'off',
    onOpenSensor: openDetail,
  });
}

/** Stop the map, if one is running. Safe to call when none is. */
function unmountMap() {
  if (state.mapHandle) {
    state.mapHandle.unmount();
    state.mapHandle = null;
  }
}

/**
 * mountWidgets wires the dashboard header.
 *
 * The drawing lives in widgets.js, which is a leaf the way search.js is: it is
 * handed the state and the four things a tile's button can do, and reaches back
 * into nothing here. The one thing that goes the other way is the new list of
 * widgets, which comes back through `onChange` and is written with the same
 * persistUI every other preference uses — so a list the server refuses springs
 * back on its own, exactly like a theme that was refused.
 */
function mountWidgets() {
  widgetsUI.mount($('#summary'), $('#widgets-tools'), {
    state: () => state,
    // The clock reads one of them — see clockTime in that module — and reads it through
    // a getter rather than once, because the tile is drawn before any preference has
    // been chosen and again after somebody changes one.
    ui: () => ui,
    can,
    openDetail,
    setRoute,
    newSensor: () => openSensorEditor(null),
    toggleLive: () => persistUI('live', ui.live === 'off' ? 'on' : 'off'),
    // The tiles need the tile server and its credit, which live on the map
    // settings. Read through a getter because the strip is drawn before the
    // settings have ever been fetched, and again once they have.
    mapSettings: () => {
      const map = (state.settings && state.settings.map) || {};
      return {
        tileURL: map.tile_url || '',
        attribution: map.attribution || '',
        borders: map.borders !== 'off',
      };
    },
    onChange: (list) => persistUI('widgets', list.join(',')),
    onArranging: () => syncDashboardControls(),
  });
}

/** statusWord is the status as a word. The one place the five are named. */
function statusWord(status) {
  const key = { up: 'status.up', down: 'status.down', warn: 'status.warn', paused: 'status.paused' }[status]
    || 'status.unknown';
  return t(key);
}

/**
 * channelKey, channelWord and channelTone describe where a build came from.
 *
 * The channel is stamped into the binary by whichever workflow produced it, because a
 * container cannot see the tag it was pulled by: an installation following `latest` and
 * one following `dev` are identical from the inside — the same version, the same commit,
 * the same date whenever both follow the same push. It is the first thing to look at
 * when a feature is not where it should be, and on a machine running two containers it
 * is the only thing that tells them apart.
 *
 * An unknown value falls to `local` rather than to a blank. A channel nothing recognises
 * is a build nobody can place, and that is what `local` means.
 */
function channelKey(channel) {
  return channel === 'release' || channel === 'dev' ? channel : 'local';
}

function channelWord(channel) {
  return t('settings.channelName.' + channelKey(channel));
}

// Green for a release and violet for anything else, which is the same violet the
// maintenance badge uses for "this is what I said it would be". A local build is not a
// problem and should not look like one.
function channelTone(channel) {
  return channelKey(channel) === 'release' ? 'up' : 'maint';
}

/**
 * statusPill draws the state of one sensor: what the probe found, and — while a
 * maintenance window is running over it — that the state is expected.
 *
 * Two pills and not one recoloured one. The status stays whatever the probe found,
 * because a card that went purple while the box was genuinely unreachable would be
 * lying about the one thing a card is for. What the second pill adds is the sentence
 * somebody needs in order to decide whether to get up: this one is down, and I
 * arranged it.
 *
 * It takes the sensor rather than the status word, which is what puts the second pill
 * in every place a state is drawn — the card, the table row, the tree branch, the wall
 * tile — from one function. A view added later cannot be the only one without it.
 */
/**
 * tagChip draws one tag: its symbol and colour when it has settings, the word
 * alone when it does not. A tag with no settings is still a tag, so the plain
 * shape has to be the ordinary one and not a broken one.
 */
function tagChip(name) {
  const tag = iconUI.groupOf(state.tags, name);
  const attr = tag && tag.colour ? ` data-colour="${esc(tag.colour)}"` : '';
  const icon = tag ? iconUI.groupIcon(tag, 14) : '';
  return `<span class="tag-chip"${attr}>${icon}${esc(name)}</span>`;
}

function statusPill(sensor) {
  const s = sensor || {};
  // The pill and its badge are one box, and that is not tidiness: the tree draws a
  // row as a five-column grid — status, name, sparkline, uptime, buttons — and two
  // elements where the first column expected one pushed everything after it into
  // the wrong column and wrapped the last one onto a second line. A sensor that was
  // excused was therefore the one row in the tree that came apart, which is exactly
  // the row somebody is looking at when something is wrong.
  return `<span class="pill-group">` +
    `<span class="pill st-${esc(s.status)}"><i class="dot"></i>${esc(statusWord(s.status))}</span>` +
    excuseBadge(s) + `</span>`;
}

/**
 * excuseBadge is the marker for a sensor that is down for a reason that is not its own,
 * or the empty string when the status is its own business.
 *
 * One function because there are two places that draw it — the cards and the detail
 * dialog — and they had drifted apart within the hour: the dialog was still asking for
 * `in_maintenance`, which is the field this replaced, so a sensor that the card behind
 * it was marking as Dependent showed up in the dialog as plain "Down".
 */
function excuseBadge(sensor) {
  const s = sensor || {};
  if (s.excuse === 'dependency') {
    return `<span class="pill st-dep" title="${esc(t('dependent.badgeHint'))}">${esc(t('dependent.badge'))}</span>`;
  }
  if (s.excuse === 'maintenance') {
    return `<span class="pill st-maint" title="${esc(t('windows.badgeHint'))}">${esc(t('windows.badge'))}</span>`;
  }
  return '';
}

/**
 * button builds one icon button for a card.
 *
 * Two things can stand in the way of a button doing something, and both are said
 * out loud rather than hidden. A sensor declared in the configuration file cannot
 * be changed from the dashboard at all, whatever the role; anything else the
 * signed-in account may not do is greyed out and names the role that would be
 * allowed — a viewer therefore sees the same card as an administrator, and can see
 * that pausing a sensor is a thing this dashboard does, just not something they
 * do. Hiding the buttons instead would make the dashboard a different product
 * depending on who is looking.
 */
function button(action, id, glyph, label, roles, managed, cls = '') {
  const blocked = managed ? t('sensor.managed') : denied(roles);
  const title = blocked || label;
  return `<button class="icon-btn${cls}" data-action="${action}" data-id="${id}"
    title="${esc(title)}" aria-label="${esc(title)}"${blocked ? ' disabled' : ''}>${glyph}</button>`;
}

function cardActions(sensor) {
  // Acknowledge hangs off the outage, not the sensor, so the button is only
  // there while the sensor is actually down — a paused sensor never is.
  const open = sensor.open_incident;
  const ack = open
    ? button(open.acked_at ? 'unack' : 'ack', sensor.id, open.acked_at ? '🔔' : '🔕',
      open.acked_at ? t('ack.undo') : t('ack.action'), 'operator', false,
      open.acked_at ? ' is-acked' : '')
    : '';
  const managed = sensor.managed;
  const pausable = sensor.enabled ? t('status.paused') : t('status.up');
  return `
    <div class="card-actions">
      ${ack}
      ${button('up', sensor.id, '↑', t('sensor.moveUp'), 'admin', managed)}
      ${button('down', sensor.id, '↓', t('sensor.moveDown'), 'admin', managed)}
      ${button('probe', sensor.id, '⟳', t('sensor.runNow'), 'operator', managed)}
      ${button('toggle', sensor.id, sensor.enabled ? '⏸' : '▶', pausable, 'operator', managed)}
      ${button('edit', sensor.id, '✎', t('sensor.edit'), 'admin', managed)}
    </div>`;
}

function cardStats(sensor) {
  return `
    <div class="card-stats">
      <span title="${esc(t('summary.uptime'))}">${esc(t('summary.dayShort'))} <b>${esc(C.fmtPct(sensor.uptime_24h))}</b></span>
      <span>${esc(t('summary.weekShort'))} <b>${esc(C.fmtPct(sensor.uptime_7d))}</b></span>
      <span>${esc(t('summary.monthShort'))} <b>${esc(C.fmtPct(sensor.uptime_30d))}</b></span>
      <span class="muted">${esc(t('detail.averageShort'))} ${esc(C.fmtLatency(sensor.avg_latency_ms))}</span>
    </div>`;
}

/**
 * renderCard draws one sensor as a card.
 *
 * The symbol leads the name, and it is the sensor's own: nothing here asks what
 * type it is. A ping sensor and an HTTP sensor on the same box are two rows of one
 * story, and which drawing somebody chose for each is more use on a dashboard than
 * the type they already know.
 */
function renderCard(sensor) {
  const down = sensor.status === 'down';
  const acked = sensor.open_incident?.acked_at;
  const example = sensor.origin === 'example';
  return `
    <article class="card st-${esc(sensor.status)}${down ? ' is-down' : ''}${sensor.excuse ? ' in-excused in-' + esc(sensor.excuse) : ''}${acked ? ' is-acked' : ''}${example ? ' is-example' : ''}" data-id="${sensor.id}" data-origin="${esc(sensor.origin || '')}" tabindex="0">
      <header class="card-head">
        ${statusPill(sensor)}
        <h3 class="card-name" title="${esc(sensor.name)}">${iconUI.iconMarkup(sensor.icon, { size: 16, cls: 'icon-lead' })}${esc(sensor.name)}</h3>
        ${example ? `<span class="pill badge-example" title="${esc(t('examples.badgeHint'))}">${esc(t('examples.badge'))}</span>` : ''}
        ${acked ? `<span class="pill st-acked" title="${esc(t('ack.badge'))}">🔕 ${esc(t('ack.short'))}</span>` : ''}
      </header>
      <p class="card-target" title="${esc(sensor.target)}">${esc(sensor.target)}</p>
      ${sensor.message ? `<p class="card-message">${esc(sensor.message)}</p>` : ''}
      <div class="card-spark">${C.sparkline(sensor.sparkline || [], { width: 240, height: 34 })}</div>
      <footer class="card-foot">
        ${cardStats(sensor)}
        <span class="card-meta muted">${esc(C.fmtAgo(sensor.last_sensor))}</span>
        ${cardActions(sensor)}
      </footer>
    </article>`;
}

/**
 * renderListRow is one sensor as one table row.
 *
 * Split out from renderList because the live dashboard replaces a single row when
 * a probe lands, and a row built by a second, parallel piece of code is a row that
 * will eventually differ from the ones the table draws. It is the row the table
 * would have drawn, on its own.
 */
function renderListRow(sensor) {
  return `
    <tr class="row st-${esc(sensor.status)}${sensor.status === 'down' ? ' is-down' : ''}${sensor.excuse ? ' in-excused in-' + esc(sensor.excuse) : ''}" data-id="${sensor.id}">
      <td>${statusPill(sensor)}</td>
      <td class="cell-name">
        <b>${iconUI.iconMarkup(sensor.icon, { size: 16, cls: 'icon-lead' })}${esc(sensor.name)}</b>
        <span class="muted">${esc(sensor.target)}</span>
      </td>
      <td>${C.sparkline(sensor.sparkline || [], { width: 140, height: 26 })}</td>
      <td class="num">${esc(C.fmtPct(sensor.uptime_24h))}</td>
      <td class="num">${esc(C.fmtLatency(sensor.latency_ms))}</td>
      <td class="muted">${esc(C.fmtAgo(sensor.last_sensor))}</td>
      <td>${cardActions(sensor)}</td>
    </tr>`;
}

function renderList(sensors) {
  const rows = sensors.map(renderListRow).join('');
  return `<div class="table-wrap"><table class="sensors-table">
    <thead><tr>
      <th></th><th>${esc(t('field.sensor'))}</th><th></th>
      <th class="num">24h</th><th class="num">${esc(t('field.latency'))}</th>
      <th>${esc(t('detail.lastProbe'))}</th><th></th>
    </tr></thead>
    <tbody>${rows}</tbody></table></div>`;
}

// Card and row interactions.
$('#groups').addEventListener('click', async (event) => {
  const action = event.target.closest('[data-action]');
  const holder = event.target.closest('[data-id]');
  if (!holder) return;
  const id = Number(holder.dataset.id);

  if (!action) {
    openDetail(id);
    return;
  }
  event.stopPropagation();
  const sensor = state.sensors.find((c) => c.id === id);
  if (!sensor) return;

  try {
    switch (action.dataset.action) {
      case 'probe': {
        action.disabled = true;
        const result = await api(`/api/sensors/${id}/probe`, { method: 'POST' });
        toast(`${sensor.name}: ${result.success ? t('sensor.testOk') : t('sensor.testFail')} · ${C.fmtLatency(result.latency_ms)}`, result.success ? 'ok' : 'error');
        await refresh({ background: true });
        break;
      }
      case 'toggle':
        // A narrow request of its own rather than the whole sensor: pausing is the
        // one change an operator may make, and sending every field back would mean
        // sending fields they are not allowed to write. The server takes exactly
        // this one, and refuses anything else.
        //
        // Which is also what makes sending it bare safe. A whole-sensor payload has
        // to carry every field the sensor has, and the day it misses one — a site, a
        // parent — the pause quietly clears it.
        await api(`/api/sensors/${id}`, { method: 'PATCH', body: { enabled: !sensor.enabled } });
        await refresh({ background: true });
        break;
      case 'edit':
        openSensorEditor(sensor);
        break;
      case 'ack':
        openAckDialog(sensor.open_incident?.id);
        break;
      case 'unack':
        await unack(sensor.open_incident?.id);
        break;
      case 'up':
      case 'down':
        await moveSensor(sensor, action.dataset.action === 'up' ? -1 : 1);
        break;
      default:
        break;
    }
  } catch (err) {
    toast(err.message, 'error');
    action.disabled = false;
  }
});

$('#groups').addEventListener('keydown', (event) => {
  if (event.key !== 'Enter') return;
  const holder = event.target.closest('[data-id]');
  if (holder) openDetail(Number(holder.dataset.id));
});

/** Move a sensor up or down within the display order. */
async function moveSensor(sensor, delta) {
  const ids = state.sensors.map((c) => c.id);
  const index = ids.indexOf(sensor.id);
  const target = index + delta;
  if (index < 0 || target < 0 || target >= ids.length) return;
  [ids[index], ids[target]] = [ids[target], ids[index]];
  await api('/api/sensor-order', { method: 'PUT', body: { ids } });
  await refresh({ background: true });
}

// ---------------------------------------------------------------------------
// sensor detail
// ---------------------------------------------------------------------------

async function openDetail(id) {
  const sensor = state.sensors.find((c) => c.id === id);
  // Not found: either the summary has not arrived yet (the pending queue in
  // refresh will call again) or the sensor is gone. Either way there is nothing
  // to draw, and saying so would be a lie about a link that is simply early.
  if (!sensor) return;
  state.detail = id;
  state.pendingDetail = null;
  // Keep the URL on the sensor so the back button returns to the dashboard, and
  // so copying the address bar shares the same link the alert sent.
  if (location.hash !== `#/sensor/${id}`) history.replaceState(null, '', `#/sensor/${id}`);

  const sheet = $('#detail');
  // The symbol the card and the tree already draw, in the window's own heading.
  // Without it the dialog is the one place a sensor appears without the thing that
  // tells it apart at a glance, and a window that opens on a bare name reads like a
  // different sensor from the card it was opened from.
  $('#detail-title').innerHTML =
    iconUI.iconMarkup(sensor.icon, { size: 20, cls: 'icon-lead' }) + esc(sensor.name);
  detailProbes = 0;
  syncDetailStatus(sensor);
  hideDetailNotice();
  const open = sensor.open_incident;
  const ackButton = open
    ? (open.acked_at
      ? `<button class="btn btn-ghost" data-action="unack">🔔 ${esc(t('ack.undo'))}</button>`
      : `<button class="btn" data-action="ack">🔕 ${esc(t('ack.action'))}</button>`)
    : '';
  $('#detail-actions').innerHTML = `
    <div class="range-picker" id="range-picker">
      ${['1h', '24h', '7d', '30d', '90d'].map((r) => `<button class="chip${r === state.range ? ' active' : ''}" data-range="${r}">${esc(t(`range.${r}`))}</button>`).join('')}
    </div>
    ${ackButton ? `<div class="detail-ack">${ackButton}</div>` : ''}`;
  $('#detail-body').innerHTML = loadingLine(t('incidents.loading'));
  sheet.showModal();
  // The keystroke that opened this window must not land inside it. Enter in the
  // search box runs this from a keydown handler and the browser has not finished
  // with that key: the `keypress` still to come goes to whatever holds focus by
  // then, and a `keypress` on a <button> is an activation. showModal() has just
  // focused the × button, so the window opened and shut on the one key — and
  // preventDefault() on the keydown does not help, because cancelling a keydown
  // does not cancel its keypress. Focusing the sheet itself leaves the tail of
  // the keystroke nothing to press. It is also where somebody with a keyboard
  // should land: this window is a thing to read rather than a form to fill, and
  // the first Tab from here reaches the range chips.
  sheet.focus();

  $('#range-picker').addEventListener('click', (event) => {
    const chip = event.target.closest('[data-range]');
    if (!chip) return;
    state.range = chip.dataset.range;
    $$('#range-picker .chip').forEach((c) => c.classList.toggle('active', c === chip));
    loadDetailBody(id);
  });

  await loadDetailBody(id);
}

async function loadDetailBody(id) {
  const body = $('#detail-body');
  const range = state.range;
  try {
    const [series, uptime, results] = await Promise.all([
      api(`/api/sensors/${id}/series?range=${range}`),
      api(`/api/sensors/${id}/uptime?range=${range}`),
      api(`/api/sensors/${id}/results?limit=40`),
    ]);
    if (state.detail !== id) return;
    body.innerHTML = detailMarkup(id, series, uptime, results);
    // Everything that had accumulated is on screen now.
    detailProbes = 0;
    hideDetailNotice();
  } catch (err) {
    body.innerHTML = `<p class="form-error">${esc(err.message)}</p>`;
  }
}

// detailProbes counts the probes that have landed since the window's body was
// last drawn. It is not how many were missed — nothing here is missed, the model
// is patched either way — but how much of what is on screen has aged.
let detailProbes = 0;

/**
 * syncDetailStatus moves the pill in the window's header, and nothing else.
 *
 * It takes the sensor rather than the status word so that a window open across the
 * start of a maintenance window picks the marker up with the next probe, the same way
 * it picks up a status change — the one thing in this dialog that is cheap enough to
 * redraw under somebody who is reading it.
 */
function syncDetailStatus(sensor) {
  const el = $('#detail-status');
  if (!el) return;
  const c = sensor || {};
  el.className = `pill st-${esc(c.status)}`;
  el.innerHTML = `<i class="dot"></i>${esc(statusWord(c.status))}`;
  const heading = $('#detail-title');
  if (!heading || !heading.parentElement) return;
  // The maintenance pill lives beside the status pill in the header, built the same
  // way the cards build it, so the two cannot drift apart.
  const existing = heading.parentElement.querySelector('.pill.st-maint');
  if (c.excuse && !existing) {
    heading.insertAdjacentHTML('afterend', excuseBadge(c));
  } else if (!c.excuse && existing) {
    existing.remove();
  } else if (c.excuse && existing) {
    // The reason can change while the window is open — a parent that was down recovers
    // and a maintenance window begins — so the badge is replaced rather than kept.
    existing.outerHTML = excuseBadge(c);
  }
}

/**
 * noteDetailProbe counts a probe into the open window and offers to redraw it.
 *
 * The body is deliberately *not* rebuilt when the sensor being read is probed.
 * The latency curve would rescale under the cursor, the axis would jump, the
 * table would reorder — once a minute, on the one screen somebody is actually
 * studying. So the pill follows the status, which is a single word and costs
 * nothing to move, and everything else is offered: a quiet line saying how many
 * probes have gone by, which the reader presses when they are ready.
 */
function noteDetailProbe(sensor) {
  detailProbes += 1;
  syncDetailStatus(sensor);
  const notice = $('#detail-notice');
  if (!notice) return;
  notice.hidden = false;
  notice.textContent = detailProbes === 1
    ? t('detail.newProbe', { count: detailProbes })
    : t('detail.newProbes', { count: detailProbes });
}

function hideDetailNotice() {
  const notice = $('#detail-notice');
  if (notice) notice.hidden = true;
}

function detailMarkup(id, series, uptime, results) {
  const sensor = state.sensors.find((c) => c.id === id) || {};
  const stats = uptime.uptime || {};
  const summary = uptime.incidents || {};
  const width = C.widthOf(body(), 720);

  const outage = sensor.open_incident;
  const acked = outage?.acked_at
    ? `<span class="ack-note">🔕 ${esc(t('ack.short'))} · ${esc(C.fmtDateTime(outage.acked_at))}${outage.ack_note ? ` · ${esc(outage.ack_note)}` : ''}</span>`
    : '';
  const open = outage
    ? `<div class="banner crit">${esc(t('detail.openSince'))} ${esc(C.fmtDateTime(outage.started_at))} · ${esc(C.fmtDuration(Date.now() - new Date(outage.started_at).getTime()))} ${acked}</div>`
    : '';

  // What this sensor sits behind, and what sits behind it.
  //
  // The dialog is where somebody has gone to look at one sensor, and the relationship is
  // the one thing about it that is not visible on the card: the badge says "expected"
  // when a parent is down, and says nothing at all when everything is fine. That is the
  // case this is for — a sensor that is behind another one and currently green, where
  // the only clue was the editor.
  //
  // Both directions, because they answer different questions: what happens to this one
  // if something else goes, and what happens to other people's sensors if this one does.
  const parent = (state.sensors || []).find((c) => c.name === sensor.parent);
  const dependents = (state.sensors || []).filter((c) => c.parent === sensor.name);
  const links = [
    sensor.parent
      ? `<span>${esc(t('detail.dependsOn', { name: sensor.parent }))}${
          parent ? ` <span class="pill st-${esc(parent.status)}">${esc(statusWord(parent.status))}</span>` : ''}</span>`
      : '',
    dependents.length === 1
      ? `<span>${esc(t('detail.dependedOnBy'))}</span>`
      : dependents.length
        ? `<span>${esc(t('detail.dependedOnByPlural', { count: dependents.length }))}</span>`
        : '',
  ].filter(Boolean).join('');
  const relationships = links ? `<p class="muted small detail-links">${links}</p>` : '';
  const tags = (sensor.tags || []).map(tagChip).join('');
  const tagLine = tags ? `<p class="detail-tags">${tags}</p>` : '';

  const rows = (results || []).map((r) => `
    <tr class="${r.success ? '' : 'row-crit'}">
      <td class="muted">${esc(C.fmtDateTime(r.ts))}</td>
      <td>${r.success ? esc(t('status.up')) : esc(t('status.down'))}</td>
      <td class="num">${esc(C.fmtLatency(r.latency_ms))}</td>
      <td class="muted">${esc(r.message)}</td>
    </tr>`).join('');

  return `
    ${open}
    <p class="muted detail-sub">${esc(sensor.type)} · ${esc(sensor.target)}${sensor.group ? ` · ${esc(sensor.group)}` : ''}</p>
    ${tagLine}
    ${relationships}
    <div class="detail-stats">
      <div class="stat"><span class="stat-label">${esc(t('detail.uptime'))}</span><span class="stat-value">${esc(C.fmtPct(stats.percent))}</span></div>
      <div class="stat"><span class="stat-label">${esc(t('detail.avg'))}</span><span class="stat-value">${esc(C.fmtLatency(stats.latency_ms))}</span></div>
      <div class="stat"><span class="stat-label">${esc(t('detail.incidents'))}</span><span class="stat-value">${summary.count ?? 0}</span></div>
      <div class="stat"><span class="stat-label">${esc(t('detail.downtime'))}</span><span class="stat-value">${esc(C.fmtDuration(summary.down_ms))}</span></div>
    </div>
    <h3 class="section-title">${esc(t('field.latency'))}</h3>
    ${C.latencyChart(series.points || [], { width, height: 190, emptyText: t('sensor.none') })}
    <h3 class="section-title">${esc(t('sensor.history'))}</h3>
    ${C.uptimeStrip(series.points || [], { width, height: 32 })}
    <h3 class="section-title">${esc(t('sensor.recent'))}</h3>
    <div class="table-wrap"><table class="results-table">
      <thead><tr><th>${esc(t('field.time'))}</th><th>${esc(t('field.result'))}</th><th class="num">${esc(t('field.latency'))}</th><th>${esc(t('field.message'))}</th></tr></thead>
      <tbody>${rows || `<tr><td colspan="4" class="muted">${esc(t('sensor.none'))}</td></tr>`}</tbody>
    </table></div>`;
}

function body() {
  return $('#detail-body');
}

// ---------------------------------------------------------------------------
// incidents
// ---------------------------------------------------------------------------

// The windows the incident filter offers. "All time" is first-class here rather
// than a last resort: a list of outages is read to answer "has this happened
// before", and that question does not have a window. The sensor detail window
// starts at one hour because a curve is read per probe; this is not that.
const INCIDENT_RANGES = ['24h', '7d', '30d', '90d', 'all'];

/**
 * buildIncidentFilter fills the chip row and wires the whole filter line.
 *
 * The listeners are delegated on the containers, as everywhere else in this file,
 * so the controls can be marked up as often as the state wants without anyone
 * having to think about rewiring them — and wiring them is a separate step from
 * filling them for one further reason.
 *
 * The chips carry a translated word each, and this used to be guarded so that the
 * whole function ran once per page. That guard was invisible until the interface
 * was translated: the row was built in English on the first draw and the guard
 * then refused to build it again, so switching the language left five English
 * chips sitting under a German heading, on a page where every other word had
 * changed. The catalogue had the German for all five the whole time. Rebuilding
 * the markup costs five buttons and leaves the listeners where they are.
 */
function buildIncidentFilter() {
  const chips = $('#incident-range');
  if (!chips) return;

  if (!chips.dataset.wired) {
    chips.dataset.wired = '1';
    chips.addEventListener('click', (event) => {
      const chip = event.target.closest('[data-range]');
      if (chip) void setIncidentFilter({ range: chip.dataset.range });
    });

    $('#incident-status').addEventListener('click', (event) => {
      const button = event.target.closest('[data-state]');
      if (button) void setIncidentFilter({ status: button.dataset.state });
    });

    $('#incident-sensor').addEventListener('change', (event) => {
      void setIncidentFilter({ sensor: Number(event.target.value) || 0 });
    });

    // Typing is debounced. Without it the filter runs a query per keystroke, and a
    // half-typed word is not a filter anybody meant.
    let timer = 0;
    $('#incident-query').addEventListener('input', (event) => {
      const value = event.target.value.trim();
      clearTimeout(timer);
      timer = setTimeout(() => void setIncidentFilter({ query: value }), 250);
    });

    $('#incident-reset').addEventListener('click', () => void resetIncidentFilter());
  }

  chips.innerHTML = INCIDENT_RANGES.map((r) => `
    <button class="chip" type="button" data-range="${r}">${esc(t(`range.${r}`))}</button>`).join('');

  syncIncidentFilter();
}

/**
 * syncIncidentSensorOptions fills the sensor select from the sensors currently
 * known — and only when that list actually changed.
 *
 * Rebuilding the options on every redraw would close the dropdown under somebody
 * who is using it: the list redraws on every keystroke in the text field beside
 * it, and on every event that arrives from the stream.
 */
function syncIncidentSensorOptions() {
  const select = $('#incident-sensor');
  if (!select) return;
  const signature = state.sensors.map((s) => `${s.id}:${s.name}`).join('\u0000');
  if (select.dataset.options === signature) return;
  select.dataset.options = signature;
  select.innerHTML = `<option value="">${esc(t('filter.anySensor'))}</option>` +
    state.sensors.map((s) => `<option value="${s.id}">${esc(s.name)}</option>`).join('');
  select.value = String(state.incidentFilter.sensor || '');
}

/** Whether the filter is hiding anything at all. */
function incidentFilterActive() {
  const f = state.incidentFilter;
  return Boolean(f.status || f.sensor || f.query || (f.range && f.range !== 'all'));
}

/** Mark every control in the filter line with the state the filter is in. */
function syncIncidentFilter() {
  const f = state.incidentFilter;
  for (const button of $$('#incident-status [data-state]')) {
    button.setAttribute('aria-pressed', button.dataset.state === f.status ? 'true' : 'false');
  }
  for (const chip of $$('#incident-range [data-range]')) {
    chip.classList.toggle('active', chip.dataset.range === f.range);
  }
  const select = $('#incident-sensor');
  if (select) select.value = String(f.sensor || '');
  // Only write into the text field when it differs, or the debounce above would
  // move the caret to the end of the word being typed on every redraw.
  const input = $('#incident-query');
  if (input && input.value !== f.query) input.value = f.query;
  const reset = $('#incident-reset');
  if (reset) reset.hidden = !incidentFilterActive();
}

/**
 * setIncidentFilter changes one part of the filter and reloads the list.
 *
 * One function for all four controls rather than one listener each doing its own
 * fetching: the query is built in loadIncidents, and a fifth control added later
 * cannot forget to redraw the others.
 */
async function setIncidentFilter(patch) {
  Object.assign(state.incidentFilter, patch);
  syncIncidentFilter();
  try {
    await loadIncidents();
    renderIncidents();
  } catch (err) {
    toast(err.message, 'error');
  }
}

/** resetIncidentFilter lifts everything, back to the unfiltered list. */
async function resetIncidentFilter() {
  await setIncidentFilter({ status: '', sensor: 0, range: 'all', query: '' });
}

function renderIncidents() {
  const rows = state.incidents || [];
  syncIncidentSensorOptions();
  // An empty list under a filter and an empty list are not the same news, and
  // saying the first one in the words of the second sends somebody looking for a
  // fault in their installation that is really a setting on this page.
  const filtered = incidentFilterActive();
  $('#incidents-empty').hidden = rows.length !== 0;
  $('#incidents-empty-body').hidden = filtered;
  $('#incidents-empty-filtered').hidden = !filtered;

  $('#incident-list').innerHTML = !rows.length ? '' : `<table class="incidents-table">
    <thead><tr>
      <th>${esc(t('field.sensor'))}</th>
      <th>${esc(t('field.started'))}</th>
      <th>${esc(t('field.resolved'))}</th>
      <th class="num">${esc(t('field.duration'))}</th>
      <th>${esc(t('field.message'))}</th>
      <th></th>
    </tr></thead>
    <tbody>${rows.map((inc) => {
      const started = new Date(inc.started_at).getTime();
      const end = inc.resolved_at ? new Date(inc.resolved_at).getTime() : Date.now();
      const ongoing = !inc.resolved_at;
      const ack = ongoing
        ? (inc.acked_at
          ? `<span class="pill st-acked">🔕 ${esc(t('ack.short'))}</span>
             <button class="btn btn-ghost btn-small" data-action="unack" data-incident="${inc.id}">${esc(t('ack.undo'))}</button>`
          : `<button class="btn btn-ghost btn-small" data-action="ack" data-incident="${inc.id}">${esc(t('ack.action'))}</button>`)
        : '';
      return `<tr class="${ongoing ? 'row-crit' : ''}" data-id="${inc.sensor_id}">
        <td><b>${esc(inc.sensor_name || inc.sensor_id)}</b><br><span class="muted">${esc(inc.sensor_target || '')}</span></td>
        <td class="muted">${esc(C.fmtDateTime(inc.started_at))}</td>
        <td>${ongoing ? `<span class="pill st-down"><i class="dot"></i>${esc(t('incidents.ongoing'))}</span>` : `<span class="muted">${esc(C.fmtDateTime(inc.resolved_at))}</span>`}</td>
        <td class="num">${esc(C.fmtDuration(end - started))}</td>
        <td class="muted">${esc(inc.cause || '')}${inc.ack_note ? `<br><span class="ack-note">🔕 ${esc(inc.ack_note)}</span>` : ''}</td>
        <td class="incident-actions">${ack}</td>
      </tr>`;
    }).join('')}</tbody></table>`;

  $('#incident-list').onclick = (event) => {
    const button = event.target.closest('[data-action]');
    if (button) {
      // The buttons live inside a row that opens the detail on click; without
      // this the acknowledgement would also open a dialog behind the request.
      event.stopPropagation();
      const id = Number(button.dataset.incident);
      if (button.dataset.action === 'ack') openAckDialog(id);
      else unack(id);
      return;
    }
    const row = event.target.closest('tr[data-id]');
    if (row) openDetail(Number(row.dataset.id));
  };
}

// ---------------------------------------------------------------------------
// acknowledging an outage
// ---------------------------------------------------------------------------
//
// Acknowledging is not pausing. A paused sensor is not watched at all; an
// acknowledged outage is watched exactly as before, and only the reminder stops.
// The record lives on the incident, so it says which outage was meant and does
// not quietly cover the next one.

function openAckDialog(incidentId) {
  if (!incidentId) return;
  modal.open({
    title: t('ack.title'),
    mode: 'ack',
    id: incidentId,
    saveLabel: t('ack.action'),
    body: `
      <p class="muted">${esc(t('ack.explain'))}</p>
      <label class="field"><span>${withHelp(t('ack.note'), 'help.ack.note')}</span>
        <input name="ack_note" type="text" autocomplete="off"
               placeholder="${esc(t('ack.notePlaceholder'))}"></label>`,
  });
}

async function submitAck(form) {
  const note = form.elements.ack_note.value.trim();
  await api(`/api/incidents/${form.dataset.id}/ack`, { method: 'POST', body: { note } });
  toast(t('ack.done'));
  await afterOutageChange();
}

async function unack(incidentId) {
  if (!incidentId) return;
  try {
    await api(`/api/incidents/${incidentId}/ack`, { method: 'DELETE' });
    toast(t('ack.undone'));
    await afterOutageChange();
  } catch (err) {
    toast(err.message, 'error');
  }
}

/** Refresh whatever view is on screen after an acknowledgement changes. */
async function afterOutageChange() {
  await refresh({ background: true });
  if (state.route === 'incidents') {
    await loadIncidents();
    renderIncidents();
  }
}

// ---------------------------------------------------------------------------
// sensors: create, edit, delete
// ---------------------------------------------------------------------------

const SENSOR_TYPES = [
  {
    value: 'http',
    group: 'web',
    icon: 'builtin:globe',
    labelKey: 'type.http',
    targetLabelKey: 'type.http.target',
    placeholder: 'https://example.org/health',
    fields: [
      { key: 'expected_status', type: 'text', placeholder: '200-299' },
      { key: 'method', type: 'select', options: ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'] },
      { key: 'follow_redirects', type: 'bool' },
      { key: 'body_contains', type: 'text' },
      { key: 'body_not_contains', type: 'text' },
      { key: 'body_regex', type: 'text' },
      // JSON values turn an API into a numeric sensor: a path into the body,
      // optional selection from a list, and an assertion.
      { key: 'json_path', type: 'text' },
      { key: 'json_present', type: 'bool' },
      { key: 'json_match_key', type: 'text' },
      { key: 'json_match_value', type: 'text' },
      { key: 'json_expect', type: 'text' },
      { key: 'json_min', type: 'number' },
      { key: 'json_max', type: 'number' },
      { key: 'expected_headers', type: 'map' },
      { key: 'headers', type: 'map' },
      { key: 'cert_warn_days', type: 'number', placeholder: '14' },
      { key: 'bearer_token', type: 'password' },
      { key: 'basic_auth_user', type: 'text' },
      { key: 'basic_auth_pass', type: 'password', generate: true },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'redirect',
    group: 'web',
    icon: 'builtin:cloud',
    labelKey: 'type.redirect',
    targetLabelKey: 'type.redirect.target',
    placeholder: 'https://example.org',
    fields: [
      { key: 'max_hops', type: 'number', placeholder: '10' },
      { key: 'expected_final', type: 'text', placeholder: 'https://example.org/' },
      { key: 'expected_status', type: 'text', placeholder: '200-299' },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'tcp',
    group: 'network',
    icon: 'builtin:plug',
    labelKey: 'type.tcp',
    targetLabelKey: 'type.tcp.target',
    placeholder: 'db.internal:5432',
    fields: [
      { key: 'tls', type: 'bool' },
      { key: 'server_name', type: 'text', placeholder: 'db.internal' },
      { key: 'cert_warn_days', type: 'number', placeholder: '14' },
      { key: 'send', type: 'text' },
      { key: 'expect', type: 'text' },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'ssh',
    group: 'services',
    icon: 'builtin:laptop',
    labelKey: 'type.ssh',
    targetLabelKey: 'type.ssh.target',
    placeholder: 'nas.local:22',
    fields: [
      { key: 'expect', type: 'text', placeholder: 'OpenSSH' },
    ],
  },
  {
    value: 'smtp',
    group: 'services',
    icon: 'builtin:mail',
    labelKey: 'type.smtp',
    targetLabelKey: 'type.smtp.target',
    placeholder: 'mail.local:25',
    fields: [
      { key: 'tls', type: 'bool' },
      { key: 'server_name', type: 'text', placeholder: 'mail.local' },
      { key: 'expect', type: 'text', placeholder: 'Postfix' },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'imap',
    group: 'services',
    icon: 'builtin:mail',
    labelKey: 'type.imap',
    targetLabelKey: 'type.imap.target',
    placeholder: 'mail.local:993',
    fields: [
      { key: 'tls', type: 'bool' },
      { key: 'server_name', type: 'text', placeholder: 'mail.local' },
      { key: 'expect', type: 'text', placeholder: 'Dovecot' },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'udp',
    group: 'network',
    icon: 'builtin:plug',
    labelKey: 'type.udp',
    targetLabelKey: 'type.udp.target',
    placeholder: '1.1.1.1:53',
    fields: [
      { key: 'payload', type: 'text' },
      { key: 'expect', type: 'text' },
    ],
  },
  {
    value: 'tls',
    group: 'services',
    icon: 'builtin:certificate',
    labelKey: 'type.tls',
    targetLabelKey: 'type.tls.target',
    placeholder: 'example.org:443',
    fields: [
      { key: 'server_name', type: 'text', placeholder: 'example.org' },
      { key: 'cert_warn_days', type: 'number', placeholder: '14' },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'docker',
    group: 'infra',
    icon: 'builtin:container',
    labelKey: 'type.docker',
    targetLabelKey: 'type.docker.target',
    placeholder: 'nginx',
    fields: [
      { key: 'socket', type: 'text', placeholder: '/var/run/docker.sock' },
      { key: 'expect_running', type: 'bool' },
      // A list rather than a text box: the way to switch the check off has to be
      // visible in the form, not something to work out by emptying a field.
      { key: 'expect_health', type: 'select', options: ['healthy', 'starting', 'unhealthy'], emptyLabel: 'opt.expect_healthOff' },
      { key: 'max_restarts', type: 'number', placeholder: '0' },
    ],
  },
  {
    value: 'snmp',
    group: 'infra',
    icon: 'builtin:switch',
    labelKey: 'type.snmp',
    targetLabelKey: 'type.snmp.target',
    placeholder: '192.168.1.2',
    fields: [
      { key: 'oid', type: 'text', placeholder: '1.3.6.1.2.1.1.3.0' },
      { key: 'expect', type: 'text' },
      { key: 'min', type: 'number' },
      { key: 'max', type: 'number' },
      { key: 'version', type: 'select', options: ['2c', '1', '3'], emptyLabel: 'opt.versionDefault' },
      // The v1/v2c community and the v3 user are alternatives; the form shows the
      // one belonging to the chosen version. An empty version chip means 2c, an
      // empty security level means authPriv — the same defaults the prober uses.
      { key: 'community', type: 'password', generate: true,
        showWhen: [{ key: 'version', in: ['2c', '1'], default: '2c' }] },
      { key: 'username', type: 'text', showWhen: [{ key: 'version', in: ['3'] }] },
      { key: 'security_level', type: 'select', options: ['noAuthNoPriv', 'authNoPriv', 'authPriv'],
        emptyLabel: 'opt.securityLevelDefault', showWhen: [{ key: 'version', in: ['3'] }] },
      { key: 'auth_protocol', type: 'select', options: ['SHA-256', 'SHA', 'SHA-224', 'SHA-384', 'SHA-512', 'MD5'],
        showWhen: [{ key: 'version', in: ['3'] },
          { key: 'security_level', in: ['authNoPriv', 'authPriv'], default: 'authPriv' }] },
      { key: 'auth_pass', type: 'password', generate: true,
        showWhen: [{ key: 'version', in: ['3'] },
          { key: 'security_level', in: ['authNoPriv', 'authPriv'], default: 'authPriv' }] },
      { key: 'priv_protocol', type: 'select', options: ['AES', 'AES-256', 'AES-192', 'DES'],
        showWhen: [{ key: 'version', in: ['3'] },
          { key: 'security_level', in: ['authPriv'], default: 'authPriv' }] },
      { key: 'priv_pass', type: 'password', generate: true,
        showWhen: [{ key: 'version', in: ['3'] },
          { key: 'security_level', in: ['authPriv'], default: 'authPriv' }] },
      { key: 'context_name', type: 'text', showWhen: [{ key: 'version', in: ['3'] }] },
    ],
  },
  {
    value: 'ping',
    group: 'network',
    icon: 'builtin:wifi',
    labelKey: 'type.ping',
    targetLabelKey: 'type.ping.target',
    placeholder: '192.168.1.1',
    fields: [{ key: 'count', type: 'number', placeholder: '3' }],
  },
  {
    value: 'hop',
    group: 'network',
    icon: 'builtin:router',
    labelKey: 'type.hop',
    targetLabelKey: 'type.hop.target',
    placeholder: '1.1.1.1',
    fields: [{ key: 'ttl', type: 'number', placeholder: '1' }],
  },
  {
    value: 'dns',
    group: 'network',
    icon: 'builtin:server',
    labelKey: 'type.dns',
    targetLabelKey: 'type.dns.target',
    placeholder: 'example.org',
    fields: [
      { key: 'record_type', type: 'select', options: ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SRV', 'PTR'] },
      { key: 'resolver', type: 'text', placeholder: '1.1.1.1:53' },
      { key: 'expected', type: 'list' },
      { key: 'expect_all', type: 'bool' },
    ],
  },
];

function typeDef(type) {
  return SENSOR_TYPES.find((c) => c.value === type) || SENSOR_TYPES[0];
}

// The order the chooser draws the groups in. The words are spelled out in
// typeGroupLabel rather than assembled from the group id: the catalogue test can
// see a literal translation call and cannot see a template, and a missing key
// would otherwise ship as "type.group.network" on a heading.
const SENSOR_TYPE_GROUPS = ['web', 'network', 'services', 'infra'];

function typeGroupLabel(group) {
  switch (group) {
    case 'network':
      return t('type.group.network');
    case 'services':
      return t('type.group.services');
    case 'infra':
      return t('type.group.infra');
    default:
      return t('type.group.web');
  }
}

/** sensorTypePanelMarkup is the chooser, rendered once into every sensor dialog. */
function sensorTypePanelMarkup(def) {
  const groups = SENSOR_TYPE_GROUPS.map((group) => {
    const tiles = SENSOR_TYPES.filter((c) => c.group === group).map((c) => `
      <button type="button" class="type-tile${c.value === def.value ? ' is-current' : ''}"
              data-sensor-type="${esc(c.value)}" title="${esc(t(c.labelKey))}">
        <span class="type-tile-icon">${iconUI.iconMarkup(c.icon, { size: 18 })}</span>
        <span class="type-tile-text">
          <span class="type-tile-label">${esc(t(c.labelKey))}</span>
          <small class="type-tile-target">${esc(t(c.targetLabelKey))}</small>
        </span>
      </button>`).join('');
    return `<section class="type-group" data-type-group="${esc(group)}">
      <p class="type-group-title">${esc(typeGroupLabel(group))}</p>
      <div class="type-grid">${tiles}</div>
    </section>`;
  }).join('');
  return `
    <div class="type-search">
      <label class="type-search-label" for="sensor-type-search">${esc(t('sensor.typeSearch'))}</label>
      <input type="search" id="sensor-type-search" data-type-query
             placeholder="${esc(t('sensor.typeSearchPlaceholder'))}" autocomplete="off" spellcheck="false">
      <p class="type-search-note" data-type-note hidden></p>
    </div>
    ${groups}`;
}

/**
 * sensorTypeFieldMarkup is the type choice as a field: a button carrying the
 * current type and a pre-rendered panel of grouped tiles.
 *
 * The value travels in a hidden input named like any other field, so the form
 * reads it the way it reads everything else and the existing tests that save a
 * sensor without touching the type keep working.
 *
 * The panel is right-anchored: the field sits in the right-hand column of the
 * name/type grid, and a 560-pixel panel opening to the left would leave the
 * dialog it belongs to.
 */
function sensorTypeFieldMarkup(def) {
  return `
    <div class="field-row">
      <label id="sensor-type-label">${withHelp(t('sensor.type'), 'help.sensor.type')}</label>
      <div class="icon-field type-field" data-type-field>
        <input type="hidden" name="type" id="sensor-type" value="${esc(def.value)}">
        <button type="button" class="icon-button" data-type-open data-catalog-open
                aria-haspopup="dialog" aria-controls="modal-catalog"
                aria-labelledby="sensor-type-label">
          <span class="icon-slot" data-type-icon>${iconUI.iconMarkup(def.icon, { size: 18 })}</span>
          <span class="icon-button-label" data-type-label>${esc(t(def.labelKey))}</span>
          <svg class="icon-caret" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
            <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8"
                  stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
      </div>
    </div>`;
}

/**
 * withHelp renders a field label with its "?" beside it. The explanation and its
 * example both live in the catalogue: the key names the sentence, and the
 * example is stored beside it under the same key plus "Example".
 */
const withHelp = (text, key) => `${esc(text)} ${helpButton(key, `${key}Example`)}`;

/**
 * choiceGroup draws a small set of choices as chips around one native radio
 * group. A radio and not a button, so the keyboard, the form reader and the
 * screen reader all keep working; the chip is only what the choice looks like.
 *
 * The empty choice is a chip like the others on purpose. "Leave it at the
 * default" is a decision somebody makes and has to be able to see, not the
 * absence of one hidden behind a blank field.
 */
function choiceGroup(name, current, choices, label) {
  const known = choices.some((c) => c.value === current);
  const items = choices.map((c) => `
    <label class="chip chip-choice">
      <input type="radio" name="${esc(name)}" value="${esc(c.value)}"${c.value === (known ? current : '') ? ' checked' : ''}>
      <span>${esc(c.label)}</span>
    </label>`).join('');
  return `<fieldset class="field choice-field">
    <legend>${label}</legend>
    <div class="choice-group">${items}</div>
  </fieldset>`;
}

/** Render one option widget. Empty means "leave it at the default". */
function optionField(field, options) {
  const value = options?.[field.key];
  const key = `help.opt.${field.key}`;
  const label = `${esc(t(`opt.${field.key}`))} ${helpButton(key, `${key}Example`)}`;
  const name = `opt_${field.key}`;

  switch (field.type) {
    case 'bool': {
      const current = value === undefined || value === null ? '' : String(value);
      return choiceGroup(name, current, [
        { value: '', label: t('common.optional') },
        { value: 'true', label: t('common.yes') },
        { value: 'false', label: t('common.no') },
      ], label);
    }
    case 'select': {
      const current = value === undefined || value === null ? '' : String(value);
      // The empty choice means "leave it at the default" for most fields, which
      // is why it is worded that way. A field where empty means something else
      // says so through emptyLabel.
      const emptyText = field.emptyLabel ? t(field.emptyLabel) : t('common.optional');
      const choices = [{ value: '', label: emptyText }]
        .concat(field.options.map((o) => ({ value: o, label: o })));
      return choiceGroup(name, current, choices, label);
    }
    case 'map': {
      const lines = value && typeof value === 'object'
        ? Object.entries(value).map(([k, v]) => `${k}: ${v}`).join('\n')
        : '';
      return `<label class="field"><span>${label}</span><textarea name="${name}" rows="3" spellcheck="false">${esc(lines)}</textarea></label>`;
    }
    case 'number':
      return `<label class="field"><span>${label}</span><input name="${name}" type="number" min="0" value="${value === undefined || value === null ? '' : esc(value)}" placeholder="${esc(field.placeholder || '')}"></label>`;
    case 'password': {
      const input = `<input name="${name}" type="password" autocomplete="new-password" value="${value === undefined ? '' : esc(value)}">`;
      const generated = field.generate
        ? `<span class="secret-field">${input}<button type="button" class="btn btn-small"
             data-generate-secret="${esc(name)}">${esc(t('common.generate'))}</button></span>`
        : input;
      return `<label class="field"><span>${label}</span>${generated}</label>`;
    }
    case 'list': {
      const text = Array.isArray(value) ? value.join(', ') : (value || '');
      return `<label class="field"><span>${label}</span><input name="${name}" type="text" value="${esc(text)}" placeholder="${esc(field.placeholder || '')}"></label>`;
    }
    default:
      return `<label class="field"><span>${label}</span><input name="${name}" type="text" value="${value === undefined || value === null ? '' : esc(value)}" placeholder="${esc(field.placeholder || '')}"></label>`;
  }
}

/** Read the option widgets back into the shape the API expects. */
function readOptions(form, def) {
  return readOptionValues(form, def.fields);
}

/**
 * readOptionValues reads chip groups and inputs out of a form or any container.
 *
 * The template window has the SNMP credential fields without a form of its own,
 * so the reader cannot rely on form.elements. A field the chosen version or
 * security level hides is left out: reading it would carry a v2c community into a
 * v3 sensor.
 */
function readOptionValues(scope, fields) {
  const options = {};
  for (const field of fields) {
    const slot = scope.querySelector(`[data-option-key="${field.key}"]`);
    if (slot && slot.hidden) continue;
    const inputs = [...scope.querySelectorAll(`[name="opt_${field.key}"]`)];
    if (!inputs.length) continue;
    // A chip group is several radios under one name. Reading the checked element
    // explicitly keeps the "nothing chosen" case visible rather than relying on a
    // RadioNodeList that a plain container does not provide.
    const raw = inputs.length > 1
      ? (inputs.find((el) => el.checked)?.value ?? '')
      : inputs[0].value;
    if (field.type === 'bool') {
      if (raw === '') continue;
      options[field.key] = raw === 'true';
      continue;
    }
    if (raw === '') continue;
    if (field.type === 'number') {
      const n = Number(raw);
      if (!Number.isNaN(n)) options[field.key] = n;
      continue;
    }
    if (field.type === 'map') {
      const map = {};
      for (const line of raw.split('\n')) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        const at = trimmed.indexOf(':');
        if (at <= 0) continue;
        map[trimmed.slice(0, at).trim()] = trimmed.slice(at + 1).trim();
      }
      if (Object.keys(map).length) options[field.key] = map;
      continue;
    }
    if (field.type === 'list') {
      const list = raw.split(',').map((v) => v.trim()).filter(Boolean);
      if (list.length) options[field.key] = list;
      continue;
    }
    options[field.key] = raw;
  }
  return options;
}

/** siteIcon is the symbol a place is drawn with: its own, or one from its kind. */
function siteIcon(site) {
  if (site?.icon) return site.icon;
  return site?.kind === 'external' ? 'builtin:cloud' : 'builtin:router';
}

/**
 * siteTile is one place in the site chooser. The "no site" entry has no icon and
 * the empty value, which is what the form sends and what the hidden input holds.
 */
function siteTile(site, selected, none = false) {
  const value = none ? '' : site.name;
  const label = none ? t('sites.none') : site.name;
  const sub = none ? '' : (site.kind === 'external' ? t('sites.kind.external') : t('sites.kind.local'));
  return `<button type="button" class="type-tile${value === (selected || '') ? ' is-current' : ''}"
      data-pick-value="${esc(value)}" title="${esc(label)}">
    <span class="type-tile-icon">${none ? '' : iconUI.iconMarkup(siteIcon(site), { size: 18 })}</span>
    <span class="type-tile-text">
      <span class="type-tile-label">${esc(label)}</span>
      ${sub ? `<small class="type-tile-target">${esc(sub)}</small>` : ''}
    </span>
  </button>`;
}

/** sitePanelMarkup is the grouped place chooser, rendered with the form. */
function sitePanelMarkup(selected) {
  const sites = state.sites || [];
  const groups = ['local', 'external'].map((kind) => {
    const list = sites.filter((s) => (s.kind || 'local') === kind);
    if (!list.length) return '';
    return `<section class="type-group" data-pick-group="${esc(kind)}">
      <p class="type-group-title">${esc(kind === 'external' ? t('sites.kind.external') : t('sites.kind.local'))}</p>
      <div class="type-grid">${list.map((s) => siteTile(s, selected)).join('')}</div>
    </section>`;
  }).join('');
  return `
    <div class="type-search">
      <label class="type-search-label" for="sensor-site-search">${esc(t('sensor.siteSearch'))}</label>
      <input type="search" id="sensor-site-search" data-pick-query
             placeholder="${esc(t('sensor.siteSearchPlaceholder'))}" autocomplete="off" spellcheck="false">
      <p class="type-search-note" data-pick-note hidden></p>
    </div>
    <div class="type-grid">${siteTile({}, selected, true)}</div>
    ${groups || `<p class="type-search-note">${esc(t('sites.empty'))}</p>`}
    <div class="type-grid pick-new">
      <button type="button" class="type-tile" data-pick-new-site>
        <span class="type-tile-icon" aria-hidden="true">+</span>
        <span class="type-tile-text"><span class="type-tile-label">${esc(t('sites.newOption'))}</span></span>
      </button>
    </div>`;
}

/**
 * parentPanelMarkup lists the other sensors this one could sit behind, with the
 * status each one has right now.
 *
 * Sorted by state before by name: a down sensor is the one somebody is looking
 * for when they make this choice, and it should be at the top rather than
 * somewhere in the alphabet. The sensor being edited is left out; the server
 * refuses a deeper circle, and offering what would be refused is offering
 * something that does not work.
 */
function parentPanelMarkup(selected, selfName) {
  const rank = { down: 0, warn: 1, unknown: 2, paused: 3, up: 4 };
  const others = [...(state.sensors || [])]
    .filter((c) => c.name !== selfName)
    .sort((a, b) => (rank[a.status] ?? 2) - (rank[b.status] ?? 2) || a.name.localeCompare(b.name));
  const tiles = others.map((c) => `
    <button type="button" class="type-tile pick-sensor${c.name === selected ? ' is-current' : ''}"
            data-pick-value="${esc(c.name)}" title="${esc(c.name)}">
      <span class="type-tile-icon">${iconUI.iconMarkup(c.icon || 'builtin:server', { size: 18 })}</span>
      <span class="type-tile-text">
        <span class="type-tile-label">${esc(c.name)}</span>
        ${c.group ? `<small class="type-tile-target">${esc(c.group)}</small>` : ''}
      </span>
      ${statusPill(c)}
    </button>`).join('');
  return `
    <div class="type-search">
      <label class="type-search-label" for="sensor-parent-search">${esc(t('sensor.parentSearch'))}</label>
      <input type="search" id="sensor-parent-search" data-pick-query
             placeholder="${esc(t('sensor.parentSearchPlaceholder'))}" autocomplete="off" spellcheck="false">
      <p class="type-search-note" data-pick-note hidden></p>
    </div>
    <div class="type-grid pick-grid">
      <button type="button" class="type-tile${selected ? '' : ' is-current'}" data-pick-value=""
              title="${esc(t('sensor.parentNone'))}">
        <span class="type-tile-label">${esc(t('sensor.parentNone'))}</span>
      </button>
    </div>
    <section class="type-group" data-pick-group="others">
      <p class="type-group-title">${esc(t('sensor.parentOthers'))}</p>
      ${tiles
        ? `<div class="type-grid pick-grid">${tiles}</div>`
        : `<p class="type-search-note">${esc(t('sensor.parentEmpty'))}</p>`}
    </section>`;
}

/** groupNames is every group a sensor names, in the dashboard's order. */
function groupNames() {
  // Both sources: the words on the sensors, and the groups somebody has named and
  // arranged before a sensor carries them. A settings-only group is a group.
  const names = [...new Set([
    ...(state.groups || []).map((g) => g.name),
    ...(state.sensors || []).map((s) => s.group).filter(Boolean),
  ])];
  const order = (name) => iconUI.groupOf(state.groups, name)?.sort_order ?? 0;
  return names.sort((a, b) => order(a) - order(b) || a.localeCompare(b));
}

/** groupTile is one existing group, with how many sensors wear it. */
function groupTile(name, selected, count) {
  const settings = iconUI.groupOf(state.groups, name);
  const icon = (settings && iconUI.groupIcon(settings, 18)) ||
    iconUI.iconMarkup('builtin:tag', { size: 18 });
  return `<button type="button" class="type-tile${name === selected ? ' is-current' : ''}"
      data-pick-value="${esc(name)}" title="${esc(name)}">
    <span class="type-tile-icon">${icon}</span>
    <span class="type-tile-text">
      <span class="type-tile-label">${esc(name)}</span>
      <small class="type-tile-target">${esc(t('sensor.groupCount', { count }))}</small>
    </span>
  </button>`;
}

/**
 * groupPanelMarkup is the group chooser. Unlike a place, a group is a word typed
 * on a sensor rather than a row somewhere else, so the search field doubles as
 * the place to name a new one: a term that is not an existing group brings a
 * "New group" tile with it.
 */
function groupPanelMarkup(selected) {
  const counts = new Map();
  for (const s of state.sensors || []) {
    if (s.group) counts.set(s.group, (counts.get(s.group) || 0) + 1);
  }
  const tiles = groupNames().map((n) => groupTile(n, selected, counts.get(n) || 0)).join('');
  return `
    <div class="type-search">
      <label class="type-search-label" for="sensor-group-search">${esc(t('sensor.groupSearch'))}</label>
      <input type="search" id="sensor-group-search" data-pick-query
             placeholder="${esc(t('sensor.groupSearchPlaceholder'))}" autocomplete="off" spellcheck="false">
      <p class="type-search-note" data-pick-note hidden></p>
    </div>
    <div class="type-grid pick-grid">
      <button type="button" class="type-tile${selected ? '' : ' is-current'}" data-pick-value=""
              title="${esc(t('sensor.groupNone'))}">
        <span class="type-tile-label">${esc(t('sensor.groupNone'))}</span>
      </button>
    </div>
    <section class="type-group" data-pick-group="groups">
      <p class="type-group-title">${esc(t('sensor.groupHeading'))}</p>
      ${tiles
        ? `<div class="type-grid">${tiles}</div>`
        : `<p class="type-search-note">${esc(t('sensor.groupEmpty'))}</p>`}
    </section>
    <div class="type-grid pick-new">
      <button type="button" class="type-tile" data-pick-new-group>
        <span class="type-tile-icon" aria-hidden="true">+</span>
        <span class="type-tile-text"><span class="type-tile-label">${esc(t('groups.newOption'))}</span></span>
      </button>
    </div>`;
}

/** groupFieldMarkup is the group picker as a field, in the type chooser's style. */
function groupFieldMarkup(selected) {
  const settings = iconUI.groupOf(state.groups, selected);
  const icon = selected
    ? ((settings && iconUI.groupIcon(settings, 18)) || iconUI.iconMarkup('builtin:tag', { size: 18 }))
    : '';
  return `
    <div class="field-row">
      <label id="sensor-group-label">${withHelp(t('sensor.group'), 'help.sensor.group')}</label>
      <div class="icon-field type-field pick-field" data-pick-field="group">
        <input type="hidden" name="group" id="sensor-group" value="${esc(selected || '')}">
        <button type="button" class="icon-button" data-pick-open
                aria-haspopup="true" aria-expanded="false" aria-controls="sensor-group-panel"
                aria-labelledby="sensor-group-label sensor-group-value">
          <span class="icon-slot" data-pick-icon>${icon}</span>
          <span class="icon-button-label" id="sensor-group-value" data-pick-label>${esc(selected || t('sensor.groupNone'))}</span>
          <svg class="icon-caret" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
            <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8"
                  stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <div class="type-panel pick-panel" id="sensor-group-panel" hidden>${groupPanelMarkup(selected)}</div>
      </div>
    </div>`;
}

function tagSelectedNames(field) {
  return (pickValueOf(field) || '').split(',').map((v) => v.trim()).filter(Boolean);
}

/** setTagValue writes a multi-selection into the tag field and its panel. */
function setTagValue(field, names) {
  if (!field) return;
  const unique = [...new Set(names.map((n) => n.trim()).filter(Boolean))];
  const input = field.querySelector('input[type="hidden"]');
  if (input) input.value = unique.join(',');
  const label = field.querySelector('[data-pick-label]');
  if (label) label.textContent = unique.length ? unique.join(', ') : t('tags.none');
  const icon = field.querySelector('[data-pick-icon]');
  if (icon) icon.innerHTML = unique.length ? iconUI.iconMarkup('builtin:tag', { size: 18 }) : '';
  for (const tile of field.querySelectorAll('[data-pick-value]')) {
    const on = unique.includes(tile.dataset.pickValue);
    tile.classList.toggle('is-current', on);
    const box = tile.querySelector('[data-tag-check]');
    if (box) box.textContent = on ? '✓' : '';
  }
}

/** tagTile is one tag in the multi-select, with a checkmark rather than a single choice. */
function tagTile(name, selected, count) {
  const settings = iconUI.groupOf(state.tags, name);
  const icon = (settings && iconUI.groupIcon(settings, 18)) ||
    iconUI.iconMarkup('builtin:tag', { size: 18 });
  return `<button type="button" class="type-tile pick-sensor tag-tile${selected.includes(name) ? ' is-current' : ''}"
      data-pick-value="${esc(name)}" title="${esc(name)}">
    <span class="tag-check" data-tag-check aria-hidden="true">${selected.includes(name) ? '✓' : ''}</span>
    <span class="type-tile-icon">${icon}</span>
    <span class="type-tile-text">
      <span class="type-tile-label">${esc(name)}</span>
      <small class="type-tile-target">${esc(t('tags.count', { count }))}</small>
    </span>
  </button>`;
}

/** tagPanelMarkup is the multi-select: existing tags, a clear entry, and "New tag…". */
function tagPanelMarkup(selected) {
  const counts = new Map();
  for (const s of state.sensors || []) {
    for (const name of s.tags || []) counts.set(name, (counts.get(name) || 0) + 1);
  }
  const names = [...new Set([...(state.tags || []).map((t) => t.name), ...counts.keys()])];
  names.sort((a, b) => {
    const ga = iconUI.groupOf(state.tags, a);
    const gb = iconUI.groupOf(state.tags, b);
    return (ga?.sort_order ?? 0) - (gb?.sort_order ?? 0) || a.localeCompare(b);
  });
  const tiles = names.map((name) => tagTile(name, selected, counts.get(name) || 0)).join('');
  return `
    <div class="type-search">
      <label class="type-search-label" for="sensor-tags-search">${esc(t('tags.search'))}</label>
      <input type="search" id="sensor-tags-search" data-pick-query
             placeholder="${esc(t('tags.searchPlaceholder'))}" autocomplete="off" spellcheck="false">
      <p class="type-search-note" data-pick-note hidden></p>
    </div>
    <div class="type-grid pick-grid">
      <button type="button" class="type-tile${selected.length ? '' : ' is-current'}" data-pick-clear
              title="${esc(t('tags.none'))}">
        <span class="type-tile-label">${esc(t('tags.none'))}</span>
      </button>
    </div>
    <section class="type-group" data-pick-group="tags">
      <p class="type-group-title">${esc(t('tags.heading'))}</p>
      ${tiles
        ? `<div class="type-grid pick-grid">${tiles}</div>`
        : `<p class="type-search-note">${esc(t('tags.emptyPicker'))}</p>`}
    </section>
    <div class="type-grid pick-new">
      <button type="button" class="type-tile" data-pick-new-tag>
        <span class="type-tile-icon" aria-hidden="true">+</span>
        <span class="type-tile-text"><span class="type-tile-label">${esc(t('tags.newOption'))}</span></span>
      </button>
    </div>`;
}

/** tagFieldMarkup is the tag multi-select as a field, in the type chooser's style. */
function tagFieldMarkup(selected) {
  const names = selected || [];
  return `
    <div class="field-row">
      <label id="sensor-tags-label">${withHelp(t('sensor.tags'), 'help.sensor.tags')}</label>
      <div class="icon-field type-field pick-field" data-pick-field="tags" data-pick-multi="true">
        <input type="hidden" name="tags" id="sensor-tags" value="${esc(names.join(','))}">
        <button type="button" class="icon-button" data-pick-open
                aria-haspopup="true" aria-expanded="false" aria-controls="sensor-tags-panel"
                aria-labelledby="sensor-tags-label sensor-tags-value">
          <span class="icon-slot" data-pick-icon>${names.length ? iconUI.iconMarkup('builtin:tag', { size: 18 }) : ''}</span>
          <span class="icon-button-label" id="sensor-tags-value" data-pick-label>${esc(names.length ? names.join(', ') : t('tags.none'))}</span>
          <svg class="icon-caret" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
            <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8"
                  stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <div class="type-panel pick-panel" id="sensor-tags-panel" hidden>${tagPanelMarkup(names)}</div>
      </div>
    </div>`;
}

/** siteFieldMarkup is the place picker as a field, in the type chooser's style. */
function siteFieldMarkup(selected) {
  const site = (state.sites || []).find((s) => s.name === selected);
  return `
    <div class="field-row">
      <label id="sensor-site-label">${withHelp(t('sensor.site'), 'help.sensor.site')}</label>
      <div class="icon-field type-field pick-field" data-pick-field="site">
        <input type="hidden" name="site" id="sensor-site" value="${esc(selected || '')}">
        <button type="button" class="icon-button" data-pick-open
                aria-haspopup="true" aria-expanded="false" aria-controls="sensor-site-panel"
                aria-labelledby="sensor-site-label sensor-site-value">
          <span class="icon-slot" data-pick-icon>${selected ? iconUI.iconMarkup(siteIcon(site), { size: 18 }) : ''}</span>
          <span class="icon-button-label" id="sensor-site-value" data-pick-label>${esc(selected || t('sites.none'))}</span>
          <svg class="icon-caret" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
            <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8"
                  stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <div class="type-panel pick-panel" id="sensor-site-panel" hidden>${sitePanelMarkup(selected)}</div>
      </div>
    </div>`;
}

/** parentFieldMarkup is the dependency picker: a sensor, with its state. */
function parentFieldMarkup(selected, selfName) {
  const sensor = (state.sensors || []).find((c) => c.name === selected);
  return `
    <div class="field-row">
      <label id="sensor-parent-label">${withHelp(t('sensor.parent'), 'help.sensor.parent')}</label>
      <div class="icon-field type-field pick-field" data-pick-field="parent">
        <input type="hidden" name="parent" id="sensor-parent" value="${esc(selected || '')}">
        <button type="button" class="icon-button" data-pick-open
                aria-haspopup="true" aria-expanded="false" aria-controls="sensor-parent-panel"
                aria-labelledby="sensor-parent-label sensor-parent-value sensor-parent-status">
          <span class="icon-slot" data-pick-icon>${selected ? iconUI.iconMarkup(sensor?.icon || 'builtin:server', { size: 18 }) : ''}</span>
          <span class="icon-button-label" id="sensor-parent-value" data-pick-label>${esc(selected || t('sensor.parentNone'))}</span>
          <span class="pick-status" id="sensor-parent-status" data-pick-status>${selected ? statusPill(sensor) : ''}</span>
          <svg class="icon-caret" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
            <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8"
                  stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
        <div class="type-panel pick-panel" id="sensor-parent-panel" hidden>${parentPanelMarkup(selected, selfName)}</div>
      </div>
    </div>`;
}

// --- the sensor type chooser -------------------------------------------------

/** openTypePanel shows one chooser, closes any other and focuses the search. */
function openTypePanel(field) {
  const panel = field.querySelector('.type-panel');
  if (!panel || !panel.hidden) return;
  closeTypePanels(field);
  closePickPanels(null);
  const query = panel.querySelector('[data-type-query]');
  if (query) {
    query.value = '';
    filterTypeTiles(field, '');
  }
  panel.hidden = false;
  field.querySelector('[data-type-open]')?.setAttribute('aria-expanded', 'true');
  // Focus first: focusing the search can scroll the dialog, and the panel has to
  // be placed against where the button ended up, not where it was before.
  query?.focus();
  placePanel(field, panel);
}

/** closeTypePanel hides one chooser, optionally putting focus back on its button. */
function closeTypePanel(field, { restoreFocus = false } = {}) {
  const panel = field?.querySelector('.type-panel');
  if (!panel || panel.hidden) return;
  panel.hidden = true;
  clearPanelPlacement(panel);
  const button = field.querySelector('[data-type-open]');
  button?.setAttribute('aria-expanded', 'false');
  if (restoreFocus) button?.focus();
}

/** closeTypePanels closes every chooser but one, since only one may be open. */
function closeTypePanels(except) {
  for (const field of document.querySelectorAll('[data-type-field]')) {
    if (field !== except) closeTypePanel(field);
  }
}

/** filterTypeTiles narrows the tiles to those matching the search box. */
function filterTypeTiles(field, term) {
  // The old chooser wrapped the tiles in .type-panel; the catalogue draws them
  // directly in its own container, so the wrapper is either there or the field
  // itself.
  const panel = field.querySelector('.type-panel') || field;
  if (!panel) return;
  const needle = term.trim().toLowerCase();
  let visible = 0;
  for (const tile of panel.querySelectorAll('[data-sensor-type]')) {
    const haystack = `${tile.dataset.sensorType} ${tile.textContent}`.toLowerCase();
    const show = !needle || haystack.includes(needle);
    tile.hidden = !show;
    if (show) visible++;
  }
  for (const group of panel.querySelectorAll('[data-type-group]')) {
    group.hidden = ![...group.querySelectorAll('[data-sensor-type]')].some((tile) => !tile.hidden);
  }
  const note = panel.querySelector('[data-type-note]');
  if (note) {
    note.hidden = visible !== 0;
    note.textContent = visible === 0 ? t('sensor.typeNoMatch', { query: term.trim() }) : '';
  }
}

/**
 * applySensorType moves the form to another type: the hidden value, the button,
 * the target label and placeholder, the option fields and the fold's summary.
 *
 * The option values are read from the type that is leaving before the fields
 * are rebuilt, so keys two types share (server_name, expect, cert_warn_days)
 * survive a switch the way they did when this was a select's change handler.
 */
function applySensorType(form, value) {
  const next = typeDef(value);
  const previous = typeDef(form.dataset.lastType || next.value);
  const values = readOptions(form, previous);
  form.dataset.lastType = next.value;

  const input = $('#sensor-type');
  if (input) input.value = next.value;

  const field = form.querySelector('[data-type-field]');
  if (field) {
    const icon = field.querySelector('[data-type-icon]');
    if (icon) icon.innerHTML = iconUI.iconMarkup(next.icon, { size: 18 });
    const label = field.querySelector('[data-type-label]');
    if (label) label.textContent = t(next.labelKey);
    for (const tile of field.querySelectorAll('[data-sensor-type]')) {
      tile.classList.toggle('is-current', tile.dataset.sensorType === next.value);
    }
  }

  const targetLabel = $('#target-label');
  if (targetLabel) targetLabel.innerHTML = withHelp(t(next.targetLabelKey), 'help.sensor.target');
  const target = form.elements.target;
  if (target) target.placeholder = next.placeholder;

  const fields = $('#option-fields');
  if (fields) {
    fields.innerHTML = next.fields.map((f) => `
      <div class="option-slot" data-option-key="${esc(f.key)}">${optionField(f, values)}</div>`).join('');
    syncConditionalOptions(next.fields);
  }
  const summary = $('#option-summary');
  if (summary) summary.textContent = t(next.labelKey);
}

/**
 * optionChoice is the value a chip group currently carries. An empty choice
 * means the prober's default, which is why each condition says what its own
 * empty value stands for.
 */
function optionChoice(container, key, fallback) {
  const checked = container.querySelector(`input[name="opt_${key}"]:checked`);
  return checked && checked.value !== '' ? checked.value : fallback;
}

/**
 * syncConditionalOptions hides the fields the chosen version and security level
 * do not use. Without it a v3 sensor asks for a community string it will ignore,
 * and a v2c sensor asks for a username that means nothing.
 */
function syncConditionalOptions(fields, container = $('#option-fields')) {
  if (!container) return;
  for (const field of fields) {
    if (!field.showWhen) continue;
    const slot = container.querySelector(`[data-option-key="${field.key}"]`);
    if (!slot) continue;
    slot.hidden = !field.showWhen.every((cond) =>
      cond.in.includes(optionChoice(container, cond.key, cond.default || '')));
  }
}

/** pickTypeTile applies one tile and puts the chooser away. */
function pickTypeTile(field, tile, form) {
  if (!form) return;
  applySensorType(form, tile.dataset.sensorType);
  closeTypePanel(field);
  // Focus moves in a task of its own. Enter chooses a tile, and the same key
  // stroke is still being resolved when this handler returns: a button focused
  // here would be activated by that default action and reopen what was just
  // closed. One task later the key is done and the button is safe to focus.
  const button = field?.querySelector('[data-type-open]');
  if (button) setTimeout(() => button.focus(), 0);
}

/**
 * mountSensorTypePicker wires the choosers once, on the document. The sensor
 * dialog is rebuilt on every open, so listeners attached to it would have to be
 * removed with it; the icon picker solved this the same way.
 */
function mountSensorTypePicker() {
  document.addEventListener('click', (event) => {
    const open = event.target.closest('[data-type-open]');
    if (open) {
      if (open.hasAttribute('data-catalog-open')) {
        openSensorCatalog(open.closest('form'));
        return;
      }
      const field = open.closest('[data-type-field]');
      const panel = field?.querySelector('.type-panel');
      if (panel && !panel.hidden) closeTypePanel(field, { restoreFocus: true });
      else if (field) openTypePanel(field);
      return;
    }

    const tile = event.target.closest('[data-sensor-type]');
    if (tile) {
      pickTypeTile(tile.closest('[data-type-field]'), tile, tile.closest('form'));
      return;
    }

    // A click anywhere else closes the chooser, which is what makes it behave
    // like a menu rather than like part of the form.
    if (!event.target.closest('.type-panel')) closeTypePanels(null);
  });

  document.addEventListener('input', (event) => {
    const query = event.target.closest('[data-type-query]');
    if (!query) return;
    const field = query.closest('[data-type-field]');
    if (field) filterTypeTiles(field, query.value);
  });

  // Version and security level decide which credential fields the SNMP form
  // shows. The listener is on the document for the same reason as the others:
  // the option fields are rebuilt with every type switch.
  document.addEventListener('change', (event) => {
    if (!event.target.closest('#option-fields')) return;
    const form = event.target.closest('form');
    if (form?.elements.type) syncConditionalOptions(typeDef(form.elements.type.value).fields);
  });

  // Enter in the search picks the first visible tile. It must not reach the
  // form: #modal-form is method="dialog", so an unhandled Enter saves and closes.
  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Enter') return;
    const query = event.target.closest('[data-type-query]');
    if (!query) return;
    event.preventDefault();
    const field = query.closest('[data-type-field]');
    const first = field?.querySelector('[data-sensor-type]:not([hidden])');
    if (!first) return;
    if (field.closest('#modal-catalog')) {
      const type = first.dataset.sensorType;
      if (!catalogForm) {
        closeCatalog();
        const options = type === 'snmp'
          ? { ...catalogSNMP }
          : (type === 'http' && catalogToken ? { bearer_token: catalogToken } : {});
        openSensorEditor(null, type, { ...catalogDevice, options });
        return;
      }
      const form = catalogForm;
      applySensorType(form, type);
      // Closing a modal dialog hands focus back to the button that opened it.
      // If that happens while Enter is still being resolved, the same key
      // activates the button and opens the catalogue again. Wait for the keyup,
      // then close; the timeout is for a key that never reports one.
      let done = false;
      const button = form.querySelector('[data-type-open]');
      const finish = () => {
        if (done) return;
        done = true;
        document.removeEventListener('keyup', finish, true);
        clearTimeout(timer);
        closeCatalog();
        setTimeout(() => button?.focus(), 0);
      };
      const timer = setTimeout(finish, 500);
      document.addEventListener('keyup', finish, true);
      return;
    }
    pickTypeTile(field, first, field.closest('form'));
  });

  // Escape closes the chooser and not the dialog. The dialog's own cancel
  // listener has the same guard, because some browsers request the cancel
  // before the keydown default is decided.
  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape') return;
    const panel = document.querySelector('.type-panel:not([hidden])');
    if (!panel) return;
    event.preventDefault();
    closeTypePanel(panel.closest('[data-type-field]'), { restoreFocus: true });
  });
}

// --- the sensor catalogue --------------------------------------------------
//
// The type field opens a window rather than a corner panel, because the answer it
// carries is larger than a list of types: alongside the individual sensors there
// are device templates, and a template needs room to show what it will add before
// it adds it.
//
// The individual sensors are the same tiles the old panel drew, from the same
// SENSOR_TYPES list, so there is one catalogue and not two. Picking one applies it
// to the sensor form behind the window and closes the window; the template half
// never touches the sensor form at all — it creates its sensors directly and the
// dashboard refreshes behind it.

let catalogForm = null;
let catalogTemplates = null;
let catalogDetail = null;
let catalogChecks = [];
let catalogCredentialFields = [];
let catalogDevice = { name: '', target: '', group: '' };
let catalogSNMP = {};
let catalogToken = '';

// The SNMP fields a template needs, in the order the sensor form draws them.
const CATALOG_CREDENTIAL_KEYS = new Set([
  'version', 'community', 'username', 'security_level', 'auth_protocol',
  'auth_pass', 'priv_protocol', 'priv_pass', 'context_name',
]);

/** openSensorCatalog draws the catalogue for the sensor form behind it. */
async function openSensorCatalog(form) {
  catalogForm = form;
  const dialog = $('#modal-catalog');
  catalogDetail = null;
  catalogFilter.query = '';
  catalogFilter.vendor = '';
  catalogFilter.system = '';
  catalogFilter.tags.clear();
  catalogFilterOpen = false;
  if (form === null) {
    catalogDevice = { name: '', target: '', group: '' };
    catalogSNMP = {};
    catalogToken = '';
  }
  if (!catalogTemplates) {
    try {
      const data = await api('/api/sensor-templates');
      catalogTemplates = data.templates || [];
    } catch (err) {
      catalogTemplates = [];
      toast(err.message, 'error');
    }
  }
  applyTranslations(dialog);
  setCatalogTab('templates');
  if (!dialog.open) dialog.showModal();
  setTimeout(() => dialog.querySelector('[data-type-query]')?.focus(), 0);
}

/** closeCatalog puts the window away and forgets which form it belonged to. */
function closeCatalog() {
  const dialog = $('#modal-catalog');
  if (dialog?.open) dialog.close();
  catalogForm = null;
  catalogDetail = null;
}

/** setCatalogTab switches between the individual sensors and the templates. */
function setCatalogTab(tab) {
  catalogDetail = null;
  catalogChecks = [];
  const dialog = $('#modal-catalog');
  for (const button of dialog.querySelectorAll('[data-catalog-tab]')) {
    button.setAttribute('aria-selected', String(button.dataset.catalogTab === tab));
  }
  $('#catalog-body').innerHTML = tab === 'templates' ? catalogTemplatesMarkup() : catalogSensorsMarkup();
  if (tab === 'templates') renderTemplateGrid();
  const add = $('#catalog-add');
  add.hidden = true;
  add.disabled = true;
  $('#catalog-error').textContent = '';
}

/** catalogSensorsMarkup is the old type panel, in the catalog window. */
function catalogSensorsMarkup() {
  const def = typeDef(catalogForm?.elements.type?.value || 'http');
  return `<div data-type-field data-catalog-sensors>${sensorTypePanelMarkup(def)}</div>`;
}

/** catalogTemplatesMarkup draws one card per device template. */
const catalogFilter = { query: '', vendor: '', system: '', tags: new Set() };
let catalogFilterOpen = false;

function catalogTemplatesMarkup() {
  if (!catalogTemplates?.length) return `<p class="muted">${esc(t('catalog.templateHint'))}</p>`;
  return `
    <p class="panel-hint">${esc(t('catalog.templateHint'))}</p>
    ${catalogDeviceSummary()}
    ${catalogFiltersMarkup()}
    <div class="template-grid" id="template-grid"></div>`;
}

/** catalogDeviceSummary shows the device the next template will use. */
function catalogDeviceSummary() {
  if (catalogForm !== null || !catalogDevice.target) return '';
  return `<p class="catalog-device-summary">
    ${esc(t('catalog.device'))}: <b>${esc(catalogDevice.name || catalogDevice.target)}</b>
    · ${esc(catalogDevice.target)}
    <button type="button" class="btn btn-small" data-device-reset>${esc(t('catalog.changeDevice'))}</button>
  </p>`;
}

/** catalogFiltersMarkup is a search box, one filter button and the active chips. */
function catalogFiltersMarkup() {
  const vendors = [...new Set(catalogTemplates.map((t) => t.vendor).filter(Boolean))].sort();
  const systems = [...new Set(catalogTemplates.map((t) => t.system).filter(Boolean))].sort();
  const tags = [...new Set(catalogTemplates.flatMap((t) => t.tags || []))].sort();
  const chip = (kind, value, active, label) => `
    <button type="button" class="chip${active ? ' active' : ''}"
            data-filter-${kind}="${esc(value)}">${esc(label)}</button>`;
  const row = (label, chips) => `<div class="catalog-filter-row">
    <span>${esc(label)}</span>${chips}</div>`;
  return `<div class="catalog-filters">
    <div class="catalog-filter-bar">
      <input type="search" data-template-query value="${esc(catalogFilter.query)}"
             placeholder="${esc(t('catalog.searchPlaceholder'))}" autocomplete="off">
      <button type="button" class="btn" data-filter-toggle aria-expanded="false">
        ${esc(t('catalog.filters'))} <span class="filter-count" data-filter-count></span>
      </button>
    </div>
    <div class="catalog-filter-active" id="catalog-filter-active"></div>
    <div class="catalog-filter-panel" id="catalog-filter-panel" hidden>
      ${row(t('catalog.filterVendor'), vendors.map((v) =>
        chip('vendor', v, catalogFilter.vendor === v, v)).join(''))}
      ${row(t('catalog.filterSystem'), systems.map((v) =>
        chip('system', v, catalogFilter.system === v, t(`catalog.system.${v}`))).join(''))}
      ${row(t('catalog.filterTag'), tags.map((v) =>
        chip('tag', v, catalogFilter.tags.has(v), t(`catalog.tag.${v}`))).join(''))}
    </div>
  </div>`;
}

/** syncCatalogFilterUI draws the count and the active chips, and the panel state. */
function syncCatalogFilterUI() {
  const count = (catalogFilter.vendor ? 1 : 0) + (catalogFilter.system ? 1 : 0) + catalogFilter.tags.size;
  const badge = $('[data-filter-count]');
  if (badge) badge.textContent = count ? String(count) : '';
  const active = $('#catalog-filter-active');
  if (active) {
    const chips = [];
    if (catalogFilter.vendor) {
      chips.push(`<button type="button" class="chip active" data-clear-vendor>${esc(catalogFilter.vendor)} ×</button>`);
    }
    if (catalogFilter.system) {
      chips.push(`<button type="button" class="chip active" data-clear-system>${esc(t(`catalog.system.${catalogFilter.system}`))} ×</button>`);
    }
    for (const tag of catalogFilter.tags) {
      chips.push(`<button type="button" class="chip active" data-clear-tag="${esc(tag)}">${esc(t(`catalog.tag.${tag}`))} ×</button>`);
    }
    if (chips.length) {
      chips.push(`<button type="button" class="btn btn-small" data-clear-all>${esc(t('catalog.clearFilters'))}</button>`);
    }
    active.innerHTML = chips.join('');
  }
  const panel = $('#catalog-filter-panel');
  if (panel) panel.hidden = !catalogFilterOpen;
  const toggle = $('[data-filter-toggle]');
  if (toggle) toggle.setAttribute('aria-expanded', String(catalogFilterOpen));
  for (const chip of $$('#modal-catalog [data-filter-vendor]')) {
    chip.classList.toggle('active', chip.dataset.filterVendor === catalogFilter.vendor);
  }
  for (const chip of $$('#modal-catalog [data-filter-system]')) {
    chip.classList.toggle('active', chip.dataset.filterSystem === catalogFilter.system);
  }
  for (const chip of $$('#modal-catalog [data-filter-tag]')) {
    chip.classList.toggle('active', catalogFilter.tags.has(chip.dataset.filterTag));
  }
}

/** filteredTemplates applies the search box and the three chips. */
function filteredTemplates() {
  const query = catalogFilter.query.trim().toLowerCase();
  return (catalogTemplates || []).filter((template) => {
    if (catalogFilter.vendor && template.vendor !== catalogFilter.vendor) return false;
    if (catalogFilter.system && template.system !== catalogFilter.system) return false;
    if (![...catalogFilter.tags].every((tag) => (template.tags || []).includes(tag))) return false;
    if (!query) return true;
    const haystack = [
      t(template.name_key), t(template.description_key), template.vendor, template.product,
      template.system ? t(`catalog.system.${template.system}`) : '',
      ...(template.tags || []).map((tag) => t(`catalog.tag.${tag}`)),
      ...template.checks.map((check) => t(check.label_key)),
    ].join(' ').toLowerCase();
    return haystack.includes(query);
  });
}

/** templateCardMarkup is one device template in the grid. */
function templateCardMarkup(template) {
  const sub = [
    template.vendor, template.product,
    template.system ? t(`catalog.system.${template.system}`) : '',
  ].filter(Boolean).join(' · ');
  return `
    <button type="button" class="template-card" data-template-id="${esc(template.id)}">
      <span class="template-icon">${iconUI.iconMarkup(template.icon, { size: 24 })}</span>
      <span class="template-text">
        <strong>${esc(t(template.name_key))}</strong>
        <small>${esc(sub)}</small>
        <span class="template-description">${esc(t(template.description_key))}</span>
      </span>
      <span class="pill">${esc(t('catalog.checks', { count: template.checks.length }))}</span>
    </button>`;
}

/** renderTemplateGrid redraws the cards after a filter changes. */
function renderTemplateGrid() {
  const grid = $('#template-grid');
  if (!grid) return;
  const list = filteredTemplates();
  grid.innerHTML = list.length
    ? list.map(templateCardMarkup).join('')
    : `<p class="muted">${esc(t('catalog.noTemplates'))}</p>`;
  syncCatalogFilterUI();
}

/**
 * templateTarget expands a check's target pattern. {address} is what the person
 * typed; {host} is its host without a scheme or port, which is what ping and
 * TCP want when the address is a URL.
 */
function templateTarget(pattern, address) {
  if (!pattern) return address;
  const clean = String(address).trim()
    .replace(/^https?:\/\//, '')
    .replace(/\/.*$/, '');
  return pattern
    .replaceAll('{address}', clean)
    .replaceAll('{host}', hostOfAddress(address));
}

function hostOfAddress(address) {
  try {
    const parsed = new URL(address.includes('://') ? address : `//${address}`);
    if (parsed.hostname) return parsed.hostname;
  } catch {
    // Fall through to the string trim below.
  }
  return String(address).replace(/^https?:\/\//, '').split('/')[0].replace(/:\d+$/, '');
}

/** catalogCheckMarkup is one selectable sensor in the template's list. */
function catalogCheckMarkup(check) {
  const label = check.label || t(check.label_key);
  return `<label class="catalog-check">
    <input type="checkbox" data-catalog-check="${esc(check.key)}"${check.default_off ? '' : ' checked'}>
    <span>${esc(label)}</span>
    <small class="muted">${esc(t(`type.${check.type}`))}</small>
  </label>`;
}

/** renderCatalogChecks redraws the static checks plus anything discovered. */
function renderCatalogChecks() {
  const box = $('#catalog-checks');
  if (!box) return;
  box.innerHTML = `<legend id="catalog-checks-legend">${esc(t('catalog.checks', { count: catalogChecks.length }))}</legend>` +
    catalogChecks.map(catalogCheckMarkup).join('');
  updateCatalogAdd();
}

/** openTemplateDetail shows the checks a template would add. */
function openTemplateDetail(template) {
  catalogDetail = template;
  catalogCredentialFields = typeDef('snmp').fields.filter((f) => CATALOG_CREDENTIAL_KEYS.has(f.key));
  const standalone = catalogForm === null;
  const name = (standalone && catalogDevice.name) || t(template.name_key);
  const deviceTarget = standalone ? catalogDevice.target : '';
  const deviceGroup = standalone ? (catalogDevice.group || catalogDevice.name || name) : name;
  const meta = [
    template.system ? t(`catalog.system.${template.system}`) : '',
    ...(template.tags || []).map((tag) => t(`catalog.tag.${tag}`)),
  ].filter(Boolean).join(' · ');
  const source = template.source
    ? `<p class="muted small">${esc(t('catalog.source'))}: <a href="${esc(template.source)}"
         target="_blank" rel="noopener noreferrer">${esc(template.source)}</a></p>`
    : '';
  const hasSNMP = template.checks.some((check) => check.type === 'snmp');
  catalogChecks = template.checks.slice();
  const fields = catalogCredentialFields.map((f) => `
    <div class="option-slot" data-option-key="${esc(f.key)}">${optionField(f, { version: '2c', ...catalogSNMP })}</div>`).join('');
  const credential = template.credential ? `
    <section class="catalog-credentials">
      <h3>${esc(t('catalog.credentials'))}</h3>
      <label class="field"><span>${esc(t(template.credential.label_key))}
        ${template.credential.help_key ? helpButton(template.credential.help_key) : ''}</span>
        <input id="catalog-credential" type="${template.credential.secret ? 'password' : 'text'}"
               value="${esc(catalogToken)}" autocomplete="off" spellcheck="false"></label>
    </section>` : '';
  const snmp = hasSNMP ? `
    <section class="catalog-credentials">
      <h3>${esc(t('catalog.credentials'))}</h3>
      <div id="catalog-options">${fields}</div>
    </section>` : '';
  $('#catalog-body').innerHTML = `
    <button type="button" class="btn btn-small catalog-back" data-catalog-back>← ${esc(t('catalog.back'))}</button>
    <div class="template-detail-head">
      <span class="template-icon">${iconUI.iconMarkup(template.icon, { size: 26 })}</span>
      <div>
        <h3>${esc(name)}</h3>
        <p class="muted">${esc(t(template.description_key))}</p>
        <p class="muted small">${esc(meta)}</p>
        ${source}
      </div>
    </div>
    <p class="muted small">${esc(t('catalog.templateNote'))}</p>
    <div class="grid-2">
      <label class="field"><span>${esc(t('catalog.deviceName'))}</span>
        <input id="catalog-name" type="text" value="${esc(name)}" autocomplete="off"></label>
      <label class="field"><span>${esc(t('catalog.address'))}</span>
        <input id="catalog-target" type="text" value="${esc(deviceTarget)}" placeholder="192.168.1.2" autocomplete="off" spellcheck="false"></label>
    </div>
    <label class="field"><span>${esc(t('catalog.group'))}</span>
      <input id="catalog-group" type="text" value="${esc(deviceGroup)}" autocomplete="off"></label>
    <p class="muted small">${esc(t('catalog.groupHint'))}</p>
    <fieldset class="field catalog-checks" id="catalog-checks">
      <legend id="catalog-checks-legend">${esc(t('catalog.checks', { count: catalogChecks.length }))}</legend>
    </fieldset>
    <p class="muted small" id="catalog-discovery-notes"></p>
    ${credential}
    ${snmp}
    <section class="catalog-device-check">
      <button type="button" class="btn" id="catalog-check-device">${esc(t('catalog.checkDevice'))}</button>
      <p class="muted small" id="catalog-device-status">${esc(t('catalog.deviceHint'))}</p>
    </section>`;
  if (hasSNMP) syncConditionalOptions(catalogCredentialFields, $('#catalog-options'));
  renderCatalogChecks();
  updateCatalogAdd();
  $('#catalog-add').hidden = false;
  $('#catalog-error').textContent = '';
  $('#catalog-name')?.focus();
}

/** checkTemplateDevice makes one real SNMP request with the form's credentials. */
async function checkTemplateDevice() {
  if (!catalogDetail) return;
  const target = $('#catalog-target').value.trim();
  if (!target) {
    $('#catalog-error').textContent = t('catalog.missing', { field: t('catalog.address') });
    return;
  }
  const button = $('#catalog-check-device');
  const status = $('#catalog-device-status');
  button.disabled = true;
  button.textContent = t('catalog.checking');
  $('#catalog-error').textContent = '';
  const hasSNMP = catalogDetail.checks.some((check) => check.type === 'snmp');
  let options = {};
  if (hasSNMP) {
    options = readOptionValues($('#catalog-options'), catalogCredentialFields);
  } else if (catalogDetail.credential) {
    options[catalogDetail.credential.key] = $('#catalog-credential').value.trim();
  }
  try {
    const data = await api(
      `/api/sensor-templates/${encodeURIComponent(catalogDetail.id)}/check`,
      { method: 'POST', body: { target, options } });
    const device = data.device?.sys_descr || data.device?.sys_object_id || target;
    status.textContent = t('catalog.deviceOk', { device });
    if (catalogDetail.discover) {
      try {
        const found = await api(
          `/api/sensor-templates/${encodeURIComponent(catalogDetail.id)}/discover`,
          { method: 'POST', body: { target, options } });
        const discovered = found.checks || [];
        if (discovered.length) {
          catalogChecks = [...catalogDetail.checks, ...discovered];
          renderCatalogChecks();
          status.textContent += ' · ' + t('catalog.discovered', { count: discovered.length });
        }
        if (found.notes?.length) {
          $('#catalog-discovery-notes').textContent =
            t('catalog.discoveryNotes', { notes: found.notes.join('; ') });
        }
      } catch (err) {
        $('#catalog-discovery-notes').textContent = err.message;
      }
    }
  } catch (err) {
    status.textContent = t('catalog.deviceHint');
    $('#catalog-error').textContent = err.message;
  } finally {
    button.disabled = false;
    button.textContent = t('catalog.checkDevice');
  }
}

/** updateCatalogAdd says how many of the template's checks are ticked. */
function updateCatalogAdd() {
  if (!catalogDetail) return;
  const count = $$('#catalog-body [data-catalog-check]:checked').length;
  const add = $('#catalog-add');
  add.textContent = t('catalog.apply', { count });
  add.disabled = count === 0;
}

/** applyCatalogTemplate creates the ticked sensors in one request. */
async function applyCatalogTemplate() {
  if (!catalogDetail) return;
  const error = $('#catalog-error');
  const name = $('#catalog-name').value.trim();
  const target = $('#catalog-target').value.trim();
  const group = $('#catalog-group').value.trim();
  if (!name || !target) {
    error.textContent = t('catalog.missing', { field: !name ? t('catalog.deviceName') : t('catalog.address') });
    return;
  }
  const checks = catalogChecks.filter((check) =>
    $(`#catalog-body [data-catalog-check="${check.key}"]`)?.checked);
  if (!checks.length) {
    error.textContent = t('catalog.noneSelected');
    return;
  }
  const hasSNMP = catalogDetail.checks.some((check) => check.type === 'snmp');
  const credentials = hasSNMP
    ? readOptionValues($('#catalog-options'), catalogCredentialFields)
    : {};
  const credentialValue = catalogDetail.credential
    ? $('#catalog-credential').value.trim()
    : '';
  if (catalogDetail.credential && !credentialValue) {
    error.textContent = t('catalog.missing', { field: t(catalogDetail.credential.label_key) });
    return;
  }
  const sensors = checks.map((check) => {
    const options = { ...(check.options || {}) };
    if (check.type === 'snmp') Object.assign(options, credentials);
    if (catalogDetail.credential && check.type === 'http' && credentialValue) {
      options[catalogDetail.credential.key] = credentialValue;
    }
    return {
      name: `${name} · ${check.label || t(check.label_key)}`,
      type: check.type,
      target: templateTarget(check.target, target),
      interval: check.interval || 60,
      timeout: 10,
      retries: 1,
      enabled: true,
      group,
      icon: check.icon || catalogDetail.icon,
      options,
    };
  });
  try {
    await api('/api/sensors/bulk', { method: 'POST', body: { sensors } });
    closeCatalog();
    $('#modal').close();
    toast(t('catalog.added', { count: sensors.length }));
    await refresh({ background: true });
  } catch (err) {
    error.textContent = err.message;
  }
}

// One delegated listener for the catalogue: tabs, template cards, the back link,
// the individual type tiles and the Add button. The window's body is rebuilt on
// every view change, so listeners on its children would be lost with them.
document.addEventListener('click', (event) => {
  if (!event.target.closest('#modal-catalog')) return;

  if (event.target.closest('[data-device-reset]')) {
    catalogDevice = { name: '', target: '', group: '' };
    catalogSNMP = {};
    catalogToken = '';
    setCatalogTab('templates');
    return;
  }
  if (event.target.closest('[data-filter-toggle]')) {
    catalogFilterOpen = !catalogFilterOpen;
    syncCatalogFilterUI();
    return;
  }
  if (event.target.closest('[data-clear-vendor]')) {
    catalogFilter.vendor = '';
    renderTemplateGrid();
    return;
  }
  if (event.target.closest('[data-clear-system]')) {
    catalogFilter.system = '';
    renderTemplateGrid();
    return;
  }
  const clearTag = event.target.closest('[data-clear-tag]');
  if (clearTag) {
    catalogFilter.tags.delete(clearTag.dataset.clearTag);
    renderTemplateGrid();
    return;
  }
  if (event.target.closest('[data-clear-all]')) {
    catalogFilter.vendor = '';
    catalogFilter.system = '';
    catalogFilter.tags.clear();
    renderTemplateGrid();
    return;
  }
  const vendor = event.target.closest('[data-filter-vendor]');
  if (vendor) {
    const value = vendor.dataset.filterVendor;
    catalogFilter.vendor = catalogFilter.vendor === value ? '' : value;
    renderTemplateGrid();
    return;
  }
  const system = event.target.closest('[data-filter-system]');
  if (system) {
    const value = system.dataset.filterSystem;
    catalogFilter.system = catalogFilter.system === value ? '' : value;
    renderTemplateGrid();
    return;
  }
  const tag = event.target.closest('[data-filter-tag]');
  if (tag) {
    const value = tag.dataset.filterTag;
    if (catalogFilter.tags.has(value)) catalogFilter.tags.delete(value);
    else catalogFilter.tags.add(value);
    renderTemplateGrid();
    return;
  }
  if (event.target.closest('#catalog-check-device')) {
    checkTemplateDevice();
    return;
  }

  const tab = event.target.closest('[data-catalog-tab]');
  if (tab) {
    setCatalogTab(tab.dataset.catalogTab);
    return;
  }
  const card = event.target.closest('[data-template-id]');
  if (card) {
    const template = (catalogTemplates || []).find((entry) => entry.id === card.dataset.templateId);
    if (template) openTemplateDetail(template);
    return;
  }
  if (event.target.closest('[data-catalog-back]')) {
    setCatalogTab('templates');
    return;
  }
  const tile = event.target.closest('#modal-catalog [data-sensor-type]');
  if (tile) {
    const type = tile.dataset.sensorType;
    if (catalogForm) {
      const form = catalogForm;
      applySensorType(form, type);
      closeCatalog();
      setTimeout(() => form.querySelector('[data-type-open]')?.focus(), 0);
      return;
    }
    const options = type === 'snmp'
      ? { ...catalogSNMP }
      : (type === 'http' && catalogToken ? { bearer_token: catalogToken } : {});
    closeCatalog();
    openSensorEditor(null, type, { ...catalogDevice, options });
    return;
  }
  if (event.target.closest('#catalog-add')) applyCatalogTemplate();
});

document.addEventListener('change', (event) => {
  if (event.target.closest('#catalog-body [data-catalog-check]')) updateCatalogAdd();
  if (event.target.closest('#catalog-options')) {
    syncConditionalOptions(catalogCredentialFields, $('#catalog-options'));
  }
});

document.addEventListener('input', (event) => {
  const query = event.target.closest('[data-template-query]');
  if (!query) return;
  catalogFilter.query = query.value;
  renderTemplateGrid();
});

$('#modal-catalog').addEventListener('close', () => {
  // A close event from an Escape can arrive after the same window has already
  // been opened again; clearing the form here would leave the reopened catalogue
  // with nothing to apply a type to.
  if ($('#modal-catalog').open) return;
  catalogForm = null;
  catalogDetail = null;
  catalogChecks = [];
});

// --- the site and dependency choosers ---------------------------------------

const pickFieldOf = (node) => node.closest('[data-pick-field]');
const pickValueOf = (field) => field?.querySelector('input[type="hidden"]')?.value || '';

/** setPickValue writes a choice into the site or dependency picker. */
function setPickValue(field, value) {
  if (!field) return;
  const input = field.querySelector('input[type="hidden"]');
  if (input) input.value = value;

  const label = field.querySelector('[data-pick-label]');
  const icon = field.querySelector('[data-pick-icon]');
  const status = field.querySelector('[data-pick-status]');
  if (field.dataset.pickField === 'site') {
    const site = (state.sites || []).find((s) => s.name === value);
    if (label) label.textContent = value || t('sites.none');
    if (icon) icon.innerHTML = value ? iconUI.iconMarkup(siteIcon(site), { size: 18 }) : '';
  } else if (field.dataset.pickField === 'group') {
    const settings = iconUI.groupOf(state.groups, value);
    if (label) label.textContent = value || t('sensor.groupNone');
    if (icon) {
      icon.innerHTML = value
        ? ((settings && iconUI.groupIcon(settings, 18)) || iconUI.iconMarkup('builtin:tag', { size: 18 }))
        : '';
    }
  } else {
    const sensor = (state.sensors || []).find((c) => c.name === value);
    if (label) label.textContent = value || t('sensor.parentNone');
    if (icon) icon.innerHTML = value ? iconUI.iconMarkup(sensor?.icon || 'builtin:server', { size: 18 }) : '';
    if (status) status.innerHTML = value ? statusPill(sensor) : '';
  }
  for (const tile of field.querySelectorAll('[data-pick-value]')) {
    tile.classList.toggle('is-current', tile.dataset.pickValue === value);
  }
}

/** renderSitePanel redraws the place list after one was made from this form. */
function renderSitePanel(field) {
  const panel = field?.querySelector('.pick-panel');
  if (panel) panel.innerHTML = sitePanelMarkup(pickValueOf(field));
}

/** renderGroupPanel redraws the group list after one was made from this form. */
function renderGroupPanel(field) {
  const panel = field?.querySelector('.pick-panel');
  if (panel) panel.innerHTML = groupPanelMarkup(pickValueOf(field));
}

/** renderTagPanel redraws the tag list after one was made from this form. */
function renderTagPanel(field) {
  const panel = field?.querySelector('.pick-panel');
  if (panel) panel.innerHTML = tagPanelMarkup(tagSelectedNames(field));
}

/** openPickPanel shows one picker, closes any other and focuses its search. */
function openPickPanel(field) {
  const panel = field.querySelector('.pick-panel');
  if (!panel || !panel.hidden) return;
  closePickPanels(field);
  closeTypePanels(null);
  const query = panel.querySelector('[data-pick-query]');
  if (query) {
    query.value = '';
    filterPickTiles(field, '');
  }
  panel.hidden = false;
  field.querySelector('[data-pick-open]')?.setAttribute('aria-expanded', 'true');
  query?.focus();
  placePanel(field, panel);
}

/** closePickPanel hides one picker. */
function closePickPanel(field) {
  const panel = field?.querySelector('.pick-panel');
  if (!panel || panel.hidden) return;
  panel.hidden = true;
  clearPanelPlacement(panel);
  field.querySelector('[data-pick-open]')?.setAttribute('aria-expanded', 'false');
}

/** closePickPanels closes every picker but one, since only one may be open. */
function closePickPanels(except) {
  for (const field of document.querySelectorAll('[data-pick-field]')) {
    if (field !== except) closePickPanel(field);
  }
}

/** filterPickTiles narrows a picker to the entries matching its search box. */
function filterPickTiles(field, term) {
  const panel = field.querySelector('.pick-panel');
  if (!panel) return;
  const kind = field.dataset.pickField;
  const needle = term.trim().toLowerCase();
  let visible = 0;
  for (const tile of panel.querySelectorAll('[data-pick-value]')) {
    const empty = tile.dataset.pickValue === '';
    const haystack = `${tile.dataset.pickValue} ${tile.textContent}`.toLowerCase();
    // A search hides the "none" choice: somebody typing a name is not looking
    // for "No site" or "Nothing", and it would otherwise be the first match for
    // a term it happens to contain.
    const show = !needle || (!empty && haystack.includes(needle));
    tile.hidden = !show;
    if (show) visible++;
  }
  for (const group of panel.querySelectorAll('[data-pick-group]')) {
    group.hidden = ![...group.querySelectorAll('[data-pick-value]')].some((tile) => !tile.hidden);
  }

  const note = panel.querySelector('[data-pick-note]');
  if (note) {
    const key = { site: 'sensor.siteNoMatch', group: 'sensor.groupNoMatch', tags: 'tags.noMatch' }[kind]
      || 'sensor.parentNoMatch';
    note.hidden = visible !== 0;
    note.textContent = visible === 0 ? t(key, { query: term.trim() }) : '';
  }
}

/**
 * mountPickPanels wires the place and dependency pickers once, on the document.
 * They follow the type chooser's keyboard and focus rules: Enter picks the first
 * match and must not submit the form, Escape closes the panel and not the dialog,
 * and focus returns to the button one task later, after the keystroke is done.
 */
function mountPickPanels() {
  document.addEventListener('click', (event) => {
    const open = event.target.closest('[data-pick-open]');
    if (open) {
      const field = pickFieldOf(open);
      const panel = field?.querySelector('.pick-panel');
      if (panel && !panel.hidden) closePickPanel(field);
      else if (field) openPickPanel(field);
      return;
    }

    const freshGroup = event.target.closest('[data-pick-new-group]');
    if (freshGroup) {
      const field = pickFieldOf(freshGroup);
      closePickPanel(field);
      openLabelEditor('group', '', null, { creating: true, onSaved: (name) => {
        renderGroupPanel(field);
        setPickValue(field, name);
        const button = field?.querySelector('[data-pick-open]');
        if (button) setTimeout(() => button.focus(), 0);
      }});
      return;
    }

    const fresh = event.target.closest('[data-pick-new-site]');
    if (fresh) {
      const field = pickFieldOf(fresh);
      closePickPanel(field);
      openSiteFromSensor((name) => {
        renderSitePanel(field);
        setPickValue(field, name);
        const button = field?.querySelector('[data-pick-open]');
        if (button) setTimeout(() => button.focus(), 0);
      });
      return;
    }

    const clearTags = event.target.closest('[data-pick-clear]');
    if (clearTags) {
      setTagValue(pickFieldOf(clearTags), []);
      return;
    }

    const freshTag = event.target.closest('[data-pick-new-tag]');
    if (freshTag) {
      const field = pickFieldOf(freshTag);
      closePickPanel(field);
      openLabelEditor('tag', '', null, { creating: true, onSaved: (name) => {
        const names = new Set(tagSelectedNames(field));
        names.add(name);
        renderTagPanel(field);
        setTagValue(field, [...names]);
        const button = field?.querySelector('[data-pick-open]');
        if (button) setTimeout(() => button.focus(), 0);
      }});
      return;
    }

    const pick = event.target.closest('[data-pick-value]');
    if (pick) {
      const field = pickFieldOf(pick);
      if (field?.dataset.pickMulti === 'true') {
        const names = new Set(tagSelectedNames(field));
        const value = pick.dataset.pickValue;
        if (names.has(value)) names.delete(value);
        else names.add(value);
        setTagValue(field, [...names]);
        // A multi-select stays open: choosing one tag is rarely the whole answer.
        return;
      }
      setPickValue(field, pick.dataset.pickValue);
      closePickPanel(field);
      const button = field?.querySelector('[data-pick-open]');
      if (button) setTimeout(() => button.focus(), 0);
      return;
    }

    if (!event.target.closest('.pick-panel')) closePickPanels(null);
  });

  document.addEventListener('input', (event) => {
    const query = event.target.closest('[data-pick-query]');
    if (!query) return;
    const field = pickFieldOf(query);
    if (field) filterPickTiles(field, query.value);
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') {
      const query = event.target.closest('[data-pick-query]');
      if (!query) return;
      event.preventDefault();
      const field = pickFieldOf(query);
      const term = query.value.trim();

      // In a multi-select Enter toggles the first match and leaves the panel open,
      // because the next thing somebody types is usually another tag.
      if (field?.dataset.pickMulti === 'true') {
        const first = field.querySelector('[data-pick-value]:not([hidden])');
        if (first) {
          const names = new Set(tagSelectedNames(field));
          const value = first.dataset.pickValue;
          if (names.has(value)) names.delete(value);
          else names.add(value);
          setTagValue(field, [...names]);
        }
        return;
      }

      let first = term
        ? [...field.querySelectorAll('[data-pick-value]:not([hidden])')]
          .find((tile) => tile.dataset.pickValue.toLowerCase() === term.toLowerCase())
        : null;
      first = first || field?.querySelector('[data-pick-value]:not([hidden])');
      if (!first) return;
      setPickValue(field, first.dataset.pickValue);
      closePickPanel(field);
      const button = field?.querySelector('[data-pick-open]');
      if (button) setTimeout(() => button.focus(), 0);
      return;
    }
    if (event.key !== 'Escape') return;
    const panel = document.querySelector('.pick-panel:not([hidden])');
    if (!panel) return;
    event.preventDefault();
    const field = panel.closest('[data-pick-field]');
    closePickPanel(field);
    field?.querySelector('[data-pick-open]')?.focus();
  });
}

// --- panel placement ---------------------------------------------------------

// A panel is fixed to the viewport rather than absolute inside the dialog. An
// absolute panel that grows past the dialog is part of the dialog's scrollable
// area, so a long dependency list added a scrollbar to the form and a wide one
// added a horizontal bar. Fixed positioning does not, and the coordinates below
// keep the panel inside the window.
const PANEL_MARGIN = 8;
const PANEL_GAP = 6;

/** placePanel anchors an open panel to its button, clamped to the window. */
function placePanel(field, panel) {
  const button = field.querySelector('[data-type-open], [data-pick-open]');
  if (!button) return;
  const rect = button.getBoundingClientRect();
  // The type chooser is 560 px wide and opens leftwards from its right column;
  // the place, dependency and group pickers are 420 px and open rightwards.
  const wanted = panel.classList.contains('pick-panel') ? 420 : 560;
  const width = Math.min(wanted, window.innerWidth - PANEL_MARGIN * 2);
  const alignRight = !panel.classList.contains('pick-panel');
  let left = alignRight ? rect.right - width : rect.left;
  left = Math.max(PANEL_MARGIN, Math.min(left, window.innerWidth - width - PANEL_MARGIN));

  const below = window.innerHeight - rect.bottom - PANEL_GAP - PANEL_MARGIN;
  const above = rect.top - PANEL_GAP - PANEL_MARGIN;
  const useBelow = below >= 220 || below >= above;
  const available = Math.max(useBelow ? below : above, 160);

  panel.style.position = 'fixed';
  panel.style.width = `${width}px`;
  panel.style.left = `${left}px`;
  panel.style.right = 'auto';
  panel.style.top = useBelow ? `${rect.bottom + PANEL_GAP}px` : 'auto';
  panel.style.bottom = useBelow ? 'auto' : `${window.innerHeight - rect.top + PANEL_GAP}px`;
  panel.style.maxHeight = `${available}px`;
}

/** clearPanelPlacement hands the panel back to the stylesheet. */
function clearPanelPlacement(panel) {
  for (const prop of ['position', 'width', 'left', 'right', 'top', 'bottom', 'maxHeight']) {
    panel.style[prop] = '';
  }
}

/** repositionPanels follows the button after a resize or a scroll of the dialog. */
function repositionPanels() {
  for (const panel of document.querySelectorAll('.type-panel:not([hidden]), .pick-panel:not([hidden])')) {
    const field = panel.closest('[data-type-field], [data-pick-field]');
    if (field) placePanel(field, panel);
  }
}

/** mountPanelPlacement keeps open panels anchored while the window moves. */
function mountPanelPlacement() {
  window.addEventListener('resize', repositionPanels);
  // Only the dialog's own scroll moves the button. A scroll inside the panel must
  // not reposition it, and the event would otherwise arrive here from the panel too.
  document.addEventListener('scroll', (event) => {
    if (event.target && event.target.id === 'modal') repositionPanels();
  }, true);
}

function sensorFormMarkup(sensor) {
  const isNew = !sensor;
  const def = typeDef(sensor?.type || 'http');
  const channels = state.channels.map((c) => {
    const selected = (sensor?.channel_ids || []).includes(c.id);
    return `<label class="sensor-row"><input type="checkbox" name="channel" value="${c.id}"${selected ? ' checked' : ''}>
      <span>${esc(c.name)} <small class="muted">${esc(c.type)}</small></span></label>`;
  }).join('');
  const escalationChannels = state.channels.map((c) => {
    const selected = (sensor?.escalate_channel_ids || []).includes(c.id);
    return `<label class="sensor-row"><input type="checkbox" name="escalate_channel" value="${c.id}"${selected ? ' checked' : ''}>
      <span>${esc(c.name)} <small class="muted">${esc(c.type)}</small></span></label>`;
  }).join('');

  // The name row: the symbol first, then the name. The symbol is a button and not
  // part of the label, because a label forwards its click to the first labelable
  // thing inside it — the button — and then pressing "Name" would open the picker.
  // The label is tied to the input with `for` instead, which is what it was for.
  return `
    <div class="grid-2">
      <div class="field-row">
        <label for="sensor-name">${withHelp(t('sensor.name'), 'help.sensor.name')}</label>
        <div class="field-row-input">
          ${iconUI.iconFieldMarkup(sensor?.icon || '', { compact: true })}
          <input id="sensor-name" name="name" type="text" required value="${esc(sensor?.name || '')}" autocomplete="off">
        </div>
      </div>
      ${sensorTypeFieldMarkup(def)}
    </div>

    <label class="field"><span id="target-label">${withHelp(t(def.targetLabelKey), 'help.sensor.target')}</span>
      <input name="target" type="text" required value="${esc(sensor?.target || '')}" placeholder="${esc(def.placeholder)}" autocomplete="off"></label>

    <div class="grid-3">
      <label class="field"><span>${withHelp(t('sensor.interval'), 'help.sensor.interval')}</span>
        <input name="interval" type="number" min="5" max="86400" value="${sensor?.interval || 60}"></label>
      <label class="field"><span>${withHelp(t('sensor.timeout'), 'help.sensor.timeout')}</span>
        <input name="timeout" type="number" min="1" max="600" value="${sensor?.timeout || 10}"></label>
      <label class="field adv-hide"><span>${withHelp(t('sensor.retries'), 'help.sensor.retries')}</span>
        <input name="retries" type="number" min="1" max="10" value="${sensor?.retries || 1}"></label>
    </div>

    <div class="grid-3 adv-hide">
      ${groupFieldMarkup(sensor?.group || '')}
      ${siteFieldMarkup(sensor?.site)}
      ${tagFieldMarkup(sensor?.tags || [])}
      ${parentFieldMarkup(sensor?.parent, sensor?.name)}
    </div>

    <label class="field adv-hide"><span>${withHelp(t('sensor.reminder'), 'help.sensor.reminder')}</span>
      <input name="reminder_every" type="number" min="0" value="${sensor?.reminder_every || 0}"></label>

    <details class="options adv-hide"${isNew ? ' open' : ''}>
      <summary id="option-summary">${esc(t(def.labelKey))}</summary>
      <div id="option-fields" class="grid-2">${def.fields.map((f) => `
        <div class="option-slot" data-option-key="${esc(f.key)}">${optionField(f, sensor?.options)}</div>`).join('')}</div>
    </details>

    <fieldset class="channels-pick adv-hide">
      <legend>${withHelp(t('sensor.channels'), 'help.sensor.channels')}</legend>
      ${channels || emptyBlock(t('channels.emptyBody'))}
    </fieldset>

    <div class="grid-2 adv-hide">
      <label class="field"><span>${withHelp(t('sensor.escalateAfter'), 'help.sensor.escalateAfter')}</span>
        <input name="escalate_after" type="number" min="0" max="43200"
               value="${Math.round((sensor?.escalate_after || 0) / 60)}"></label>
      <fieldset class="channels-pick">
        <legend>${withHelp(t('sensor.escalateChannels'), 'help.sensor.escalateChannels')}</legend>
        ${escalationChannels || emptyBlock(t('channels.emptyBody'))}
      </fieldset>
    </div>

    <label class="sensor-row"><input type="checkbox" name="enabled"${sensor?.enabled === false ? '' : ' checked'}>
      <span>${withHelp(t('sensor.enabled'), 'help.sensor.enabled')}</span></label>`;
}

async function openSensorEditor(existing = null, presetType = null, prefill = null) {
  await loadChannels(existing === null);
  // The tag field draws the symbols and colours from the tag settings, and the
  // picker offers "New tag…"; both need the list before the form is built.
  await loadTags();
  const isNew = existing === null;
  const form = $('#modal-form');
  const draft = existing || (presetType ? { type: presetType, options: prefill?.options || {} } : null);

  modal.open({
    title: isNew ? t('sensor.new') : t('sensor.edit'),
    body: sensorFormMarkup(draft),
    mode: 'sensor',
    id: existing ? existing.id : '',
    showTest: true,
  });

  form.dataset.lastType = presetType || existing?.type || 'http';
  if (prefill) {
    if (prefill.name) form.elements.name.value = prefill.name;
    if (prefill.target && form.elements.target) form.elements.target.value = prefill.target;
    if (prefill.group && form.elements.group) form.elements.group.value = prefill.group;
  }
  syncConditionalOptions(typeDef(form.dataset.lastType).fields);
  // The tiles call applySensorType directly; the hidden input keeps a change
  // from a script — or from a test — working the way the old select did.
  const typeInput = $('#sensor-type');
  typeInput.addEventListener('change', () => applySensorType(form, typeInput.value));

  if (!isNew && !existing.managed) {
    modal.danger(t('sensor.delete'), () => deleteSensor(existing));
  }
}

function readSensorForm(form) {
  const def = typeDef(form.elements.type.value);
  return {
    name: form.elements.name.value.trim(),
    type: form.elements.type.value,
    target: form.elements.target.value.trim(),
    interval: Number(form.elements.interval.value) || 60,
    timeout: Number(form.elements.timeout.value) || 10,
    retries: Number(form.elements.retries.value) || 1,
    enabled: form.elements.enabled.checked,
    group: form.elements.group.value.trim(),
    parent: form.elements.parent.value,
    site: form.elements.site.value,
    icon: iconUI.readIconField(form),
    tags: form.elements.tags.value.split(',').map((v) => v.trim()).filter(Boolean),
    reminder_every: Number(form.elements.reminder_every.value) || 0,
    escalate_after: (Number(form.elements.escalate_after.value) || 0) * 60,
    options: readOptions(form, def),
    channel_ids: $$('input[name="channel"]:checked', form).map((el) => Number(el.value)),
    escalate_channel_ids: $$('input[name="escalate_channel"]:checked', form).map((el) => Number(el.value)),
  };
}

/**
 * openSiteFromSensor opens the site editor over the sensor form.
 *
 * Creating a sensor is often the moment somebody first knows where it stands, so
 * the place picker carries an entry that makes one, rather than sending them to
 * Settings and back — a trip that loses the half-filled form. What answers is the
 * site editor itself, the same window Settings → Sites opens: one dialog makes
 * places, and a second set of fields with its own names and no coordinates would
 * be a copy to keep in step forever.
 *
 * The sensor form is not saved and not rebuilt; it stays behind the window with
 * everything in it, which is the whole reason the site editor is its own dialog.
 * Choosing "New site…" does not touch the field: when the place is made its name
 * is put straight into the picker, and when the window is dismissed the picker
 * still holds whatever it held before.
 *
 * The place is made when this window is saved, not when the sensor is. Abandoning
 * the sensor afterwards therefore leaves the place standing — the one thing this
 * arrangement gives up, and it is worth saying out loud rather than discovering.
 */
function openSiteFromSensor(onSaved) {
  openSiteDialog(null, {
    hint: t('sites.fromSensorHint'),
    onSaved: (name) => {
      // Re-read rather than append: the list is the server's, and the tree behind
      // the window wants to know about it too. The picker is then drawn from that
      // list again, because it was drawn when the form opened and the place was
      // made after that — without the redraw the entry is not there to choose.
      loadSites(true).then(() => onSaved(name));
    },
  });
}

async function saveSensor(payload, id) {
  const saved = id
    ? await api(`/api/sensors/${id}`, { method: 'PUT', body: payload })
    : await api('/api/sensors', { method: 'POST', body: payload });
  await refresh({ background: true });
  return saved;
}

$('#new-sensor').addEventListener('click', () => openSensorCatalog(null));
document.addEventListener('click', (event) => {
  if (event.target.closest('[data-action="new-sensor"]')) openSensorCatalog(null);
});

// Modal plumbing: one form, several modes.
//
// The modes users.js owns are listed rather than probed for, so that the sensor
// path — the one that runs when nothing else matches — cannot quietly swallow a
// typo in a mode name. The site editor is not among them: it has a dialog and a
// form of its own, and its submit handler is below.
const USER_MODES = new Set(['user', 'user-password', 'totp', 'totp-off', 'totp-codes', 'recovery']);

$('#modal-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  const error = $('#modal-error');
  error.textContent = '';
  const save = $('#modal-save');
  save.disabled = true;
  try {
    const mode = form.dataset.mode;
    // Only the dialogs that own the submit button get closed by it. Enrolling a
    // second factor ends by putting the recovery codes on screen, and closing
    // here would take them away again in the same tick.
    let done = true;
    if (mode === 'ack') {
      await submitAck(form);
    } else if (mode === 'channel') {
      await submitChannel(form);
    } else if (USER_MODES.has(mode)) {
      done = await users.submit(mode, form);
    } else {
      const id = form.dataset.id ? Number(form.dataset.id) : 0;
      await saveSensor(readSensorForm(form), id);
      toast(t('common.saved'));
    }
    if (done) modal.close();
  } catch (err) {
    error.textContent = err.message;
  } finally {
    save.disabled = false;
  }
});

// The site editor's own form, handled on its own dialog. It shares no error line
// and no save button with the dialog above it, which is the point of there being
// two: a refusal here is written under this window, and the sensor form behind it
// is exactly as it was.
$('#window-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  const error = $('#window-error');
  error.textContent = '';
  const save = $('#window-save');
  save.disabled = true;
  try {
    await submitWindow(form);
    windowModal.close();
  } catch (err) {
    // The message goes into the dialog rather than into a toast, because every one
    // of these is about a field in the form in front of you: "a window is for one
    // sensor or for one group" is only useful while the select that chose is still
    // on screen, and a toast that fades leaves somebody guessing which one.
    error.textContent = err.message;
  } finally {
    save.disabled = false;
  }
});

$('#site-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  const error = $('#site-error');
  error.textContent = '';
  const save = $('#site-save');
  save.disabled = true;
  try {
    await submitSite(form);
    siteModal.close();
  } catch (err) {
    error.textContent = err.message;
  } finally {
    save.disabled = false;
  }
});

$('#group-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.target;
  const error = $('#group-error');
  error.textContent = '';
  const save = $('#group-save');
  save.disabled = true;
  try {
    await submitLabel(form);
    groupModal.close();
  } catch (err) {
    // In the dialog and not in a toast, for the reason the site form gives: these
    // are messages about the fields in front of you — "there is no built-in icon
    // nope" is only useful while the picker that chose it is still on screen.
    error.textContent = err.message;
  } finally {
    save.disabled = false;
  }
});

$('#modal-test').addEventListener('click', async () => {
  const form = $('#modal-form');
  const error = $('#modal-error');
  error.textContent = '';
  const button = $('#modal-test');
  button.disabled = true;
  button.textContent = t('sensor.testing');

  // say writes a result down where it can still be read afterwards, and
  // announces it once. Both, because the two are read at different moments: the
  // footer line stands beside the button that was just pressed and stays until
  // the next press, and the message is a strip that takes itself away. A result
  // written in one place only is a result somebody misses, and that is what "the
  // Test button does nothing" was — the form was incomplete, the server answered
  // with the reason, and the answer went into one thin line nobody noticed.
  const say = (text, kind) => {
    error.textContent = text;
    toast(text, kind);
  };

  // Test what is on screen, not what is saved: the point is to catch a typo
  // before it becomes a monitored sensor.
  const sensor = form.dataset.mode === 'channel' ? null : readSensorForm(form);
  try {
    if (form.dataset.mode === 'channel') {
      const body = readChannelForm(form);
      const id = form.dataset.id;
      await api(id ? `/api/channels/test?id=${id}` : '/api/channels/test', { method: 'POST', body });
      toast(t('channels.testOk'));
    } else {
      const result = await api('/api/sensors/validate', { method: 'POST', body: { ...sensor, probe: true } });
      if (!result.valid) {
        say(result.error, 'error');
      } else if (result.result) {
        const line = `${result.result.success ? t('sensor.testOk') : t('sensor.testFail')} · ${C.fmtLatency(result.result.latency_ms)} · ${result.result.message}`;
        // Why the probe failed is the whole value of having pressed the button;
        // a strip that leaves after four seconds would take it with it.
        if (result.result.success) {
          toast(line);
        } else {
          say(line, 'error');
        }
      } else {
        toast(t('sensor.testOk'));
      }
    }
  } catch (err) {
    say(err.message, 'error');
  } finally {
    button.disabled = false;
    button.textContent = t('sensor.test');
  }
});

async function deleteSensor(sensor) {
  if (!confirmDialog(t('sensor.deleteConfirm', { name: sensor.name }))) return;
  await api(`/api/sensors/${sensor.id}`, { method: 'DELETE' });
  $('#modal').close();
  $('#detail').close();
  toast(t('common.saved'));
  await refresh({ background: true });
}

// Dialog closing: [data-close] buttons and the channel editor's stale state.
document.addEventListener('click', (event) => {
  if (event.target.closest('[data-close]')) {
    const dialog = event.target.closest('dialog');
    if (dialog) dialog.close();
    return;
  }
  // The recovery codes are the one thing the dashboard shows exactly once, so
  // "Copy all" is a plain button in the dialog body rather than a footer action —
  // it must not be confused with the acknowledgement that ends the dialog.
  if (event.target.closest('[data-action="copy-codes"]')) {
    const codes = $$('#modal-body .recovery-codes code').map((el) => el.textContent).join('\n');
    copyText(codes).then(() => toast(t('settings.copied')));
  }
});
// Acknowledging straight from the sensor's dialog. Delegated once, on the
// dialog that persists, so reopening it does not stack handlers.
$('#detail').addEventListener('click', (event) => {
  // The offer to catch up. Pressing it is the only thing that redraws the body
  // while the window is open, which is what makes it the reader's decision.
  if (event.target.closest('#detail-notice')) {
    if (state.detail) void loadDetailBody(state.detail);
    return;
  }
  const button = event.target.closest('[data-action]');
  if (!button) return;
  const sensor = state.sensors.find((c) => c.id === state.detail);
  const incident = sensor?.open_incident?.id;
  if (!incident) return;
  if (button.dataset.action === 'ack') openAckDialog(incident);
  else if (button.dataset.action === 'unack') unack(incident);
});

$('#detail').addEventListener('close', () => {
  state.detail = null;
  state.pendingDetail = null;
  // Dismissing the dialog takes the URL back to the dashboard, so a reload does
  // not silently reopen a dialog the person just closed.
  if (parseHash(location.hash).sensor !== null) history.replaceState(null, '', '#/dashboard');
});

// ---------------------------------------------------------------------------
// alert channels
// ---------------------------------------------------------------------------

$('#new-channel').addEventListener('click', () => openChannelEditor(null));

async function openChannelEditor(existing) {
  const types = await loadChannelTypes();
  const form = $('#modal-form');

  const def = types.find((x) => x.type === (existing?.type || types[0].type)) || types[0];
  modal.open({
    title: existing ? t('channels.edit') : t('channels.new'),
    mode: 'channel',
    id: existing ? existing.id : '',
    showTest: true,
    body: `
      <div class="grid-2">
        <label class="field"><span>${esc(t('channels.name'))}</span>
          <input name="name" type="text" required value="${esc(existing?.name || '')}" autocomplete="off"></label>
        <label class="field"><span>${esc(t('channels.type'))}</span>
          <select name="type" id="channel-type">
            ${types.map((x) => `<option value="${x.type}"${x.type === def.type ? ' selected' : ''}>${esc(x.label)}</option>`).join('')}
          </select></label>
      </div>
      <div id="channel-fields">${channelFields(def, existing?.config)}</div>
      <label class="sensor-row"><input type="checkbox" name="enabled"${existing?.enabled === false ? '' : ' checked'}>
        <span>${esc(t('channels.enabled'))}</span></label>`,
  });

  const typeSelect = $('#channel-type');
  typeSelect.addEventListener('change', () => {
    const next = types.find((x) => x.type === typeSelect.value) || types[0];
    const kept = {};
    for (const spec of def.fields) {
      const input = form.elements[`cfg_${spec.key}`];
      if (input && input.value) kept[spec.key] = input.value;
    }
    $('#channel-fields').innerHTML = channelFields(next, kept);
  });

  if (existing && !existing.managed) {
    modal.danger(t('common.delete'), async () => {
      if (!confirmDialog(t('channels.deleteConfirm', { name: existing.name }))) return;
      await api(`/api/channels/${existing.id}`, { method: 'DELETE' });
      modal.close();
      await loadChannels(true);
      renderChannels();
    });
  }
}

/** One field of a channel type, built from the API's spec. */
function channelField(spec, def, config) {
  const override = t(`channels.opt.${def.type}.${spec.key}`);
  const label = override === `channels.opt.${def.type}.${spec.key}` ? spec.label : override;
  // The field's Help and Example are written in Go (internal/notify) and arrive
  // with the type definition, so they need no catalogue entry here.
  const help = helpButtonText(spec.help, spec.example);
  const value = config?.[spec.key];
  const required = spec.required ? ' required' : '';
  const type = spec.secret ? 'password' : 'text';
  const name = `cfg_${spec.key}`;
  const input = `<input name="${esc(name)}" type="${type}"${required}
           value="${value === undefined ? '' : esc(value)}"
           placeholder="${esc(spec.placeholder || '')}" autocomplete="off">`;
  const body = spec.generate
    ? `<span class="secret-field">${input}<button type="button" class="btn btn-small"
         data-generate-secret="${esc(name)}">${esc(t('common.generate'))}</button></span>`
    : input;
  return `<label class="field"><span>${esc(label)}${spec.required ? ' *' : ''} ${help}</span>
    ${body}</label>`;
}

/**
 * Build the type's form from the API's field specs, with i18n overrides. The
 * message templates are folded away: most people never touch them, and the two
 * textareas dominate a form that is otherwise a name and a token.
 */
function channelFields(def, config = {}) {
  const isTemplate = (spec) => spec.key === 'title_template' || spec.key === 'body_template';
  const plain = def.fields.filter((s) => !isTemplate(s)).map((s) => channelField(s, def, config)).join('');
  const templates = def.fields.filter(isTemplate);
  const folded = templates.length ? `<details class="adv-fold">
      <summary>${esc(t('channels.templates'))}</summary>
      <div class="grid-2">${templates.map((s) => channelField(s, def, config)).join('')}</div>
    </details>` : '';
  return plain + folded + channelSetup(def);
}

/**
 * The recipe for this channel type: where the token comes from, where the chat
 * or the room comes from, in the order somebody does it the first time.
 *
 * It is written in Go beside the field descriptions (internal/notify) for the
 * same reason those are: the recipe names fields, so the two have to travel
 * together, and a recipe that talks about a box the form no longer has is worse
 * than none. It is drawn in full, not behind a fold — the reader who needs it is
 * exactly the reader who does not know it is worth opening — and it is rebuilt
 * with the fields whenever the type changes, so the two can never disagree.
 */
function channelSetup(def) {
  if (!def.setup || !def.setup.length) return '';
  const steps = def.setup.map((step) => `<li>${esc(step)}</li>`).join('');
  return `<div class="channel-setup">
      <h4>${esc(t('channels.setup', { type: def.label }))}</h4>
      <ol>${steps}</ol>
    </div>`;
}

function readChannelForm(form) {
  const type = form.elements.type.value;
  const def = state.channelTypes.find((x) => x.type === type);
  const config = {};
  for (const spec of def.fields) {
    const input = form.elements[`cfg_${spec.key}`];
    if (input && input.value !== '') config[spec.key] = input.value;
  }
  return { name: form.elements.name.value.trim(), type, enabled: form.elements.enabled.checked, config };
}

async function submitChannel(form) {
  const payload = readChannelForm(form);
  const id = form.dataset.id ? Number(form.dataset.id) : 0;
  if (id) {
    await api(`/api/channels/${id}`, { method: 'PUT', body: payload });
  } else {
    await api('/api/channels', { method: 'POST', body: payload });
  }
  await loadChannels(true);
  renderChannels();
  toast(t('common.saved'));
}

function renderChannels() {
  const list = $('#channel-list');
  $('#channels-empty').hidden = state.channels.length !== 0;
  list.innerHTML = state.channels.map((c) => `
    <article class="card channel${c.enabled ? '' : ' is-paused'}" data-id="${c.id}">
      <header class="card-head">
        <h3 class="card-name">${esc(c.name)}</h3>
        ${c.managed ? `<span class="pill st-paused"><i class="dot"></i>${esc(t('sensor.managed'))}</span>` : ''}
      </header>
      <p class="card-target">${esc(c.type)}</p>
      <p class="muted">${c.sensor_count ? esc(t('channels.usedBy', { n: c.sensor_count })) : esc(t('channels.unused'))}</p>
      ${c.last_error ? `<p class="form-error">${esc(t('channels.error'))}: ${esc(c.last_error)}</p>` : ''}
      <div class="card-actions">
        <button class="btn" data-action="test" data-id="${c.id}">${esc(t('channels.test'))}</button>
        <button class="btn" data-action="edit" data-id="${c.id}"${c.managed ? ' disabled' : ''}>${esc(t('sensor.edit'))}</button>
      </div>
    </article>`).join('');
}

$('#channel-list').addEventListener('click', async (event) => {
  const button = event.target.closest('[data-action]');
  if (!button) return;
  const channel = state.channels.find((c) => c.id === Number(button.dataset.id));
  if (!channel) return;
  if (button.dataset.action === 'edit') {
    openChannelEditor(channel);
    return;
  }
  if (button.dataset.action === 'test') {
    button.disabled = true;
    button.textContent = t('channels.testing');
    try {
      await api(`/api/channels/${channel.id}/test`, { method: 'POST' });
      toast(t('channels.testOk'));
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      button.disabled = false;
      button.textContent = t('channels.test');
    }
  }
});

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

const choice = (name, value, options, extra = '') => `
  <label class="field${extra ? ` ${extra}` : ''}"><span>${esc(t(`settings.${name}`))}</span>
    <select data-setting="${name}">
      ${options.map((o) => `<option value="${o}"${o === value ? ' selected' : ''}>${esc(t(`settings.${name}.${o}`))}</option>`).join('')}
    </select></label>`;

/**
 * apiKeyText is what stands in the key's place.
 *
 * The server leaves the key out of the settings for anyone below administrator,
 * so "None" would be a lie to a viewer — it would read as "this installation has
 * no API key" rather than "this is not yours to see". The role is named instead.
 */
function apiKeyText(key) {
  if (!can('admin')) return esc(t('common.needsRole', { role: roleLabel('admin') }));
  return key ? esc(key) : esc(t('settings.apiKeyNone'));
}

/**
 * widgetPicker is the settings half of the dashboard header.
 *
 * The strip can be arranged on the dashboard itself, by dragging — but dragging
 * is a pointer gesture, and a wall of tiles is a poor place to read what is
 * available in the first place. So the same list, in the same order, appears here
 * as checkboxes and two arrows: it names each widget, it works from the keyboard,
 * and it is where somebody looks for the setting rather than stumbling over it.
 *
 * Both write the same account preference, so the two views of it cannot disagree
 * — and a widget switched off here is off in the strip the moment it is clicked.
 */
/**
 * widgetsPanel is what the top of the dashboard shows, and how it is written.
 *
 * The clock is here rather than in Appearance because it is a widget's setting and not
 * the dashboard's: the format it is written in belongs to the one tile that writes a
 * time. It is a preference like the theme, so it goes through the same write path as the
 * rest — see PrefClock, which is stored per account.
 */
function widgetsPanel() {
  return `
    <section class="panel" data-panel="widgets" data-group="widgets">
      <div class="panel-head">
        <h2>${esc(t('settings.widgets'))}</h2>
        <p class="panel-hint">${esc(t('settings.widgetsHint'))}</p>
      </div>
      <div class="grid-2">
        ${choice('clock', ui.clock, ['auto', '24', '12'])}
      </div>
      <p class="muted small">${esc(t('settings.clockHint'))}</p>
      ${widgetPicker()}
    </section>`;
}

/**
 * widgetPicker is the list of every widget there is, with a place to move it to.
 *
 * It is drawn once, in the Widgets category, and that is the whole answer to where
 * the header is arranged. It used to be drawn twice as well — here and at the bottom
 * of the Dashboard panel under Monitoring — which put one list in two categories and
 * left "which of these two is the real one" to be worked out by whoever found them.
 * Both copies wrote the same preference, so neither was wrong; a setting that appears
 * in two places still reads as two settings.
 *
 * The class is what the redraw goes by.
 */
function widgetPicker() {
  return `
    <ul class="widget-picks" id="widget-picks">${widgetPickRows()}</ul>
    <div class="form-actions">
      <button type="button" class="btn btn-small" data-pick-reset>${esc(t('widgets.reset'))}</button>
    </div>`;
}

/**
 * widgetPickRows draws the picker from the stored list.
 *
 * The rows are in the registry's own order — every widget there is, not only the
 * ones in use — because this list is also the answer to "what else is there". A
 * widget that is not on the strip has no place to move to, which is what disables
 * its arrows rather than hiding them: hiding the control would make the row jump
 * height as widgets are switched on and off.
 */
function widgetPickRows() {
  const chosen = widgetsUI.parse(ui.widgets);
  return widgetsUI.WIDGET_IDS.map((id) => {
    const at = chosen.indexOf(id);
    return `<li class="widget-pick">
      <label class="widget-pick-label">
        <input type="checkbox" data-pick-toggle="${id}"${at >= 0 ? ' checked' : ''}>
        <span>${esc(t(`widgets.${id}`))}</span>
      </label>
      <button type="button" class="icon-btn" data-pick-move="-1" data-pick-id="${id}"${at > 0 ? '' : ' disabled'}
              title="${esc(t('widgets.moveLeft'))}" aria-label="${esc(t('widgets.moveLeft'))}">◀</button>
      <button type="button" class="icon-btn" data-pick-move="1" data-pick-id="${id}"${at >= 0 && at < chosen.length - 1 ? '' : ' disabled'}
              title="${esc(t('widgets.moveRight'))}" aria-label="${esc(t('widgets.moveRight'))}">▶</button>
    </li>`;
  }).join('');
}

/**
 * persistWidgets writes a new header, and keeps the panel in step with it.
 *
 * The strip itself follows from the preference — applyUIPreferences hands the
 * stored value over on every change — so the only thing left to do here is the
 * picker, which is not redrawn by a preference change and would otherwise go on
 * showing the list from before the click. On a refusal persistUI has already put
 * the old value back, so this redraws the truth either way.
 */
async function persistWidgets(list) {
  await persistUI('widgets', list.join(','));
  syncWidgetPicker();
}

/**
 * syncWidgetPicker redraws every copy of the picker's rows from the stored list.
 *
 * "Every", not "the": the list is in the Widgets category as well as in the window
 * the arranging button opens, and the two are on screen at different times — so one
 * is redrawn that nobody is looking at, which is cheaper than working out which.
 */
function syncWidgetPicker() {
  for (const host of $$('.widget-picks')) host.innerHTML = widgetPickRows();
}

/** Move one widget one place along in the stored list. */
function movePickedWidget(id, delta) {
  const list = widgetsUI.parse(ui.widgets);
  const at = list.indexOf(id);
  const to = at + delta;
  if (at < 0 || to < 0 || to >= list.length) return;
  const next = list.slice();
  next.splice(at, 1);
  next.splice(to, 0, id);
  void persistWidgets(next);
}

/**
 * examplePanel is the offer to clear out the sensors the dashboard came with, and
 * to put them back afterwards.
 *
 * It is always there, because both directions are worth offering and a panel that
 * vanishes takes the way back with it. Which button it shows is decided by what
 * is in the table right now: remove while there is something to remove, load once
 * there is not. The delete stays the destructive one and keeps its confirmation.
 */
/**
 * backupPanel is the backup and restore panel.
 *
 * It is an administrator's panel and it is absent — not hidden — for everybody
 * else, the way the users panel is: a control whose request comes back 403 is
 * worse than no control, because it looks like a broken installation rather than
 * like a permission.
 */
function backupPanel() {
  if (!can('admin')) return '';
  return `
    <section class="panel" data-panel="backup" data-group="backup">
      <div class="panel-head">
        <h2>${esc(t('settings.backup'))}</h2>
        <p class="panel-hint">${esc(t('backup.hint'))}</p>
      </div>

      <div class="grid-2">
        <label class="field"><span>${esc(t('backup.scope'))}</span>
          <select id="backup-scope">
            <option value="config" selected>${esc(t('backup.scope.config'))}</option>
            <option value="history">${esc(t('backup.scope.history'))}</option>
          </select></label>

        <label class="field" id="backup-days-field"><span>${esc(t('backup.days'))}</span>
          <select id="backup-days">
            ${['7', '30', '90', 'all'].map((d) => `<option value="${d}"${d === '30' ? ' selected' : ''}>${esc(t(`backup.days.${d}`))}</option>`).join('')}
          </select></label>
      </div>

      <p class="muted small" id="backup-summary">${esc(t('backup.scope.configHint'))}</p>

      <div class="form-actions">
        <button class="btn btn-primary" data-action="backup-download">${esc(t('backup.download'))}</button>
        <button class="btn" data-action="backup-file">${esc(t('backup.restore'))}</button>
      </div>

      <h3 class="section-title">${esc(t('configExport.title'))}</h3>
      <p class="muted small">${esc(t('configExport.hint'))}</p>
      <div class="form-actions">
        <button class="btn" data-action="config-export">${esc(t('configExport.download'))}</button>
      </div>

      <!-- A plain file input, and the chosen file is sent as the body of one POST.
           Nothing is buffered in the browser: a backup can be a few megabytes, and
           the server streams it into the database rather than reading it whole. -->
      <input type="file" id="restore-file" accept="application/json,.json" hidden>

      <p class="muted small warn-note">${esc(t('backup.secretWarning'))}</p>
      <p class="muted small">${esc(t('backup.epochWarning'))}</p>
      <p class="form-error" id="backup-error"></p>
    </section>`;
}

function examplePanel() {
  const data = state.exampleData || { sensors: 0, results: 0, incidents: 0, sites: 0 };
  const has = data.sensors > 0;
  return `
    <section class="panel" id="examples-panel" data-panel="examples" data-group="monitor">
      <div class="panel-head">
        <h2>${esc(t('examples.title'))}</h2>
        <p class="panel-hint">${esc(t('examples.hint'))}</p>
      </div>
      ${has ? `
      <p class="muted small">${esc(t('examples.count', { sensors: data.sensors, results: data.results, incidents: data.incidents, sites: data.sites || 0 }))}</p>
      <div class="form-actions">
        <button class="btn btn-danger" data-action="drop-examples"${denied('admin')}>${esc(t('examples.remove'))}</button>
      </div>` : `
      <p class="muted small">${esc(t('examples.none'))}</p>
      <div class="form-actions">
        <button class="btn" data-action="restore-examples"${denied('admin')}>${esc(t('examples.restore'))}</button>
      </div>`}
    </section>`;
}

/**
 * sitesPanel lists the places sensors live, and edits them.
 *
 * A site is small enough to live in a table rather than on a screen of its own,
 * and it sits in Settings next to everything else that is configured once and
 * then forgotten. The count column is the one that matters: it is what turns
 * "delete" into a decision, because a site with sensors on it will be refused
 * and the refusal is easier to understand when the number was already on screen.
 */
function sitesPanel() {
  const sites = state.sites || [];
  const rows = sites.map((site) => `
    <tr data-name="${esc(site.name)}" data-origin="${esc(site.origin || '')}">
      <td><span class="site-kind-cell">
        <i class="legend-dot k-${esc(site.kind)}" aria-hidden="true"></i>
        ${iconUI.iconMarkup(site.icon, { size: 16 })}
        <b>${esc(site.name)}</b>
        ${site.origin === 'example' ? `<span class="pill badge-example" title="${esc(t('sites.exampleHint'))}">${esc(t('examples.badge'))}</span>` : ''}
      </span></td>
      <td>${esc(t(`sites.kind.${site.kind}`))}</td>
      <td class="coord">${site.lat !== undefined && site.lon !== undefined && site.lat !== null && site.lon !== null
        ? `${Number(site.lat).toFixed(4)}, ${Number(site.lon).toFixed(4)}`
        : `<span class="muted">${esc(t('common.none'))}</span>`}</td>
      <td class="num">${site.sensor_count || 0}</td>
      <td class="row-actions">
        <button class="btn" data-action="site-edit" data-name="${esc(site.name)}"${site.managed ? ` disabled title="${esc(t('sites.managed'))}"` : ''}>${esc(t('common.edit'))}</button>
        <button class="btn btn-danger" data-action="site-delete" data-name="${esc(site.name)}"${site.managed ? ` disabled title="${esc(t('sites.managed'))}"` : ''}>${esc(t('common.delete'))}</button>
      </td>
    </tr>`).join('');

  return `
    <section class="panel" id="sites-panel" data-panel="sites" data-group="monitor">
      <div class="panel-head">
        <h2>${esc(t('sites.title'))}</h2>
        <p class="panel-hint">${esc(t('sites.hint'))}</p>
      </div>
      ${sites.length ? `
      <div class="table-wrap">
        <table class="data-table sites-table">
          <thead><tr>
            <th>${esc(t('sites.name'))}</th>
            <th>${esc(t('sites.kind'))}</th>
            <th>${esc(t('sites.latitude'))} / ${esc(t('sites.longitude'))}</th>
            <th class="num">${esc(t('settings.sensors'))}</th>
            <th></th>
          </tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>` : emptyBlock(t('sites.emptyBody'))}
      <div class="form-actions">
        <button class="btn btn-primary" data-action="site-new"${denied('admin')}>${esc(t('sites.new'))}</button>
      </div>
    </section>`;
}

/**
 * groupsPanel lists the groups this installation has, and what their headings look
 * like.
 *
 * Every row is a word some sensor carries, and some of those rows have settings and
 * some have none — the two are drawn the same way, because a group with no settings
 * is a group, not an unfinished one. "New group" names one before a sensor carries
 * it, which is the same act the sensor form's picker offers; the row can then be
 * given a symbol and a colour before anything is put in it.
 */
function groupsPanel() {
  const groups = state.groups || [];
  const admin = can('admin');
  const rows = groups.map((group) => {
    const settings = iconUI.groupOf(groups, group.name);
    return `
    <tr data-name="${esc(group.name)}"${iconUI.colourAttr(settings)}${admin ? ' draggable="true"' : ''}>
      <td><span class="group-cell">
        ${admin ? `<span class="drag-grip" aria-hidden="true" title="${esc(t('groups.dragHint'))}">⠿</span>` : ''}
        <i class="group-dot" aria-hidden="true"></i>
        ${iconUI.groupIcon(settings)}
        <b>${esc(group.name)}</b>
      </span></td>
      <td class="num">${group.sensor_count || 0}</td>
      <td class="row-actions">
        <button class="btn" data-action="group-edit" data-name="${esc(group.name)}"${denied('admin')}>${esc(t('common.edit'))}</button>
        <button class="btn btn-danger" data-action="group-forget" data-name="${esc(group.name)}"${denied('admin')}>${esc(t('groups.forget'))}</button>
      </td>
    </tr>`;
  }).join('');

  return `
    <section class="panel" id="groups-panel" data-panel="groups" data-group="monitor">
      <div class="panel-head">
        <h2>${esc(t('groups.title'))}</h2>
        <p class="panel-hint">${esc(t('groups.hint'))}</p>
      </div>
      ${groups.length ? `
      <div class="table-wrap">
        <table class="data-table groups-table">
          <thead><tr>
            <th>${esc(t('sensor.group'))}</th>
            <th class="num">${esc(t('settings.sensors'))}</th>
            <th></th>
          </tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>` : emptyBlock(t('groups.empty'))}
      <div class="form-actions">
        <button class="btn btn-primary" data-action="group-new"${denied('admin')}>${esc(t('groups.new'))}</button>
      </div>
    </section>`;
}

/**
 * tagsPanel is groupsPanel's twin for tags. A tag is a word in a sensor's
 * comma-separated list, and the settings are what a word cannot say: the symbol,
 * the colour and the place in the order.
 */
function tagsPanel() {
  const tags = state.tags || [];
  const rows = tags.map((tag) => {
    const settings = iconUI.groupOf(tags, tag.name);
    return `
    <tr data-name="${esc(tag.name)}"${iconUI.colourAttr(settings)}>
      <td><span class="group-cell">
        <i class="group-dot" aria-hidden="true"></i>
        ${iconUI.groupIcon(settings)}
        <b>${esc(tag.name)}</b>
      </span></td>
      <td class="num">${tag.sensor_count || 0}</td>
      <td class="row-actions">
        <button class="btn" data-action="tag-edit" data-name="${esc(tag.name)}"${denied('admin')}>${esc(t('common.edit'))}</button>
        <button class="btn btn-danger" data-action="tag-forget" data-name="${esc(tag.name)}"${denied('admin')}>${esc(t('tags.forget'))}</button>
      </td>
    </tr>`;
  }).join('');

  return `
    <section class="panel" id="tags-panel" data-panel="tags" data-group="monitor">
      <div class="panel-head">
        <h2>${esc(t('tags.title'))}</h2>
        <p class="panel-hint">${esc(t('tags.hint'))}</p>
      </div>
      ${tags.length ? `
      <div class="table-wrap">
        <table class="data-table groups-table">
          <thead><tr>
            <th>${esc(t('sensor.tags'))}</th>
            <th class="num">${esc(t('settings.sensors'))}</th>
            <th></th>
          </tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>` : emptyBlock(t('tags.emptyBody'))}
      <div class="form-actions">
        <button class="btn btn-primary" data-action="tag-new"${denied('admin')}>${esc(t('tags.new'))}</button>
      </div>
    </section>`;
}

/**
 * metricsPanel manages the read-only token a Prometheus scraper uses.
 *
 * The token is shown rather than hidden: it is a credential, but one that can do
 * nothing except read the numbers on /metrics, and a scraper's configuration is
 * somewhere the person setting it up has to copy it to.
 */
function metricsPanel() {
  if (!can('admin')) return '';
  const token = state.metricsToken || '';
  const endpoint = `${location.origin}/metrics`;
  return `
    <section class="panel" id="metrics-panel" data-panel="metrics" data-group="monitor">
      <div class="panel-head">
        <h2>${esc(t('metrics.title'))}</h2>
        <p class="panel-hint">${esc(t('metrics.hint'))}</p>
      </div>
      ${token ? `
      <label class="field"><span>${esc(t('metrics.token'))}</span>
        <input id="metrics-token" type="text" readonly value="${esc(token)}" spellcheck="false"></label>
      <p class="muted small">${esc(t('metrics.example', { url: endpoint }))}</p>
      <div class="form-actions">
        <button class="btn" data-action="metrics-copy">${esc(t('settings.copy'))}</button>
        <button class="btn" data-action="metrics-rotate">${esc(t('metrics.rotate'))}</button>
        <button class="btn btn-danger" data-action="metrics-disable">${esc(t('metrics.disable'))}</button>
      </div>` : `
      <p class="muted small">${esc(t('metrics.off'))}</p>
      <div class="form-actions">
        <button class="btn btn-primary" data-action="metrics-rotate">${esc(t('metrics.enable'))}</button>
      </div>`}
      <p class="form-error" id="metrics-error"></p>
    </section>`;
}

async function rotateMetricsToken() {
  if (!confirmDialog(t('metrics.rotateConfirm'))) return;
  const data = await api('/api/metrics-token', { method: 'PUT' });
  state.metricsToken = data.token || '';
  await renderSettings();
  toast(t('metrics.rotated'));
}

async function disableMetricsToken() {
  if (!confirmDialog(t('metrics.disableConfirm'))) return;
  await api('/api/metrics-token', { method: 'DELETE' });
  state.metricsToken = '';
  await renderSettings();
  toast(t('metrics.disabled'));
}

// --- dragging the group order ----------------------------------------------

let draggedGroup = '';

// The rows in Settings and the headings on the dashboard are the same kind of
// item: both carry a group's name, and both can be dragged into a new order. One
// set of handlers serves them, so the two places cannot drift apart.
const GROUP_DRAG_ITEMS = '#groups-panel tbody tr[data-name], #groups [data-group-heading]';

function groupDragItem(node) {
  return node.closest(GROUP_DRAG_ITEMS);
}

function groupItemName(item) {
  return item?.dataset.name || item?.dataset.groupHeading || '';
}

function clearGroupDrag() {
  draggedGroup = '';
  for (const item of $$(GROUP_DRAG_ITEMS)) item.classList.remove('dragging', 'drag-over');
}

function onGroupDragStart(event) {
  const item = groupDragItem(event.target);
  if (!item || !can('admin')) return;
  draggedGroup = groupItemName(item);
  item.classList.add('dragging');
  if (event.dataTransfer) {
    event.dataTransfer.setData('text/plain', draggedGroup);
    event.dataTransfer.effectAllowed = 'move';
  }
}

function onGroupDragOver(event) {
  const item = groupDragItem(event.target);
  if (!item || !draggedGroup || groupItemName(item) === draggedGroup) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move';
  for (const other of $$(GROUP_DRAG_ITEMS)) other.classList.remove('drag-over');
  item.classList.add('drag-over');
}

async function onGroupDrop(event) {
  const target = groupDragItem(event.target);
  const targetName = groupItemName(target);
  if (!target || !draggedGroup || targetName === draggedGroup) return;
  event.preventDefault();

  // The full order, not just the rows on screen: the dashboard shows only the
  // groups that have sensors, and a settings-only group must keep its place
  // rather than be handed a colliding number.
  const names = (state.groups || []).map((g) => g.name);
  for (const item of $$(GROUP_DRAG_ITEMS)) {
    const name = groupItemName(item);
    if (name && !names.includes(name)) names.push(name);
  }
  const from = names.indexOf(draggedGroup);
  const to = names.indexOf(targetName);
  if (from < 0 || to < 0) return;
  const moved = names[from];
  names.splice(from, 1);
  // Drop before the row or heading it was released on, which is where the line is.
  names.splice(from < to ? to - 1 : to, 0, moved);
  clearGroupDrag();
  await saveGroupOrder(names);
}

/**
 * saveGroupOrder writes the whole order, not just the moved row.
 *
 * A gap or a duplicate in the numbers would silently fall back to the alphabet
 * for the rows it leaves at zero, so every row gets its index. Groups without a
 * settings row get one: the order is now something somebody chose for them too.
 */
async function saveGroupOrder(names) {
  try {
    await Promise.all(names.map((name, index) =>
      api(`/api/groups/${encodeURIComponent(name)}`, {
        method: 'PUT',
        body: { sort_order: index },
      })));
    await afterLabelChange('group');
    toast(t('groups.reordered'));
  } catch (err) {
    toast(err.message, 'error');
    await renderSettings();
  }
}

/**
 * mapPanel is the tile source, and the sentence that has to be read with it.
 *
 * Empty is the default and it means "off" rather than "not set": the map draws the
 * coastline that shipped in the binary and no request leaves the machine. So the note
 * under the fields is not a footnote — it says what entering an address here costs,
 * and that is the decision this panel exists to make deliberate.
 */
function mapPanel() {
  const map = (state.settings && state.settings.map) || {};
  const admin = can('admin');
  return `
    <section class="panel" id="map-panel" data-panel="map" data-group="look">
      <div class="panel-head">
        <h2>${esc(t('settings.map'))}</h2>
        <p class="panel-hint">${esc(t('settings.mapHint'))}</p>
      </div>
      <label class="field"><span>${esc(t('settings.tileUrl'))}</span>
        <input name="tile_url" value="${esc(map.tile_url || '')}"
               placeholder="${esc(map.default_tile_url || '')}"${admin ? '' : ' disabled'}></label>
      <p class="muted small">${esc(t('settings.tileHint'))}</p>
      <label class="field"><span>${esc(t('settings.tileAttribution'))}</span>
        <input name="attribution" value="${esc(map.attribution || '')}"${admin ? '' : ' disabled'}></label>
      <label class="field check"><input type="checkbox" name="borders"
             ${map.borders === 'off' ? '' : 'checked'}${admin ? '' : ' disabled'}>
        <span>${esc(t('settings.mapBorders'))}</span></label>
      <p class="muted small">${esc(t('settings.mapBordersHint'))}</p>
      <p class="muted small">${esc(t('settings.tilePrivacy'))}</p>
      <div class="form-actions">
        <button class="btn btn-primary" data-action="map-save"${denied('admin')}>${esc(t('common.save'))}</button>
      </div>
    </section>`;
}

/**
 * openSiteDialog is the small dialog behind "New site", "Edit", and the sensor
 * form's "New site…" entry.
 *
 * The two callers want different things after it closes, so it takes them as
 * options rather than guessing: `onSaved` gets the name of a place that was made,
 * `onClosed` runs when the window went away without one — which is how the sensor
 * form knows to put its picker back to "No site". `hint` is the line that says
 * where the window came from, because a dialog that appears over another one
 * without saying why reads like a mistake.
 */
function openSiteDialog(existing, { onSaved, onClosed, hint } = {}) {
  const site = existing || { name: '', kind: 'local', lat: null, lon: null, icon: '' };
  const coord = (value) => (value === null || value === undefined ? '' : String(value));
  const form = $('#site-form');
  const dialog = $('#modal-site');
  delete form.dataset.saved;
  siteModal.open({
    title: existing ? t('sites.edit') : t('sites.new'),
    mode: 'site',
    id: existing ? existing.name : '',
    body: `
      ${hint ? `<p class="muted small sheet-note">${esc(hint)}</p>` : ''}
      <label class="field adv-hide"><span>${esc(t('sites.name'))}</span>
        <input name="site_name" value="${esc(site.name)}" maxlength="64" required></label>
      <p class="muted small adv-hide">${esc(t('sites.nameHint'))}</p>
      ${choiceGroup('kind', site.kind, [
        { value: 'local', label: t('sites.kind.local') },
        { value: 'external', label: t('sites.kind.external') },
      ], esc(t('sites.kind')))}
      <div class="field adv-hide"><span>${withHelp(t('icons.label'), 'help.sites.icon')}</span>
        ${iconUI.iconFieldMarkup(site.icon || '')}</div>
      <div class="grid-2">
        <label class="field"><span>${esc(t('sites.latitude'))}</span>
          <input name="lat" type="number" step="any" min="-90" max="90" value="${esc(coord(site.lat))}"></label>
        <label class="field"><span>${esc(t('sites.longitude'))}</span>
          <input name="lon" type="number" step="any" min="-180" max="180" value="${esc(coord(site.lon))}"></label>
      </div>
      <p class="muted small">${esc(t('sites.coordsHint'))}</p>`,
  });
  // The dialog element outlives every opening, so this listener is added per
  // opening and taken off again by the first close: a listener left behind would
  // run once for every window that has been closed before.
  const settled = () => {
    dialog.removeEventListener('close', settled);
    const name = form.dataset.saved;
    if (name) onSaved?.(name);
    else onClosed?.();
  };
  dialog.addEventListener('close', settled);
}

/**
 * readSiteForm turns the dialog into what the API takes.
 *
 * Empty coordinates are left out entirely rather than sent as zero: the server
 * reads a missing pair as "not placed" and 0/0 as the Gulf of Guinea, and those
 * have to stay different things. Half a pair is sent as it is so that the server
 * refuses it and says why, instead of this quietly dropping the number somebody
 * typed.
 */
function readSiteForm(form) {
  const value = (name) => form.elements.namedItem(name).value.trim();
  const body = {
    name: value('site_name'),
    kind: value('kind'),
    icon: iconUI.readIconField(form),
  };
  const lat = value('lat');
  const lon = value('lon');
  if (lat !== '' || lon !== '') {
    body.lat = Number(lat);
    body.lon = Number(lon);
  }
  return body;
}

async function submitSite(form) {
  const previous = form.dataset.id;
  const body = readSiteForm(form);
  await api(previous ? `/api/sites/${encodeURIComponent(previous)}` : '/api/sites', {
    method: previous ? 'PUT' : 'POST',
    body,
  });
  // What the window's own close listener reads: the name of the place this
  // dialog just saved, so a caller can put it to use after the window is gone.
  form.dataset.saved = body.name;
  await afterSiteChange();
  toast(previous ? t('sites.updated') : t('sites.created'));
}

/**
 * removeSite deletes a site, and turns the refusal into the next question.
 *
 * The server answers 409 with the number of sensors still on the site, because
 * deleting it is not the only way out — leaving those sensors without a place is
 * often what was meant, and it is one more click rather than a trip to each
 * sensor to move it first.
 */
async function removeSite(site) {
  const count = site.sensor_count || 0;
  if (!confirmDialog(t(count ? 'sites.deleteConfirmUnassign' : 'sites.deleteConfirm',
    { name: site.name, count }))) return;
  try {
    await api(`/api/sites/${encodeURIComponent(site.name)}`, { method: 'DELETE' });
    await afterSiteChange();
    toast(t('sites.deleted'));
  } catch (err) {
    if (err.status !== 409) {
      toast(err.message, 'error');
      return;
    }
    if (!confirmDialog(t('sites.inUse', { count }))) return;
    try {
      await api(`/api/sites/${encodeURIComponent(site.name)}?unassign=1`, { method: 'DELETE' });
      await afterSiteChange();
      toast(t('sites.deleted'));
    } catch (again) {
      toast(again.message, 'error');
    }
  }
}

/**
 * afterSiteChange redraws everything a site can appear in.
 *
 * The dashboard draws the tree from the sites it was last given, so changing one
 * has to reach the summary as well as the panel — otherwise the tree keeps its
 * old shape until the next refresh and looks like the edit did not take.
 */
async function afterSiteChange() {
  await loadSites(true);
  await refresh({ background: true });
  await renderSettings();
}

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

/**
 * loadGroups reads the groups and what is known about them.
 *
 * The dashboard gets them with the summary; this is for the settings page opened
 * directly, where there is no summary behind it — a bookmark to Settings, or a
 * reload while the tab is already chosen.
 */
async function loadGroups(force = false) {
  if (state.groups.length && !force) return state.groups;
  const data = await api('/api/groups');
  state.groups = data.groups || [];
  // The colours come with the list, and are kept so that the editor offers exactly
  // what the server would accept. ACCENTS is what a build that has not heard from
  // this route yet falls back to, and the two are the same six names.
  if (data.accents && data.accents.length) state.accents = data.accents;
  return state.groups;
}

/**
 * loadTags reads the tag settings. Like groups, the list is the server's and is a
 * superset of the words on the sensors: a tag with no settings is still a tag.
 */
async function loadTags(force = false) {
  if (state.tags.length && !force) return state.tags;
  const data = await api('/api/tags');
  state.tags = data.tags || [];
  if (data.accents && data.accents.length) state.accents = data.accents;
  return state.tags;
}

/**
 * loadMetricsToken reads the read-only token /metrics uses. It is loaded with the
 * settings page rather than on the first render of the panel, because the panel
 * decides whether to show a token or the way to make one.
 */
async function loadMetricsToken() {
  const data = await api('/api/metrics-token');
  state.metricsToken = data.token || '';
  return state.metricsToken;
}

let labelSaved = null;

/**
 * openLabelEditor edits what is about a group or a tag: its symbol, its colour and
 * where it stands in the order.
 *
 * One dialog for both because the decision is the same shape; the titles, the words
 * and the endpoint differ. In creating mode the name is a field, because a group or
 * a tag can be named before the first sensor carries it — that is what a "New…"
 * entry in the picker means.
 *
 * Editing keeps the name printed: a group or a tag is a word on sensors, so
 * renaming it here would mean rewriting every sensor that carries it, which is
 * editing sensors and not what this window is for.
 */
function openLabelEditor(kind, name, settings, { creating = false, initialName = '', onSaved = null } = {}) {
  const isTag = kind === 'tag';
  const row = settings || { name, icon: '', colour: '', sort_order: 0 };
  labelSaved = onSaved;
  groupModal.open({
    title: creating
      ? t(isTag ? 'tags.new' : 'groups.new')
      : t(isTag ? 'tags.editorTitle' : 'groups.editorTitle', { name }),
    mode: kind,
    id: creating ? '' : name,
    body: `
      <p class="muted small sheet-note">${esc(creating
        ? t(isTag ? 'tags.newHint' : 'groups.newHint')
        : t(isTag ? 'tags.editorNote' : 'groups.editorNote', { count: row.sensor_count || 0 }))}</p>
      ${creating ? `
      <label class="field"><span>${esc(t(isTag ? 'tags.name' : 'groups.name'))}</span>
        <input name="label_name" type="text" required maxlength="64" autocomplete="off"
               value="${esc(initialName)}"></label>` : ''}
      <div class="field"><span>${withHelp(t('icons.label'), isTag ? 'help.tags.icon' : 'help.groups.icon')}</span>
        ${iconUI.iconFieldMarkup(row.icon || '')}</div>
      ${choiceGroup('colour', row.colour || '', [
        { value: '', label: t('groups.colourNone') },
        ...(state.accents || ACCENTS).map((c) => ({ value: c, label: t(`settings.accent.${c}`) })),
      ], withHelp(t(isTag ? 'tags.colour' : 'groups.colour'),
        isTag ? 'help.tags.colour' : 'help.groups.colour'))}
      <label class="field"><span>${esc(t(isTag ? 'tags.order' : 'groups.order'))}</span>
        <input name="sort_order" type="number" value="${Number(row.sort_order) || 0}"></label>
      <p class="muted small">${esc(t(isTag ? 'tags.orderHint' : 'groups.orderHint'))}</p>`,
  });
  // The kind and the mode have nowhere else to live: the sheet carries the title
  // and the id, and the form needs to know which endpoint to write.
  const form = $('#group-form');
  form.dataset.kind = kind;
  form.dataset.creating = creating ? '1' : '0';
}

/** readLabelForm turns the editor into what the API takes. */
function readLabelForm(form) {
  const body = {
    icon: iconUI.readIconField(form),
    colour: form.elements.colour.value,
    sort_order: Number(form.elements.sort_order.value) || 0,
  };
  if (form.dataset.creating === '1') body.name = form.elements.label_name.value.trim();
  return body;
}

async function submitLabel(form) {
  const kind = form.dataset.kind === 'tag' ? 'tag' : 'group';
  const creating = form.dataset.creating === '1';
  const body = readLabelForm(form);
  const name = creating ? body.name : form.dataset.id;
  delete body.name;
  if (!name) {
    throw new Error(t(kind === 'tag' ? 'tags.nameRequired' : 'groups.nameRequired'));
  }
  await api(`/api/${kind}s/${encodeURIComponent(name)}`, { method: 'PUT', body });
  await afterLabelChange(kind);
  toast(t(kind === 'tag' ? 'tags.saved' : 'groups.saved'));
  const done = labelSaved;
  labelSaved = null;
  if (done) done(name);
}

/**
 * forgetLabel removes what was said about a group or a tag and leaves the word on
 * the sensors. The question says so in as many words, because the button sits in a
 * table of rows that look like things and "delete" next to a row reads like deleting
 * the row. What goes is the symbol, the colour and the place in the order.
 */
async function forgetLabel(kind, name, row) {
  const count = (row && row.sensor_count) || 0;
  const confirmKey = kind === 'tag' ? 'tags.forgetConfirm' : 'groups.forgetConfirm';
  if (!confirmDialog(t(confirmKey, { name, count }))) return;
  await api(`/api/${kind}s/${encodeURIComponent(name)}`, { method: 'DELETE' });
  await afterLabelChange(kind);
  toast(t(kind === 'tag' ? 'tags.forgotten' : 'groups.forgotten'));
}

let groupDeleteName = '';

/** openGroupDelete asks what should happen to a group's sensors. */
async function openGroupDelete(name) {
  groupDeleteName = name;
  let sensors = [];
  try {
    sensors = (await api('/api/sensors')).filter((sensor) => sensor.group === name);
  } catch (err) {
    toast(err.message, 'error');
    return;
  }
  const managed = sensors.filter((sensor) => sensor.managed).length;
  const rows = sensors.map((sensor) => `
    <label class="group-delete-row">
      <input type="checkbox" data-delete-sensor="${sensor.id}"
             ${sensor.managed ? 'disabled' : 'checked'}>
      <span>${esc(sensor.name)}</span>
      ${sensor.managed ? `<span class="pill">${esc(t('groups.deleteManaged'))}</span>` : ''}
    </label>`).join('');
  $('#group-delete-title').textContent = t('groups.deleteTitle', { name });
  $('#group-delete-body').innerHTML = `
    <p class="muted small">${esc(t('groups.deleteHint'))}</p>
    <fieldset class="field">
      <legend>${esc(t('settings.sensors'))}</legend>
      <div class="choice-group">
        <label class="chip chip-choice"><input type="radio" name="group-delete-mode" value="keep" checked><span>${esc(t('groups.deleteKeep'))}</span></label>
        <label class="chip chip-choice"><input type="radio" name="group-delete-mode" value="selected"${sensors.length ? '' : ' disabled'}><span>${esc(t('groups.deleteSelected'))}</span></label>
        <label class="chip chip-choice"><input type="radio" name="group-delete-mode" value="all"${sensors.length ? '' : ' disabled'}><span>${esc(t('groups.deleteAll', { count: sensors.length }))}</span></label>
      </div>
    </fieldset>
    ${sensors.length ? `<div class="group-delete-list" id="group-delete-list" hidden>${rows}</div>` : `<p class="muted small">${esc(t('groups.deleteNoSensors'))}</p>`}
    ${managed ? `<p class="muted small">${esc(t('groups.deleteManaged'))}: ${managed}</p>` : ''}`;
  $('#group-delete-error').textContent = '';
  $('#group-delete-confirm').textContent = t('groups.deleteConfirm');
  $('#modal-group-delete').showModal();
}

async function deleteGroupConfirmed() {
  if (!groupDeleteName) return;
  const mode = document.querySelector('input[name="group-delete-mode"]:checked')?.value || 'keep';
  const ids = mode === 'selected'
    ? $$('#group-delete-list [data-delete-sensor]:checked').map((el) => Number(el.dataset.deleteSensor))
    : [];
  if (mode === 'selected' && ids.length === 0) {
    $('#group-delete-error').textContent = t('groups.deleteSelectedNone');
    return;
  }
  try {
    await api(`/api/groups/${encodeURIComponent(groupDeleteName)}/delete`, {
      method: 'POST', body: { mode, sensor_ids: ids },
    });
    $('#modal-group-delete').close();
    await afterLabelChange('group');
    toast(t('groups.deleted'));
  } catch (err) {
    $('#group-delete-error').textContent = err.message;
  }
}

document.addEventListener('change', (event) => {
  const mode = event.target.closest('input[name="group-delete-mode"]');
  if (!mode) return;
  const list = $('#group-delete-list');
  if (list) list.hidden = mode.value !== 'selected';
});
$('#group-delete-confirm').addEventListener('click', deleteGroupConfirmed);

/**
 * afterLabelChange redraws every place a group or a tag appears.
 *
 * The headings are drawn from the summary the dashboard was last given, so a change
 * to a symbol or colour has to reach the summary as well as this panel — otherwise
 * the tree goes on drawing the old heading and the edit looks like it did not take.
 */
async function afterLabelChange(kind) {
  if (kind === 'tag') await loadTags(true);
  else await loadGroups(true);
  await refresh({ background: true });
  await renderSettings();
}

// ---------------------------------------------------------------------------
// maintenance windows
// ---------------------------------------------------------------------------

/**
 * WEEKDAYS is Monday-first, because that is the order a week is written in most of
 * Europe and the order the checkboxes are laid out in. The names come from Intl
 * rather than from the catalogue: a weekday is a word every language already has in
 * its own calendar, and twenty-eight translations of "Mon" would be twenty-eight
 * chances to disagree with the date shown next to them.
 *
 * 2026-10-05 was a Monday.
 */
const WEEKDAYS = [...Array(7)].map((_, i) => ({
  // Sunday is 0 in both Go and JavaScript, and last in this list.
  value: (i + 1) % 7,
  name: new Intl.DateTimeFormat(locale(), { weekday: 'short', timeZone: 'UTC' })
    .format(new Date(Date.UTC(2026, 9, 5 + i))),
}));

/** HH:MM into minutes past midnight, and back. */
function minutesOf(value) {
  const [h, m] = String(value).split(':').map(Number);
  return (h || 0) * 60 + (m || 0);
}

function clockOf(minutes) {
  return `${String(Math.floor(minutes / 60)).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`;
}

/**
 * windowWhen writes a window the way somebody would say it out loud.
 *
 * A weekly one is a time of day and a list of days; a one-off is two instants. They
 * are different sentences because they are different things, and a table that showed
 * both as a pair of timestamps would make the weekly one look like it expires.
 */
function windowWhen(w) {
  if (w.kind === 'weekly') {
    const days = (w.weekdays || []).slice().sort((a, b) => ((a + 6) % 7) - ((b + 6) % 7))
      .map((d) => (WEEKDAYS.find((x) => x.value === d) || {}).name || d);
    const span = `${clockOf(w.start_min)}–${clockOf(w.end_min)}`;
    if (days.length === 7) return t('windows.everyDay', { span });
    return `${days.join(', ')} ${span}`;
  }
  if (!w.starts_at || !w.ends_at) return '—';
  return `${C.fmtDateTime(w.starts_at)} – ${C.fmtDateTime(w.ends_at)}`;
}

/** windowWhat names what the window is for, in the words the form used. */
function windowWhat(w) {
  if (w.sensor_id) {
    const c = (state.sensors || []).find((x) => x.id === w.sensor_id);
    return c ? c.name : t('windows.sensorGone', { id: w.sensor_id });
  }
  return t('windows.groupName', { group: w.group });
}

/** windowScopeValue is what the form's select carries, and what it is read back as. */
function windowScopeValue(w) {
  return w.sensor_id ? `sensor:${w.sensor_id}` : `group:${w.group}`;
}

/**
 * windowFormMarkup builds the editor.
 *
 * One form for both shapes, with the half that does not apply hidden — the same
 * form, because switching a window from weekly to one-off should be one change of
 * a select and not a second dialog. The blocks are marked with data-kind and
 * toggled by a delegated listener rather than by an inline handler, so the markup
 * stays markup.
 *
 * The time zone is the browser's, filled in when the form is opened and carried
 * along invisibly. It is the one fact this process cannot work out for itself: a
 * server in a container is UTC, and "Sunday at three" is not.
 */
function windowFormMarkup(w) {
  const sensors = state.sensors || [];
  const groups = [...new Set(sensors.map((c) => c.group).filter(Boolean))].sort();
  const options = [
    `<optgroup label="${esc(t('windows.scopeSensors'))}">` +
      sensors.map((c) => `<option value="sensor:${c.id}">${esc(c.name)}</option>`).join('') +
      '</optgroup>',
    groups.length
      ? `<optgroup label="${esc(t('windows.scopeGroups'))}">` +
        groups.map((g) => `<option value="group:${esc(g)}">${esc(g)}</option>`).join('') +
        '</optgroup>'
      : '',
  ].join('');

  const kind = w.kind || 'once';
  const chosen = new Set(w.weekdays || []);
  const days = WEEKDAYS.map((d) => `
    <label class="chip-check"><input type="checkbox" name="window_day_${d.value}"
      ${chosen.has(d.value) ? 'checked' : ''}> <span>${esc(d.name)}</span></label>`).join('');

  // datetime-local wants "YYYY-MM-DDTHH:MM" in the browser's own zone, which is what
  // the Date is read back as. An instant from the server is therefore rendered by
  // shifting it into local time first, and read back the same way round.
  const local = (iso) => {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };

  return `
    <label class="field"><span>${esc(t('windows.scope'))}</span>
      <select name="window_scope">${options}</select>
    </label>

    <label class="field"><span>${esc(t('windows.kind'))}</span>
      <select name="window_kind">
        <option value="once"${kind === 'once' ? ' selected' : ''}>${esc(t('windows.kindOnce'))}</option>
        <option value="weekly"${kind === 'weekly' ? ' selected' : ''}>${esc(t('windows.kindWeekly'))}</option>
      </select>
    </label>

    <div data-kind="once"${kind === 'once' ? '' : ' hidden'}>
      <label class="field"><span>${esc(t('windows.from'))}</span>
        <input type="datetime-local" name="window_from" value="${esc(local(w.starts_at))}">
      </label>
      <label class="field"><span>${esc(t('windows.to'))}</span>
        <input type="datetime-local" name="window_to" value="${esc(local(w.ends_at))}">
      </label>
    </div>

    <div data-kind="weekly"${kind === 'weekly' ? '' : ' hidden'}>
      <div class="field"><span>${esc(t('windows.days'))}</span>
        <div class="chips">${days}</div>
      </div>
      <label class="field"><span>${esc(t('windows.from'))}</span>
        <input type="time" name="window_start" value="${esc(clockOf(w.start_min || 0))}">
      </label>
      <label class="field"><span>${esc(t('windows.to'))}</span>
        <input type="time" name="window_end" value="${esc(clockOf(w.end_min || 0))}">
      </label>
    </div>

    <label class="field"><span>${esc(t('windows.note'))}</span>
      <input type="text" name="window_note" maxlength="200" value="${esc(w.note || '')}"
             placeholder="${esc(t('windows.notePlaceholder'))}">
    </label>

    <label class="check"><input type="checkbox" name="window_enabled"
      ${w.enabled === false ? '' : 'checked'}> <span>${esc(t('windows.enabled'))}</span></label>
    <p class="panel-hint small">${esc(t('windows.enabledHint'))}</p>`;
}

/**
 * maintenancePanel is the list of periods during which downtime is expected.
 *
 * It sits in the Monitoring category beside the sites, because that is the same kind
 * of thing: something an administrator decides in advance about the installation,
 * rather than something anybody does during an outage.
 *
 * A window that is running right now is marked. It is the only place the dashboard
 * says so outside a card, and it is what somebody checks when a sensor goes quiet and
 * they want to know whether they arranged it.
 */
function maintenancePanel() {
  const windows = state.windows || [];
  const now = Date.now();
  const running = (w) => (w.sensor_id
    ? (state.sensors || []).some((c) => c.id === w.sensor_id && c.excuse === 'maintenance')
    : (state.sensors || []).some((c) => c.group === w.group && c.excuse === 'maintenance'));

  const rows = windows.map((w) => `
    <tr data-id="${w.id}">
      <td><b>${esc(windowWhat(w))}</b></td>
      <td>${esc(windowWhen(w))}</td>
      <td>${w.note ? esc(w.note) : `<span class="muted">${esc(t('common.none'))}</span>`}</td>
      <td>${w.enabled
        ? (running(w)
          ? `<span class="pill pill-down">${esc(t('windows.running'))}</span>`
          : `<span class="muted small">${esc(t('windows.waiting'))}</span>`)
        : `<span class="muted small">${esc(t('windows.off'))}</span>`}</td>
      <td class="row-actions">
        <button class="btn" data-action="window-edit" data-id="${w.id}"${w.managed ? ` disabled title="${esc(t('windows.managed'))}"` : ''}>${esc(t('common.edit'))}</button>
        <button class="btn btn-danger" data-action="window-delete" data-id="${w.id}"${w.managed ? ` disabled title="${esc(t('windows.managed'))}"` : ''}>${esc(t('common.delete'))}</button>
      </td>
    </tr>`).join('');

  return `
    <section class="panel" id="windows-panel" data-panel="maintenance" data-group="monitor">
      <div class="panel-head">
        <h2>${esc(t('windows.title'))}</h2>
        <p class="panel-hint">${esc(t('windows.hint'))}</p>
      </div>
      ${windows.length ? `
      <div class="table-wrap">
        <table class="data-table windows-table">
          <thead><tr>
            <th>${esc(t('windows.what'))}</th>
            <th>${esc(t('windows.when'))}</th>
            <th>${esc(t('windows.note'))}</th>
            <th>${esc(t('windows.state'))}</th>
            <th></th>
          </tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>` : emptyBlock(t('windows.emptyBody'))}
      <div class="form-actions">
        <button class="btn btn-primary" data-action="window-new"${denied('admin')}>${esc(t('windows.new'))}</button>
      </div>
    </section>`;
}

/**
 * windowModal is the window editor, and it is the third dialog on purpose. Its own
 * element means its own form and its own error line, so the sensor form underneath
 * keeps every value it was holding — see the comment on `sheet`.
 */
const windowModal = sheet({
  dialog: '#modal-window',
  form: '#window-form',
  title: '#window-title',
  body: '#window-body',
  error: '#window-error',
  save: '#window-save',
  cancel: '#window-cancel',
  x: '#window-x',
});

// The kind select switches which half of the form is on screen. Delegated on the
// body, so it survives the body being rebuilt every time the dialog opens.
$('#window-body').addEventListener('change', (event) => {
  if (event.target.name !== 'window_kind') return;
  for (const part of $$('#window-body [data-kind]')) {
    part.hidden = part.dataset.kind !== event.target.value;
  }
});

/** openWindowEditor opens the editor on an existing window or on a new one. */
function openWindowEditor(w) {
  const blank = {
    kind: 'once',
    enabled: true,
    weekdays: [],
    start_min: 3 * 60,
    end_min: 4 * 60,
  };
  const window_ = w || blank;
  windowModal.open({
    title: window_ ? t('windows.editTitle') : t('windows.newTitle'),
    body: windowFormMarkup(window_),
    mode: window_ ? 'window-edit' : 'window-new',
    id: w ? String(w.id) : '',
    saveLabel: t('common.save'),
  });
  const scope = $('#window-body').querySelector('[name="window_scope"]');
  if (scope) {
    // Preselect what the editor was opened for, and on a new one the first sensor —
    // a window for nobody is the one thing the server refuses, so a form that opened
    // with nothing chosen would greet every user with an error on the first Save.
    scope.value = w ? windowScopeValue(w) : scope.options[0]?.value || '';
  }
}

/**
 * readWindowForm turns the dialog into what the API takes.
 *
 * The two shapes are read from the same fields the markup showed, and only the half
 * the kind selected is sent. That is the point of sending exactly one shape: a body
 * carrying both would leave the server guessing which one was meant, and the answer
 * would be a window whose meaning depended on which half won.
 */
function readWindowForm(form) {
  const value = (name) => (form.elements.namedItem(name)?.value || '').trim();
  const scope = value('window_scope');
  const body = {
    note: value('window_note'),
    enabled: form.elements.namedItem('window_enabled').checked,
    kind: value('window_kind'),
  };
  if (scope.startsWith('sensor:')) body.sensor_id = Number(scope.slice('sensor:'.length));
  else body.group = scope.slice('group:'.length);

  // The browser's zone, which is what "3am on Sunday" means to the person typing it.
  // A container is UTC and has no way to know better.
  body.timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';

  if (body.kind === 'weekly') {
    body.weekdays = WEEKDAYS
      .filter((d) => form.elements.namedItem(`window_day_${d.value}`)?.checked)
      .map((d) => d.value);
    body.start_min = minutesOf(value('window_start'));
    body.end_min = minutesOf(value('window_end') || '24:00');
    // Midnight at the end means the end of the day, which is 1440 and not 0. A form
    // that sent 0 would be refused as "all 24 hours", and the refusal would be right
    // about the number and wrong about what was meant.
    if (body.end_min === 0) body.end_min = 1440;
  } else {
    // datetime-local is read as local time by the Date constructor, which is what was
    // typed; toISOString() then says which instant that was.
    body.starts_at = value('window_from') ? new Date(value('window_from')).toISOString() : '';
    body.ends_at = value('window_to') ? new Date(value('window_to')).toISOString() : '';
  }
  return body;
}

async function submitWindow(form) {
  const previous = form.dataset.id;
  const body = readWindowForm(form);
  await api(previous ? `/api/maintenance/${previous}` : '/api/maintenance', {
    method: previous ? 'PUT' : 'POST',
    body,
  });
  form.dataset.saved = '1';
  await afterWindowChange();
  toast(previous ? t('windows.updated') : t('windows.created'));
}

async function removeWindow(w) {
  if (!confirmDialog(t('windows.deleteConfirm', { what: windowWhat(w) }))) return;
  try {
    await api(`/api/maintenance/${w.id}`, { method: 'DELETE' });
    await afterWindowChange();
    toast(t('windows.deleted'));
  } catch (err) {
    toast(err.message, 'error');
  }
}

/**
 * afterWindowChange redraws everything a window can appear in.
 *
 * The cards are on that list: a window that is running changes what every sensor it
 * covers says about itself, so the summary has to be re-read rather than only the
 * panel redrawn.
 */
async function afterWindowChange() {
  await loadWindows(true);
  await refresh({ background: true });
  await renderSettings();
}

async function saveMap() {
  const panel = $('#map-panel');
  const value = (name) => panel.querySelector(`[name="${name}"]`).value.trim();
  // .checked, not .value: a checkbox's value is the word "on" whatever its state, so
  // reading it would turn the layer off every time somebody pressed Save.
  const borders = panel.querySelector('[name="borders"]');
  await api('/api/map', {
    method: 'PUT',
    body: {
      tile_url: value('tile_url'),
      attribution: value('attribution'),
      borders: borders && borders.checked ? 'on' : 'off',
    },
  });
  await loadSettings(true);
  await renderSettings();
  toast(t('settings.mapSaved'));
}

// The shape of the settings page: five categories, and which panels sit under
// each. The keys are the `data-group` the panels carry and the `data-category`
// the tabs carry, so that showing one category is nothing but an attribute
// lookup — see showSettingsCategory.
const SETTINGS_GROUPS = [
  { key: 'look', panels: ['appearance', 'map'] },
  // Its own category rather than a corner of Monitoring, which is where the header's
  // arrangement used to sit: what the top of the dashboard shows is not what is being
  // monitored, and Monitoring had grown to four panels.
  { key: 'widgets', panels: ['widgets'] },
  { key: 'monitor', panels: ['dashboard', 'sites', 'groups', 'tags', 'metrics', 'maintenance', 'examples'] },
  { key: 'access', panels: ['access', 'users'] },
  { key: 'backup', panels: ['backup'] },
  { key: 'help', panels: ['help', 'diagnostics', 'about'] },
];

// One small drawing per category, drawn the way the layout switcher draws its
// five: inline SVG, no icon font and no image. A settings page of ten identical
// blocks is a page you scroll through rather than read, and the mark is what
// makes the one you want findable at a glance.
const SETTINGS_ICONS = {
  look: '<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M8 2a6 6 0 0 1 0 12Z" fill="currentColor"/></svg>',
  monitor: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M1.5 8h3l2-4.5 3 9 2-4.5h3"/></svg>',
  widgets: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><rect x="1.6" y="1.6" width="5.4" height="5.4" rx="1"/><rect x="9" y="1.6" width="5.4" height="5.4" rx="1"/><rect x="1.6" y="9" width="5.4" height="5.4" rx="1"/><rect x="9" y="9" width="5.4" height="5.4" rx="1"/></svg>',
  access: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="5.5" cy="5.5" r="3"/><path d="M7.8 7.8 14 14M11 11l-1.6 1.6M13 13l-1.6 1.6"/></svg>',
  backup: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" aria-hidden="true"><rect x="1.5" y="2.5" width="13" height="3.5" rx="1"/><path d="M3 6v6.5a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V6M6.5 9.2h3"/></svg>',
  help: '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2"/><path d="M8 7.2v4M8 4.9v.1"/></svg>',
};

/**
 * settingsTabs is the band of categories at the top of the settings page.
 *
 * A tablist rather than a table of contents: the page shows one category, so a
 * control that scrolls to another one would be a second, quieter way of doing
 * what the tab does. The markup is the same shape as the top bar's own tabs —
 * `role="tab"` with `aria-selected`, and every tab reachable with Tab — because
 * two tab strips that are used differently for no reason is one thing to learn
 * too many.
 */
function settingsTabs() {
  return `
    <nav class="settings-tabs" role="tablist" aria-label="${esc(t('settings.tabsAria'))}">
      ${SETTINGS_GROUPS.map((group) => `
        <button type="button" class="tab-btn" role="tab" data-category="${group.key}"
                aria-selected="false">
          ${SETTINGS_ICONS[group.key]}<span>${esc(t(`settings.group.${group.key}`))}</span>
        </button>`).join('')}
    </nav>`;
}

/**
 * panelsOf is the panels of one category that are on screen in the current mode.
 *
 * "On screen" is computed rather than measured. The obvious way — asking the
 * browser, with `offsetParent` — stops working the moment the tabs themselves do
 * the hiding: a panel is then off screen both because its mode folded it away
 * and because its category is not the chosen one, and the two are not the same
 * question. The fold is the `adv-hide` class and the mode is in `ui`, so the
 * question can be answered in full without touching the document — and it gives
 * the same answer while the settings view is hidden, which is what keeps the
 * tabs correct when a mode is switched from the dashboard.
 */
function panelsOf(key) {
  const simple = simpleMode();
  return $$(`#settings-body [data-panel][data-group="${key}"]`)
    .filter((panel) => !(simple && panel.classList.contains('adv-hide')));
}

/**
 * showSettingsCategory puts one category of the settings page on screen.
 *
 * Every panel stays in the document and only the chosen one is shown. Taking the
 * others out would take their behaviour with them: the listeners are delegated
 * from `#settings-body`, and `saveMap` reads the map panel whether or not the
 * person is looking at it.
 *
 * A category that has nothing to show gets no tab and cannot be the chosen one:
 * a viewer has no backup panel and no user panel, and in simple mode the user
 * panel is folded away — so for them the Access tab is the security page alone,
 * and Backup is not there at all.
 */
function showSettingsCategory(key) {
  const body = $('#settings-body');
  if (!body) return;
  // In the order of the table, so "the first one there is" is a stable answer
  // rather than whichever happened to come first in the document.
  const shown = SETTINGS_GROUPS
    .filter((group) => panelsOf(group.key).length)
    .map((group) => group.key);
  const chosen = shown.includes(key) ? key : (shown[0] || '');
  state.settingsTab = chosen;

  for (const panel of $$('#settings-body [data-panel]')) {
    panel.hidden = panel.dataset.group !== chosen;
  }
  for (const tab of $$('#settings-body [data-category]')) {
    const active = tab.dataset.category === chosen;
    tab.hidden = !shown.includes(tab.dataset.category);
    tab.setAttribute('aria-selected', active ? 'true' : 'false');
  }
}

async function renderSettings() {
  const settings = await loadSettings(true).catch(() => null);
  if (!settings) return;
  const runtime = settings.runtime || {};
  const stats = settings.stats || {};

  $('#settings-body').innerHTML = `
    ${settingsTabs()}

    <section class="panel" data-panel="appearance" data-group="look">
      <div class="panel-head">
        <h2>${esc(t('settings.appearance'))}</h2>
        <p class="panel-hint">${esc(t('settings.appearanceHint'))}</p>
      </div>
      <div class="grid-2">
        ${choice('mode', ui.mode, ['simple', 'advanced'])}
        ${choice('theme', ui.theme, ['auto', 'dark', 'light'])}
        ${choice('accent', ui.accent, ACCENTS, 'adv-hide')}
        ${choice('density', ui.density, ['comfortable', 'compact'], 'adv-hide')}
        ${choice('layout', ui.layout, ['grid', 'list', 'tree', 'wall', 'map'], 'adv-hide')}
        <label class="field"><span>${esc(t('settings.language'))}</span>
          <select data-setting="language">
            ${available.map((code) => `<option value="${code}"${code === ui.language ? ' selected' : ''}>${esc(nameOf(code))}</option>`).join('')}
          </select></label>
      </div>
      <p class="muted small">${esc(t('settings.modeHint'))}</p>
    </section>

    ${mapPanel()}

    ${widgetsPanel()}

    <section class="panel" data-panel="dashboard" data-group="monitor">
      <div class="panel-head">
        <h2>${esc(t('settings.dashboard'))}</h2>
        <p class="panel-hint">${esc(t('settings.dashboardHint'))}</p>
      </div>
      <div class="detail-stats">
        <div class="stat"><span class="stat-label">${esc(t('settings.sensors'))}</span><span class="stat-value">${stats.sensors ?? 0}</span></div>
        <div class="stat"><span class="stat-label">${esc(t('settings.channels'))}</span><span class="stat-value">${stats.channels ?? 0}</span></div>
        <div class="stat"><span class="stat-label">${esc(t('settings.probes'))}</span><span class="stat-value">${stats.probes ?? 0}</span></div>
        <div class="stat"><span class="stat-label">${esc(t('settings.openIncidents'))}</span><span class="stat-value">${stats.open_incidents ?? 0}</span></div>
        <div class="stat"><span class="stat-label">${esc(t('settings.dbSize'))}</span><span class="stat-value">${esc(C.fmtBytes(stats.database_bytes))}</span></div>
        <div class="stat"><span class="stat-label">${esc(t('settings.uptime'))}</span><span class="stat-value small">${esc(runtime.uptime || '—')}</span></div>
      </div>
      <dl class="kv">
        <dt>${esc(t('settings.oldest'))}</dt><dd>${esc(stats.oldest_sample ? C.fmtDateTime(stats.oldest_sample) : t('settings.none'))}</dd>
        <dt>${esc(t('settings.newest'))}</dt><dd>${esc(stats.newest_sample ? C.fmtDateTime(stats.newest_sample) : t('settings.none'))}</dd>
        <dt>${esc(t('settings.retentionRaw'))}</dt><dd>${runtime.retention_raw_hours ? `${runtime.retention_raw_hours} h` : '—'}</dd>
        <dt>${esc(t('settings.retentionHourly'))}</dt><dd>${runtime.retention_hourly_hours ? `${runtime.retention_hourly_hours} h` : '—'}</dd>
        <dt>${esc(t('settings.retentionDaily'))}</dt><dd>${esc(t('settings.retentionDailyValue'))}</dd>
      </dl>
    </section>

    ${sitesPanel()}

    ${groupsPanel()}

    ${tagsPanel()}

    ${metricsPanel()}

    ${maintenancePanel()}

    ${examplePanel()}

    <section class="panel" data-panel="access" data-group="access">
      <div class="panel-head">
        <h2>${esc(t('settings.security'))}</h2>
        <p class="panel-hint">${esc(t('settings.securityHint'))}</p>
      </div>
      <h3 class="section-title">${esc(t('settings.password'))}</h3>
      <form id="password-form" class="grid-2">
        <label class="field"><span>${esc(t('settings.currentPassword'))}</span>
          <input name="current_password" type="password" autocomplete="current-password" required></label>
        <label class="field"><span>${esc(t('settings.newPassword'))}</span>
          <input name="new_password" type="password" autocomplete="new-password" minlength="8" required>
          <div id="password-meter"></div></label>
        <div class="form-actions"><button class="btn btn-primary" type="submit">${esc(t('settings.password'))}</button></div>
        <p class="form-error" id="password-error"></p>
      </form>

      ${usersUI.twoFactorBlock(state.session?.user)}

      <h3 class="section-title">${esc(t('settings.apiKey'))}</h3>
      <p class="muted">${esc(t('settings.apiKeyHint'))}</p>
      <div class="key-row">
        <code id="api-key">${apiKeyText(settings.api_key)}</code>
        <button class="btn" data-action="copy-key"${settings.api_key && can('admin') ? '' : ' disabled'}>${esc(t('settings.copy'))}</button>
        <button class="btn" data-action="new-key"${denied('admin')}>${esc(t('settings.rotateKey'))}</button>
        <button class="btn btn-danger" data-action="drop-key"${settings.api_key && can('admin') ? '' : ' disabled'}>${esc(t('settings.deleteKey'))}</button>
      </div>
    </section>

    ${can('admin') ? `
    <section class="panel adv-hide" id="users-panel" data-panel="users" data-group="access">
      <div class="panel-head">
        <h2>${esc(t('users.title'))}</h2>
        <p class="panel-hint">${esc(t('users.panelHint'))}</p>
      </div>
      ${usersUI.panel({ users: state.users, me: state.session?.user })}
    </section>` : ''}

    ${backupPanel()}

    <section class="panel" data-panel="help" data-group="help">
      <div class="panel-head">
        <h2>${esc(t('settings.help'))}</h2>
        <p class="panel-hint">${esc(t('settings.tourHint'))}</p>
      </div>
      <div class="form-actions">
        <button class="btn" data-action="tour">${esc(t('settings.tourStart'))}</button>
      </div>
    </section>

    <section class="panel" data-panel="diagnostics" data-group="help">
      <div class="panel-head">
        <h2>${esc(t('settings.diagnostics'))}</h2>
        <p class="panel-hint">${esc(t('settings.doctorHint'))}</p>
      </div>
      <div class="form-actions">
        <button class="btn" data-action="doctor-run">${esc(t('settings.doctorRun'))}</button>
      </div>
      <div id="doctor-result"></div>
      <div class="key-row">
        <code id="doctor-command">docker exec probeglass probeglass doctor</code>
        <button class="btn" data-action="copy-doctor">${esc(t('settings.copy'))}</button>
      </div>
    </section>

    <section class="panel" data-panel="about" data-group="help">
      <div class="panel-head">
        <h2>${esc(t('settings.about'))}</h2>
        <p class="panel-hint">${esc(t('settings.aboutHint'))}</p>
      </div>
      <dl class="kv">
        <dt>${esc(t('settings.channel'))}</dt>
        <dd><span class="pill st-${esc(channelTone(runtime.channel))}">${esc(channelWord(runtime.channel))}</span>
          <span class="muted small">${esc(t('settings.channelAbout.' + channelKey(runtime.channel)))}</span></dd>
        <dt>${esc(t('settings.version'))}</dt><dd>${esc(runtime.version || '—')}</dd>
        <dt>${esc(t('settings.built'))}</dt><dd>${esc(when(runtime.build_date))}</dd>
        <dt>${esc(t('settings.baseUrl'))}</dt><dd>${esc(runtime.base_url || '—')}</dd>
        <dt>${esc(t('settings.dataDir'))}</dt><dd>${esc(runtime.data_dir || '—')}</dd>
        <dt>${esc(t('settings.configFile'))}</dt><dd>${esc(runtime.config_file || t('settings.none'))}</dd>
        <dt>${esc(t('settings.logLevel'))}</dt><dd>${esc(runtime.log_level || '—')}</dd>
      </dl>
    </section>`;

  // The folded panels were drawn with the markup above; the mode decides whether
  // they start open. That has to happen before the category is shown, because the
  // fold is a CSS decision and whether a category has anything left to show is
  // read off the class it sets.
  applyFold();
  // The category the person was last in, which is nothing on the first visit —
  // and then showSettingsCategory falls back to the first one there is.
  showSettingsCategory(state.settingsTab);
  syncBackupScope();
  // The panel is rebuilt on every visit, so this mounts onto the fresh input and
  // the listener on the old one goes away with it.
  const newPassword = $('#password-form input[name="new_password"]');
  if (newPassword) mountPasswordMeter(newPassword, $('#password-meter'));
}

/** Render the diagnostic result: counts, the findings, and the raw output. */
function doctorMarkup(out) {
  const s = out.summary || {};
  const findings = (out.findings || []).map((f) => `
    <li class="finding lv-${esc(f.level)}">
      <span class="pill lv-${esc(f.level)}">${esc(f.level)}</span>
      <span class="finding-label">${esc(f.label)}</span>
      <span class="finding-detail muted">${esc(f.detail)}</span>
      ${f.hint ? `<span class="finding-hint muted">${esc(f.hint)}</span>` : ''}
    </li>`).join('');
  return `
    <div class="doctor-summary">
      <span class="pill lv-ok">${s.ok ?? 0} ${esc(t('settings.doctorOk'))}</span>
      <span class="pill lv-warn">${s.warn ?? 0} ${esc(t('settings.doctorWarn'))}</span>
      <span class="pill lv-fail">${s.fail ?? 0} ${esc(t('settings.doctorFail'))}</span>
      <span class="muted small">${esc(C.fmtDateTime(s.ran_at))}</span>
    </div>
    <ul class="findings">${findings}</ul>
    <details class="adv-fold">
      <summary>${esc(t('settings.doctorOutput'))}</summary>
      <pre class="doctor-text">${esc(out.text || '')}</pre>
    </details>`;
}

// Listeners are delegated from the persistent container, so re-rendering the
// panel never stacks duplicate handlers.
$('#settings-body').addEventListener('change', onSettingsChange);
const settingsBody = $('#settings-body');
settingsBody.addEventListener('click', onSettingsClick);
// Dragging is the mouse path; the number in the editor stays as the keyboard one.
settingsBody.addEventListener('dragstart', onGroupDragStart);
settingsBody.addEventListener('dragover', onGroupDragOver);
settingsBody.addEventListener('drop', onGroupDrop);
settingsBody.addEventListener('dragend', clearGroupDrag);

// The same order can be arranged on the dashboard itself. The container is
// static; only its contents are redrawn by a refresh.
const dashboardGroups = $('#groups');
dashboardGroups.addEventListener('dragstart', onGroupDragStart);
dashboardGroups.addEventListener('dragover', onGroupDragOver);
dashboardGroups.addEventListener('drop', onGroupDrop);
dashboardGroups.addEventListener('dragend', clearGroupDrag);
$('#settings-body').addEventListener('submit', (event) => {
  if (event.target.id === 'password-form') onPasswordChange(event);
});

async function onSettingsChange(event) {
  // Two controls in this panel are not preferences: the backup scope decides what
  // the download contains, and the file field is the restore. Neither is stored on
  // the account, so neither goes through persistUI.
  if (event.target.id === 'backup-scope') {
    syncBackupScope();
    return;
  }
  if (event.target.id === 'restore-file') {
    await restoreFromFile(event.target);
    return;
  }

  // A widget's checkbox is a preference, but not one of the `[data-setting]`
  // ones: it does not set a value to the widget's own name, it adds the widget to
  // a list or takes it out.
  const toggle = event.target.closest('[data-pick-toggle]');
  if (toggle) {
    const id = toggle.dataset.pickToggle;
    const list = widgetsUI.parse(ui.widgets);
    const next = toggle.checked
      ? (list.includes(id) ? list : list.concat([id]))
      : list.filter((x) => x !== id);
    await persistWidgets(next);
    return;
  }

  const select = event.target.closest('[data-setting]');
  if (!select) return;
  await persistUI(select.dataset.setting, select.value, select);
}

/**
 * syncBackupScope says what the chosen scope contains.
 *
 * The two choices are named in a sentence under the controls rather than in the
 * option labels, because "config" and "history" are not words that tell anybody
 * whether their outage log is in the file. The day count only means something for
 * the history scope, so it is folded away with it.
 */
function syncBackupScope() {
  const scope = $('#backup-scope');
  if (!scope) return;
  const history = scope.value === 'history';
  const days = $('#backup-days-field');
  if (days) days.hidden = !history;
  const summary = $('#backup-summary');
  if (summary) summary.textContent = t(history ? 'backup.scope.historyHint' : 'backup.scope.configHint');
}

/**
 * runDoctor asks the server for the self-check and shows what came back.
 *
 * The route is the most expensive thing in the API — it re-reads the database
 * and dials the health endpoint — so it gets a longer deadline than the rest and
 * the server caches the run for a few seconds behind a single flight.
 */
async function runDoctor(button) {
  const host = $('#doctor-result');
  button.disabled = true;
  host.innerHTML = `<p class="muted">${esc(t('settings.doctorRunning'))}</p>`;
  try {
    const out = await api('/api/doctor', { method: 'POST', timeout: 30000 });
    host.innerHTML = doctorMarkup(out);
    applyFold();
  } catch (err) {
    host.innerHTML = `<p class="form-error">${esc(err.message)}</p>`;
  } finally {
    button.disabled = false;
  }
}

/**
 * downloadBackup fetches the backup and hands the file to the browser.
 *
 * Through `fetch` and a blob rather than a plain navigation to the URL. The reason
 * is the failure path: a 403 or a 500 that arrives as a navigation replaces the
 * whole page with a JSON error document, and somebody who asked for a backup finds
 * themselves looking at `{"error":"…"}` with no way back. The cost is that the bytes
 * pass through the page once, which for one homelab installation's history is a few
 * megabytes; the server still streams, so nothing is held twice on that side.
 *
 * The filename comes from the server's own `Content-Disposition` rather than being
 * made up here. It is the same string either way, but only one place should decide
 * what a backup file is called.
 */
async function downloadBackup(button) {
  const scope = $('#backup-scope')?.value || 'config';
  const days = $('#backup-days')?.value || '30';
  const query = scope === 'history' ? `?scope=history&days=${encodeURIComponent(days)}` : '?scope=config';
  const error = $('#backup-error');
  if (error) error.textContent = '';

  button.disabled = true;
  try {
    const res = await fetch(`/api/backup${query}`, { credentials: 'same-origin' });
    if (!res.ok) throw new Error(errorFrom(await res.text()) || t('common.error'));
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = filenameFrom(res.headers.get('Content-Disposition')) || 'probeglass-backup.json';
    document.body.append(link);
    link.click();
    link.remove();
    // Revoked on the next turn of the event loop rather than immediately: some
    // browsers have not started reading the blob at the moment `click()` returns.
    setTimeout(() => URL.revokeObjectURL(url), 0);
    toast(t('backup.downloaded'));
  } catch (err) {
    if (error) error.textContent = err.message;
  } finally {
    button.disabled = false;
  }
}

/**
 * downloadConfig writes the installation out as the YAML a file-driven setup is
 * configured from. It is the other direction from the backup: no accounts, no
 * history, no incidents — just the three things a configuration file can hold.
 */
async function downloadConfig(button) {
  const error = $('#backup-error');
  if (error) error.textContent = '';
  button.disabled = true;
  try {
    const res = await fetch('/api/config/export', { credentials: 'same-origin' });
    if (!res.ok) throw new Error(errorFrom(await res.text()) || t('common.error'));
    const text = await res.text();
    const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = 'probeglass.yaml';
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
    toast(t('configExport.downloaded'));
  } catch (err) {
    if (error) error.textContent = err.message;
  } finally {
    button.disabled = false;
  }
}

/**
 * restoreFromFile sends the chosen file as the body of one request.
 *
 * The file object is the body: it is a `Blob`, `fetch` streams it, and nothing is
 * read into a string on the way. `decodeJSON` is deliberately not used here — it
 * caps the body at a megabyte, and a backup with history is several times that.
 *
 * Two confirmations, and the second one is the one that says what is about to
 * happen. A restore replaces sensors, channels, accounts and settings with what the
 * file says; there is no partial restore and no undo beyond the copy the server
 * takes first, and a single "are you sure" is a question people answer reflexively.
 */
async function restoreFromFile(input) {
  const file = input.files?.[0];
  input.value = '';
  if (!file) return;

  const error = $('#backup-error');
  if (error) error.textContent = '';
  if (!window.confirm(t('backup.confirmRestore', { name: file.name }))) return;
  if (!window.confirm(t('backup.confirmRestoreAgain'))) return;

  const button = $('[data-action="backup-file"]');
  if (button) button.disabled = true;
  try {
    const res = await fetch('/api/restore', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: file,
    });
    const text = await res.text();
    if (!res.ok) throw new Error(errorFrom(text) || t('common.error'));
    const out = JSON.parse(text || '{}');
    const counts = out.counts || {};
    // What was put back, what happened to the previous state, and — if the file brings
    // back sessions that were signed out after it was written — that. All three go into
    // one sentence, because the page is about to be replaced and a message that arrives
    // after the reload is a message nobody reads.
    let message = t('backup.restored', { sensors: counts.sensors ?? 0, channels: counts.channels ?? 0 });
    if (out.safety_copy) message += ' ' + t('backup.safetyCopy', { name: out.safety_copy });
    if (out.token_epoch_notice) message += ' ' + t('backup.epochNotice');
    toast(message);
    // The whole page is out of date now: the accounts may be different, so may the
    // role this person has. A reload is the only way to be sure nothing of the old
    // installation is still on screen. It waits for the toast, which is the only place
    // the result is said.
    setTimeout(() => location.reload(), 4000);
  } catch (err) {
    if (error) error.textContent = err.message;
    if (button) button.disabled = false;
  }
}

/** filenameFrom reads the suggested name out of a Content-Disposition header. */
function filenameFrom(header) {
  const match = /filename="?([^";]+)"?/.exec(header || '');
  return match ? match[1] : '';
}

/** errorFrom pulls the server's sentence out of an error body, if there is one. */
function errorFrom(text) {
  try {
    return JSON.parse(text).error || '';
  } catch {
    return '';
  }
}

async function onPasswordChange(event) {
  event.preventDefault();
  const form = event.target;
  const error = $('#password-error');
  error.textContent = '';
  try {
    await api('/api/password', {
      method: 'PUT',
      body: {
        current_password: form.elements.current_password.value,
        new_password: form.elements.new_password.value,
      },
    });
    form.reset();
    toast(t('settings.passwordChanged'));
  } catch (err) {
    error.textContent = err.message;
  }
}

async function onSettingsClick(event) {
  // The category tabs first: they are not an action and carry no data-action, and
  // the two are in the same container so that one delegated listener serves both.
  const tab = event.target.closest('[data-category]');
  if (tab) {
    showSettingsCategory(tab.dataset.category);
    return;
  }

  // The widget picker's arrows and its reset, likewise: neither is a data-action.
  const pick = event.target.closest('[data-pick-move]');
  if (pick) {
    movePickedWidget(pick.dataset.pickId, Number(pick.dataset.pickMove));
    return;
  }
  if (event.target.closest('[data-pick-reset]')) {
    await persistWidgets(widgetsUI.DEFAULT_IDS.slice());
    return;
  }

  const button = event.target.closest('[data-action]');
  if (!button) return;
  const action = button.dataset.action;
  const account = state.users.find((u) => u.id === Number(button.dataset.id));
  try {
    if (action === 'copy-key') {
      await copyText($('#api-key').textContent.trim());
      toast(t('settings.copied'));
    } else if (action === 'new-key') {
      const result = await api('/api/api-key', { method: 'POST' });
      await renderSettings();
      toast(t('settings.rotateKey'));
      void result;
    } else if (action === 'drop-key') {
      await api('/api/api-key', { method: 'DELETE' });
      await renderSettings();
    } else if (action === 'copy-doctor') {
      await copyText($('#doctor-command').textContent.trim());
      toast(t('settings.copied'));
    } else if (action === 'doctor-run') {
      await runDoctor(button);
    } else if (action === 'backup-download') {
      await downloadBackup(button);
    } else if (action === 'config-export') {
      await downloadConfig(button);
    } else if (action === 'metrics-copy') {
      await copyText($('#metrics-token').value);
      toast(t('settings.copied'));
    } else if (action === 'metrics-rotate') {
      await rotateMetricsToken();
    } else if (action === 'metrics-disable') {
      await disableMetricsToken();
    } else if (action === 'backup-file') {
      $('#restore-file').click();
    } else if (action === 'tour') {
      await openTour();
    } else if (action === 'logout') {
      // The settings page no longer carries a sign-out button — it lives in the
      // account menu now, where it is looked for — but the branch stays: a stale
      // page still in the browser may be showing the old button.
      await signOut();
    } else if (action === 'user-new') {
      users.openUserEditor(null);
    } else if (action === 'user-edit' && account) {
      users.openUserEditor(account);
    } else if (action === 'user-password' && account) {
      users.openUserPassword(account);
    } else if (action === 'user-toggle' && account) {
      await setUserDisabled(account, !account.disabled);
    } else if (action === 'user-delete' && account) {
      await removeUser(account);
    } else if (action === 'totp-start') {
      await users.startEnrolment();
    } else if (action === 'totp-disable') {
      users.openConfirm({
        title: t('totp.disable'),
        message: t('totp.why'),
        mode: 'totp-off',
      });
    } else if (action === 'totp-recovery') {
      users.openConfirm({
        title: t('totp.renew'),
        message: t('totp.codesBody'),
        mode: 'totp-codes',
      });
    } else if (action === 'drop-examples') {
      await removeExampleData();
    } else if (action === 'restore-examples') {
      await restoreExampleData();
    } else if (action === 'site-new') {
      openSiteDialog(null);
    } else if (action === 'site-edit') {
      const site = state.sites.find((s) => s.name === button.dataset.name);
      if (site) openSiteDialog(site);
    } else if (action === 'site-delete') {
      const site = state.sites.find((s) => s.name === button.dataset.name);
      if (site) await removeSite(site);
    } else if (action === 'group-new') {
      openLabelEditor('group', '', null, { creating: true });
    } else if (action === 'group-edit') {
      const group = iconUI.groupOf(state.groups, button.dataset.name);
      openLabelEditor('group', button.dataset.name, group);
    } else if (action === 'group-forget') {
      await openGroupDelete(button.dataset.name);
    } else if (action === 'tag-new') {
      openLabelEditor('tag', '', null, { creating: true });
    } else if (action === 'tag-edit') {
      const tag = iconUI.groupOf(state.tags, button.dataset.name);
      openLabelEditor('tag', button.dataset.name, tag);
    } else if (action === 'tag-forget') {
      const tag = iconUI.groupOf(state.tags, button.dataset.name);
      await forgetLabel('tag', button.dataset.name, tag);
    } else if (action === 'window-new') {
      openWindowEditor(null);
    } else if (action === 'window-edit') {
      const w = (state.windows || []).find((x) => String(x.id) === button.dataset.id);
      if (w) openWindowEditor(w);
    } else if (action === 'window-delete') {
      const w = (state.windows || []).find((x) => String(x.id) === button.dataset.id);
      if (w) await removeWindow(w);
    } else if (action === 'map-save') {
      await saveMap();
    }
  } catch (err) {
    toast(err.message, 'error');
  }
}

/**
 * copyText puts text on the clipboard, and falls back to selecting it.
 *
 * The dashboard is usually reached over plain HTTP on a home network, where the
 * clipboard API refuses to run — it is restricted to secure contexts. That would
 * make "Copy all" fail silently at the one moment it matters most, so the fallback
 * at least leaves the text selected for a manual copy.
 */
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Fall through to the selection below.
  }
  const node = document.createElement('textarea');
  node.value = text;
  node.setAttribute('readonly', '');
  node.className = 'offscreen';
  document.body.appendChild(node);
  node.select();
  toast(t('settings.copyManual'));
  return false;
}

/**
 * generateSecret returns 128 random bits as hex. Hex on purpose: a community or
 * a passphrase is typed into a device's own form, and a value with punctuation
 * in it is a value some of those forms refuse.
 */
function generateSecret() {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('');
}

// One listener for every Generate button. It fills the field, reveals it so the
// value can be read while it is being stored on the device, and copies it. The
// scope is the form or dialog the button lives in, so the named input cannot be
// confused with one of the same name in another open dialog.
document.addEventListener('click', (event) => {
  const button = event.target.closest('[data-generate-secret]');
  if (!button) return;
  const scope = button.closest('form, dialog') || document;
  const input = scope.querySelector(`input[name="${button.dataset.generateSecret}"]`);
  if (!input) return;
  const value = generateSecret();
  input.value = value;
  input.type = 'text';
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.dispatchEvent(new Event('change', { bubbles: true }));
  input.focus();
  input.select();
  copyText(value).then((copied) =>
    toast(t(copied ? 'common.generatedCopied' : 'common.generatedManual')));
});

async function setUserDisabled(account, disabled) {
  await api(`/api/users/${account.id}`, { method: 'PUT', body: { disabled } });
  await loadUsers(true);
  await renderSettings();
  toast(t('users.updated'));
}

async function removeUser(account) {
  if (!confirmDialog(t('users.deleteConfirm', { name: account.username }))) return;
  await api(`/api/users/${account.id}`, { method: 'DELETE' });
  await loadUsers(true);
  await renderSettings();
  toast(t('users.deleted'));
}

/**
 * removeExampleData clears out everything the dashboard came with.
 *
 * The confirmation carries the counts, because this is the one button in the
 * product that removes history rather than adding configuration, and the numbers
 * are what make it a decision rather than a reflex.
 */
async function removeExampleData() {
  const data = state.exampleData || { sensors: 0, results: 0, incidents: 0, sites: 0 };
  if (!confirmDialog(t('examples.confirm', { sensors: data.sensors, sites: data.sites || 0 }))) return;
  const removed = await api('/api/examples', { method: 'DELETE' });
  // The counts that come back are what was actually there, which is not
  // necessarily what the panel was showing: somebody may have added or removed a
  // sensor in another tab in between.
  toast(t('examples.removed', removed));
  state.exampleData = { sensors: 0, results: 0, incidents: 0, sites: 0 };
  await loadExampleData(true);
  await refresh({ background: true });
  await renderSettings();
}

/**
 * restoreExampleData puts the example data back.
 *
 * It loads the richer demonstration set rather than the four bare sensors a fresh
 * installation starts with: somebody who has just cleared the dashboard and asked
 * for it back wants to see what it looks like with something on it — groups,
 * curves, an incident or two — and that set comes with an invented week of
 * history. The confirmation says so, so that "example data" is not mistaken for
 * "your data coming back".
 */
async function restoreExampleData() {
  if (!confirmDialog(t('examples.confirmLoad'))) return;
  const data = await api('/api/examples', { method: 'POST' });
  toast(t('examples.restored', data));
  state.exampleData = data;
  await loadExampleData(true);
  await refresh({ background: true });
  await renderSettings();
}

// ---------------------------------------------------------------------------
// responsive charts
// ---------------------------------------------------------------------------

let resizeTimer = null;
window.addEventListener('resize', () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    if (state.detail) loadDetailBody(state.detail);
  }, 250);
});

initHelp();
boot();
