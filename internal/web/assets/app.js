// Weatherglass dashboard.
//
// Plain ES modules, no framework: the whole UI is a few hundred lines of DOM
// building on top of a REST API and one Server-Sent Events stream. State lives
// in one object, views are pure functions of it, and everything the user can
// type is escaped on the way into the DOM.

import { t, applyTranslations, setLanguage, available } from './i18n.js';
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

const esc = C.esc;

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
  settings: null,
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
  return Number.isNaN(at.getTime()) ? String(value) : at.toLocaleString();
}

// A request that never comes back is the worst failure mode this dashboard can
// have: the page sits on a spinner and nothing says why. Every call therefore
// carries a deadline, and a call that hits it reports a message the user can act
// on instead of hanging forever.
const API_TIMEOUT_MS = 15000;

/** Fetch JSON from the API, surfacing the server's error message. */
async function api(path, { method = 'GET', body, timeout = API_TIMEOUT_MS } = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  let res;
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
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
 * modal is the one dialog every editor in the dashboard shares.
 *
 * It owns the four things that have to be reset between openings, because getting
 * any of them wrong leaves the next dialog carrying the last one's state. The
 * delete button is the sharpest of them: each editor that offers one injects it
 * into the shared footer, and an editor that does not offer one has to take it
 * away again — otherwise "New channel" opens with a live "Delete sensor" button
 * under it.
 */
const modal = {
  open({ title, body, mode, id = '', saveLabel, showTest = false, locked = false }) {
    const form = $('#modal-form');
    form.dataset.mode = mode;
    form.dataset.id = id === '' ? '' : String(id);
    form.dataset.locked = locked ? '1' : '';
    $('#modal-body').innerHTML = body;
    $('#modal-error').textContent = '';
    $('#modal-title').textContent = title;
    applyTranslations($('#modal'));
    // After applyTranslations, not before: the save button carries the generic
    // "Save" in its data-i18n, and this is the per-dialog word for it.
    $('#modal-save').textContent = saveLabel || t('common.save');
    $('#modal-test').hidden = !showTest;
    $('#modal-cancel').hidden = locked;
    $('#modal-x').hidden = locked;
    this.clearDanger();
    $('#modal').showModal();
    form.querySelector('input:not([disabled]):not([type="radio"]), select, textarea')?.focus();
  },

  /** danger puts a destructive action at the far left of the footer. */
  danger(label, onClick) {
    this.clearDanger();
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'btn btn-danger danger-delete';
    button.textContent = label;
    button.addEventListener('click', onClick);
    const footer = $('#modal .sheet-foot');
    footer.insertBefore(button, footer.firstChild);
  },

  clearDanger() {
    $('.danger-delete')?.remove();
  },

  close() {
    $('#modal').close();
  },
};

// A locked dialog has no way out but its own button: Escape would close it and
// take the only copy of whatever it is showing with it. The dashboard uses it for
// the recovery codes, which are shown exactly once.
$('#modal').addEventListener('cancel', (event) => {
  if ($('#modal-form').dataset.locked === '1') event.preventDefault();
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
 * buildLayoutSwitcher fills the control once.
 *
 * The listeners are on the container and delegated, so nothing has to be
 * rewired when a button's pressed state changes — and there is exactly one
 * handler however many times the view is redrawn.
 */
function buildLayoutSwitcher() {
  const host = $('#layout-switcher');
  if (!host || host.dataset.built) return;
  host.dataset.built = '1';
  host.innerHTML = LAYOUTS.map((value) => `
    <button class="seg-btn" type="button" data-layout="${value}"
            title="${esc(t(`settings.layout.${value}`))}"
            aria-label="${esc(t(`settings.layout.${value}`))}">
      ${LAYOUT_ICONS[value]}<span>${esc(t(`settings.layout.${value}`))}</span>
    </button>`).join('');
  host.addEventListener('click', (event) => {
    const button = event.target.closest('[data-layout]');
    if (button) void persistUI('layout', button.dataset.layout);
  });
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
  ui[key] = value;
  applyUIPreferences();
  if (key === 'layout') renderDashboard();
  try {
    await api('/api/settings', { method: 'PUT', body: { ui: { [key]: value } } });
  } catch (err) {
    ui[key] = previous;
    applyUIPreferences();
    if (control) control.value = previous;
    if (key === 'layout') renderDashboard();
    toast(err.message, 'error');
  }
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
    window.__weatherglassBooted = true;
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
      await Promise.all([loadUsers(), loadExampleData(), loadSites()]);
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

function renderAll() {
  if (state.route === 'dashboard') renderDashboard();
  if (state.route === 'incidents') renderIncidents();
  if (state.route === 'channels') renderChannels();
  if (state.route === 'settings') renderSettings();
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
    groups.innerHTML = `<p class="muted">${esc(t('common.none'))}</p>`;
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
 * renderByGroup lays sensors out under the heading of the group they belong to,
 * in the order they arrived. It is what the card and list views have always
 * done; the tree and the wall do their own, deeper, nesting.
 */
function renderByGroup(sensors, render) {
  const byGroup = new Map();
  for (const sensor of sensors) {
    const key = sensor.group || '';
    if (!byGroup.has(key)) byGroup.set(key, []);
    byGroup.get(key).push(sensor);
  }
  let html = '';
  for (const [group, items] of byGroup) {
    if (group) html += `<h2 class="group-title">${esc(group)}</h2>`;
    html += render(items);
  }
  return html;
}

/**
 * layoutContext is what the tree and the wall are handed besides the sensors.
 *
 * The callbacks are the same ones the cards already use, so every view opens the
 * same detail and offers the same buttons — the layouts module never learns what
 * any of it does, it only draws.
 */
function layoutContext() {
  return { statusPill, cardActions, sparkline: C.sparkline, openDetail };
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
      return { tileURL: map.tile_url || '', attribution: map.attribution || '' };
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

function statusPill(status) {
  return `<span class="pill st-${esc(status)}"><i class="dot"></i>${esc(statusWord(status))}</span>`;
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
      <span title="${esc(t('summary.uptime'))}">24h <b>${esc(C.fmtPct(sensor.uptime_24h))}</b></span>
      <span>7d <b>${esc(C.fmtPct(sensor.uptime_7d))}</b></span>
      <span>30d <b>${esc(C.fmtPct(sensor.uptime_30d))}</b></span>
      <span class="muted">${esc(t('detail.averageShort'))} ${esc(C.fmtLatency(sensor.avg_latency_ms))}</span>
    </div>`;
}

function renderCard(sensor) {
  const down = sensor.status === 'down';
  const acked = sensor.open_incident?.acked_at;
  const example = sensor.origin === 'example';
  return `
    <article class="card st-${esc(sensor.status)}${down ? ' is-down' : ''}${acked ? ' is-acked' : ''}${example ? ' is-example' : ''}" data-id="${sensor.id}" data-origin="${esc(sensor.origin || '')}" tabindex="0">
      <header class="card-head">
        ${statusPill(sensor.status)}
        <h3 class="card-name">${esc(sensor.name)}</h3>
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
    <tr class="row st-${esc(sensor.status)}${sensor.status === 'down' ? ' is-down' : ''}" data-id="${sensor.id}">
      <td>${statusPill(sensor.status)}</td>
      <td class="cell-name">
        <b>${esc(sensor.name)}</b>
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
  $('#detail-title').textContent = sensor.name;
  detailProbes = 0;
  syncDetailStatus(sensor.status);
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
  $('#detail-body').innerHTML = `<p class="muted">${esc(t('incidents.loading'))}</p>`;
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

/** syncDetailStatus moves the pill in the window's header, and nothing else. */
function syncDetailStatus(status) {
  const el = $('#detail-status');
  if (!el) return;
  el.className = `pill st-${status}`;
  el.innerHTML = `<i class="dot"></i>${esc(statusWord(status))}`;
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
  syncDetailStatus(sensor.status);
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
 * buildIncidentFilter fills the chip row and wires the whole filter line once.
 *
 * The listeners are delegated on the containers, as everywhere else in this file,
 * so the controls can be marked up as often as the state wants without anyone
 * having to think about rewiring them.
 */
function buildIncidentFilter() {
  const chips = $('#incident-range');
  if (!chips || chips.dataset.built) return;
  chips.dataset.built = '1';
  chips.innerHTML = INCIDENT_RANGES.map((r) => `
    <button class="chip" type="button" data-range="${r}">${esc(t(`range.${r}`))}</button>`).join('');

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
    label: 'HTTP(S)',
    targetLabel: 'URL',
    placeholder: 'https://example.org/health',
    fields: [
      { key: 'expected_status', type: 'text', placeholder: '200-299' },
      { key: 'method', type: 'select', options: ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'] },
      { key: 'follow_redirects', type: 'bool' },
      { key: 'body_contains', type: 'text' },
      { key: 'body_not_contains', type: 'text' },
      { key: 'body_regex', type: 'text' },
      { key: 'expected_headers', type: 'map' },
      { key: 'headers', type: 'map' },
      { key: 'cert_warn_days', type: 'number', placeholder: '14' },
      { key: 'bearer_token', type: 'password' },
      { key: 'basic_auth_user', type: 'text' },
      { key: 'basic_auth_pass', type: 'password' },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'tcp',
    label: 'TCP port',
    targetLabel: 'Host:port',
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
    value: 'tls',
    label: 'TLS certificate',
    targetLabel: 'Host or host:port',
    placeholder: 'example.org:443',
    fields: [
      { key: 'server_name', type: 'text', placeholder: 'example.org' },
      { key: 'cert_warn_days', type: 'number', placeholder: '14' },
      { key: 'insecure_skip_verify', type: 'bool' },
    ],
  },
  {
    value: 'docker',
    label: 'Docker container',
    targetLabel: 'Container name or id',
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
    label: 'SNMP',
    targetLabel: 'Host or host:port',
    placeholder: '192.168.1.2',
    fields: [
      { key: 'oid', type: 'text', placeholder: '1.3.6.1.2.1.1.3.0' },
      { key: 'community', type: 'password' },
      { key: 'version', type: 'select', options: ['2c', '1'] },
      { key: 'expect', type: 'text' },
      { key: 'min', type: 'number' },
      { key: 'max', type: 'number' },
    ],
  },
  {
    value: 'ping',
    label: 'Ping (ICMP)',
    targetLabel: 'Host',
    placeholder: '192.168.1.1',
    fields: [{ key: 'count', type: 'number', placeholder: '3' }],
  },
  {
    value: 'dns',
    label: 'DNS',
    targetLabel: 'Name',
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

/**
 * withHelp renders a field label with its "?" beside it. The explanation and its
 * example both live in the catalogue: the key names the sentence, and the
 * example is stored beside it under the same key plus "Example".
 */
const withHelp = (text, key) => `${esc(text)} ${helpButton(key, `${key}Example`)}`;

/** Render one option widget. Empty means "leave it at the default". */
function optionField(field, options) {
  const value = options?.[field.key];
  const key = `help.opt.${field.key}`;
  const label = `${esc(t(`opt.${field.key}`))} ${helpButton(key, `${key}Example`)}`;
  const name = `opt_${field.key}`;

  switch (field.type) {
    case 'bool': {
      const current = value === undefined || value === null ? '' : String(value);
      return `<label class="field"><span>${label}</span>
        <select name="${name}">
          <option value=""${current === '' ? ' selected' : ''}>${esc(t('common.optional'))}</option>
          <option value="true"${current === 'true' ? ' selected' : ''}>${esc(t('common.yes'))}</option>
          <option value="false"${current === 'false' ? ' selected' : ''}>${esc(t('common.no'))}</option>
        </select></label>`;
    }
    case 'select': {
      const current = value === undefined || value === null ? '' : String(value);
      // The empty choice means "leave it at the default" for most fields, which
      // is why it is worded that way. A field where empty means something else
      // says so through emptyLabel.
      const emptyText = field.emptyLabel ? t(field.emptyLabel) : t('common.optional');
      const opts = [`<option value=""${current === '' ? ' selected' : ''}>${esc(emptyText)}</option>`]
        .concat(field.options.map((o) => `<option value="${esc(o)}"${current === o ? ' selected' : ''}>${esc(o)}</option>`));
      return `<label class="field"><span>${label}</span><select name="${name}">${opts.join('')}</select></label>`;
    }
    case 'map': {
      const lines = value && typeof value === 'object'
        ? Object.entries(value).map(([k, v]) => `${k}: ${v}`).join('\n')
        : '';
      return `<label class="field"><span>${label}</span><textarea name="${name}" rows="3" spellcheck="false">${esc(lines)}</textarea></label>`;
    }
    case 'number':
      return `<label class="field"><span>${label}</span><input name="${name}" type="number" min="0" value="${value === undefined || value === null ? '' : esc(value)}" placeholder="${esc(field.placeholder || '')}"></label>`;
    case 'password':
      return `<label class="field"><span>${label}</span><input name="${name}" type="password" autocomplete="new-password" value="${value === undefined ? '' : esc(value)}"></label>`;
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
  const options = {};
  for (const field of def.fields) {
    const input = form.elements[`opt_${field.key}`];
    if (!input) continue;
    const raw = input.value;
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

function sensorFormMarkup(sensor) {
  const isNew = !sensor;
  const def = typeDef(sensor?.type || 'http');
  const channels = state.channels.map((c) => {
    const selected = (sensor?.channel_ids || []).includes(c.id);
    return `<label class="sensor-row"><input type="checkbox" name="channel" value="${c.id}"${selected ? ' checked' : ''}>
      <span>${esc(c.name)} <small class="muted">${esc(c.type)}</small></span></label>`;
  }).join('');

  return `
    <div class="grid-2">
      <label class="field"><span>${withHelp(t('sensor.name'), 'help.sensor.name')}</span>
        <input name="name" type="text" required value="${esc(sensor?.name || '')}" autocomplete="off"></label>
      <label class="field"><span>${withHelp(t('sensor.type'), 'help.sensor.type')}</span>
        <select name="type" id="sensor-type">
          ${SENSOR_TYPES.map((c) => `<option value="${c.value}"${c.value === def.value ? ' selected' : ''}>${esc(c.label)}</option>`).join('')}
        </select></label>
    </div>

    <label class="field"><span id="target-label">${withHelp(def.targetLabel, 'help.sensor.target')}</span>
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
      <label class="field"><span>${withHelp(t('sensor.group'), 'help.sensor.group')}</span>
        <input name="group" type="text" value="${esc(sensor?.group || '')}" list="group-list"></label>
      <!-- A list to choose from rather than a name to type: a site is a row in
           another table, and a typo here is refused by the database. Offering
           only what exists makes the mistake impossible instead of explaining it.

           The last entry is the way out of that: a sensor is often the moment
           somebody first knows where it stands, and sending them to Settings to
           make the site and then back here to save the sensor is a trip that
           loses the half-filled form. Choosing it makes the two fields below
           appear, and the site is made with the sensor. -->
      <label class="field"><span>${withHelp(t('sensor.site'), 'help.sensor.site')}</span>
        <select name="site" id="sensor-site">
          <option value="">${esc(t('sites.none'))}</option>
          ${(state.sites || []).map((s) => `<option value="${esc(s.name)}"${s.name === sensor?.site ? ' selected' : ''}>${esc(s.name)}</option>`).join('')}
          <option value="${NEW_SITE}">${esc(t('sites.newOption'))}</option>
        </select></label>
      <label class="field"><span>${withHelp(t('sensor.tags'), 'help.sensor.tags')}</span>
        <input name="tags" type="text" value="${esc((sensor?.tags || []).join(', '))}"></label>
      <div class="new-site" id="sensor-new-site" hidden>
        <label class="field"><span>${esc(t('sites.name'))}</span>
          <input name="site_name" type="text" maxlength="64" autocomplete="off" placeholder="${esc(t('sites.nameHint'))}"></label>
        <label class="field"><span>${esc(t('sites.kind'))}</span>
          <select name="site_kind">
            <option value="local">${esc(t('sites.kind.local'))}</option>
            <option value="external">${esc(t('sites.kind.external'))}</option>
          </select></label>
      </div>
    </div>
    <datalist id="group-list">
      ${[...new Set(state.sensors.map((c) => c.group).filter(Boolean))].map((g) => `<option value="${esc(g)}">`).join('')}
    </datalist>

    <label class="field adv-hide"><span>${withHelp(t('sensor.reminder'), 'help.sensor.reminder')}</span>
      <input name="reminder_every" type="number" min="0" value="${sensor?.reminder_every || 0}"></label>

    <details class="options adv-hide"${isNew ? ' open' : ''}>
      <summary>${esc(def.label)}</summary>
      <div id="option-fields" class="grid-2">${def.fields.map((f) => optionField(f, sensor?.options)).join('')}</div>
    </details>

    <fieldset class="channels-pick adv-hide">
      <legend>${withHelp(t('sensor.channels'), 'help.sensor.channels')}</legend>
      ${channels || `<p class="muted">${esc(t('channels.emptyBody'))}</p>`}
    </fieldset>

    <label class="sensor-row"><input type="checkbox" name="enabled"${sensor?.enabled === false ? '' : ' checked'}>
      <span>${withHelp(t('sensor.enabled'), 'help.sensor.enabled')}</span></label>`;
}

async function openSensorEditor(existing = null) {
  await loadChannels(existing === null);
  const isNew = existing === null;
  const form = $('#modal-form');

  modal.open({
    title: isNew ? t('sensor.new') : t('sensor.edit'),
    body: sensorFormMarkup(existing),
    mode: 'sensor',
    id: existing ? existing.id : '',
    showTest: true,
  });

  const typeSelect = $('#sensor-type');
  typeSelect.addEventListener('change', () => {
    const def = typeDef(typeSelect.value);
    const values = readOptions(form, typeDef(form.dataset.lastType || def.value));
    form.dataset.lastType = typeSelect.value;
    $('#target-label').textContent = def.targetLabel;
    const target = form.elements.target;
    target.placeholder = def.placeholder;
    $('#option-fields').innerHTML = def.fields.map((f) => optionField(f, values)).join('');
  });
  form.dataset.lastType = existing?.type || 'http';

  const siteSelect = $('#sensor-site');
  siteSelect.addEventListener('change', () => syncNewSite(form));
  syncNewSite(form);

  if (!isNew && !existing.managed) {
    modal.danger(t('sensor.delete'), () => deleteSensor(existing));
  }
}

function sensorToPayload(sensor) {
  return {
    name: sensor.name,
    type: sensor.type,
    target: sensor.target,
    interval: sensor.interval,
    timeout: sensor.timeout,
    retries: sensor.retries,
    enabled: sensor.enabled,
    group: sensor.group,
    tags: sensor.tags || [],
    options: sensor.options || {},
    // The API names this field channel_ids. Reading it as "channels" here
    // silently detached every alert channel on a pause/resume toggle.
    channel_ids: sensor.channel_ids || [],
    reminder_every: sensor.reminder_every || 0,
  };
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
    // The "new site" entry is a button in the shape of an option, not a site
    // called that; the site it stands for is made by resolveSite and its name
    // arrives afterwards.
    site: form.elements.site.value === NEW_SITE ? '' : form.elements.site.value,
    tags: form.elements.tags.value.split(',').map((v) => v.trim()).filter(Boolean),
    reminder_every: Number(form.elements.reminder_every.value) || 0,
    options: readOptions(form, def),
    channel_ids: $$('input[name="channel"]:checked', form).map((el) => Number(el.value)),
  };
}

// NEW_SITE is the value of the last entry in the sensor form's site list: the one
// that means "there is no such site yet". It cannot collide with a real name —
// the server allows letters, digits and a few separators, and no underscores.
const NEW_SITE = '__new__';

/** Shows the two fields that make a site, when the chosen site is one being made. */
function syncNewSite(form) {
  const block = $('#sensor-new-site', form);
  if (block) block.hidden = form.elements.site.value !== NEW_SITE;
}

/**
 * resolveSite turns the site field into the name of a site that exists.
 *
 * Creating a sensor is often the moment somebody first knows where it stands, so
 * the site list carries an entry that makes one, rather than sending them to
 * Settings and back — a trip that loses the half-filled form.
 *
 * The making happens on save and not when the entry was picked: a site chosen and
 * then thought better of should leave nothing behind, and neither should a form
 * closed without saving. A name that is already taken is used rather than made
 * again, because two people naming the same rack within a minute of each other is
 * not an error to report, and a name clash would be the only thing that said so.
 */
async function resolveSite(form) {
  const chosen = form.elements.site.value;
  if (chosen !== NEW_SITE) return chosen;
  const name = form.elements.site_name.value.trim();
  if (!name) throw new Error(t('sites.nameRequired'));
  if ((state.sites || []).some((s) => s.name === name)) return name;
  await api('/api/sites', { method: 'POST', body: { name, kind: form.elements.site_kind.value } });
  // The list is re-read rather than appended to, so the sensor being saved lands
  // in a tree that already knows about the site it is going into.
  await loadSites(true);
  toast(t('sites.created'));
  return name;
}

async function saveSensor(payload, id) {
  const saved = id
    ? await api(`/api/sensors/${id}`, { method: 'PUT', body: payload })
    : await api('/api/sensors', { method: 'POST', body: payload });
  await refresh({ background: true });
  return saved;
}

$('#new-sensor').addEventListener('click', () => openSensorEditor(null));
document.addEventListener('click', (event) => {
  if (event.target.closest('[data-action="new-sensor"]')) openSensorEditor(null);
});

// Modal plumbing: one form, several modes.
//
// The modes users.js owns are listed rather than probed for, so that the sensor
// path — the one that runs when nothing else matches — cannot quietly swallow a
// typo in a mode name.
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
    } else if (mode === 'site') {
      await submitSite(form);
    } else if (USER_MODES.has(mode)) {
      done = await users.submit(mode, form);
    } else {
      const id = form.dataset.id ? Number(form.dataset.id) : 0;
      const sensor = readSensorForm(form);
      sensor.site = await resolveSite(form);
      await saveSensor(sensor, id);
      toast(t('common.saved'));
    }
    if (done) modal.close();
  } catch (err) {
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
  return `<label class="field"><span>${esc(label)}${spec.required ? ' *' : ''} ${help}</span>
    <input name="cfg_${esc(spec.key)}" type="${type}"${required}
           value="${value === undefined ? '' : esc(value)}"
           placeholder="${esc(spec.placeholder || '')}" autocomplete="off"></label>`;
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
function widgetPicker() {
  return `
    <h3 class="section-title">${esc(t('settings.widgets'))}</h3>
    <p class="muted small">${esc(t('settings.widgetsHint'))}</p>
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

/** syncWidgetPicker redraws the picker's rows from the stored list. */
function syncWidgetPicker() {
  const host = $('#widget-picks');
  if (host) host.innerHTML = widgetPickRows();
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
      <h2>${esc(t('settings.backup'))}</h2>
      <p class="muted">${esc(t('backup.hint'))}</p>

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
  const data = state.exampleData || { sensors: 0, results: 0, incidents: 0 };
  const has = data.sensors > 0;
  return `
    <section class="panel" id="examples-panel" data-panel="examples" data-group="monitor">
      <h2>${esc(t('examples.title'))}</h2>
      <p class="muted">${esc(t('examples.hint'))}</p>
      ${has ? `
      <p class="muted small">${esc(t('examples.count', { sensors: data.sensors, results: data.results, incidents: data.incidents }))}</p>
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
    <tr data-name="${esc(site.name)}">
      <td><span class="site-kind-cell">
        <i class="legend-dot k-${esc(site.kind)}" aria-hidden="true"></i>
        <b>${esc(site.name)}</b>
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
      <h2>${esc(t('sites.title'))}</h2>
      <p class="muted">${esc(t('sites.hint'))}</p>
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
      </div>` : `<p class="muted small">${esc(t('sites.emptyBody'))}</p>`}
      <div class="form-actions">
        <button class="btn btn-primary" data-action="site-new"${denied('admin')}>${esc(t('sites.new'))}</button>
      </div>
    </section>`;
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
      <h2>${esc(t('settings.map'))}</h2>
      <label class="field"><span>${esc(t('settings.tileUrl'))}</span>
        <input name="tile_url" value="${esc(map.tile_url || '')}"
               placeholder="${esc(map.default_tile_url || '')}"${admin ? '' : ' disabled'}></label>
      <p class="muted small">${esc(t('settings.tileHint'))}</p>
      <label class="field"><span>${esc(t('settings.tileAttribution'))}</span>
        <input name="attribution" value="${esc(map.attribution || '')}"${admin ? '' : ' disabled'}></label>
      <div class="form-actions">
        <button class="btn btn-primary" data-action="map-save"${denied('admin')}>${esc(t('common.save'))}</button>
      </div>
      <p class="muted small">${esc(t('settings.tilePrivacy'))}</p>
    </section>`;
}

/** openSiteEditor is the small dialog behind "New site" and "Edit". */
function openSiteEditor(existing) {
  const site = existing || { name: '', kind: 'local', lat: null, lon: null };
  const coord = (value) => (value === null || value === undefined ? '' : String(value));
  modal.open({
    title: existing ? t('sites.edit') : t('sites.new'),
    mode: 'site',
    id: existing ? existing.name : '',
    body: `
      <label class="field adv-hide"><span>${esc(t('sites.name'))}</span>
        <input name="site_name" value="${esc(site.name)}" maxlength="64" required></label>
      <p class="muted small adv-hide">${esc(t('sites.nameHint'))}</p>
      <label class="field"><span>${esc(t('sites.kind'))}</span>
        <select name="kind">
          ${['local', 'external'].map((k) => `<option value="${k}"${k === site.kind ? ' selected' : ''}>${esc(t(`sites.kind.${k}`))}</option>`).join('')}
        </select></label>
      <div class="grid-2">
        <label class="field"><span>${esc(t('sites.latitude'))}</span>
          <input name="lat" type="number" step="any" min="-90" max="90" value="${esc(coord(site.lat))}"></label>
        <label class="field"><span>${esc(t('sites.longitude'))}</span>
          <input name="lon" type="number" step="any" min="-180" max="180" value="${esc(coord(site.lon))}"></label>
      </div>
      <p class="muted small">${esc(t('sites.coordsHint'))}</p>`,
  });
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
  const body = { name: value('site_name'), kind: value('kind') };
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

async function saveMap() {
  const panel = $('#map-panel');
  const value = (name) => panel.querySelector(`[name="${name}"]`).value.trim();
  await api('/api/map', {
    method: 'PUT',
    body: { tile_url: value('tile_url'), attribution: value('attribution') },
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
  { key: 'monitor', panels: ['dashboard', 'sites', 'examples'] },
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
      <h2>${esc(t('settings.appearance'))}</h2>
      <div class="grid-2">
        ${choice('mode', ui.mode, ['simple', 'advanced'])}
        ${choice('theme', ui.theme, ['auto', 'dark', 'light'])}
        ${choice('accent', ui.accent, ['azure', 'violet', 'emerald', 'amber', 'rose', 'slate'], 'adv-hide')}
        ${choice('density', ui.density, ['comfortable', 'compact'], 'adv-hide')}
        ${choice('layout', ui.layout, ['grid', 'list', 'tree', 'wall', 'map'], 'adv-hide')}
        <label class="field"><span>${esc(t('settings.language'))}</span>
          <select data-setting="language">
            ${available.map((code) => `<option value="${code}"${code === ui.language ? ' selected' : ''}>${code.toUpperCase()}</option>`).join('')}
          </select></label>
      </div>
      <p class="muted small">${esc(t('settings.modeHint'))}</p>
    </section>

    ${mapPanel()}

    <section class="panel" data-panel="dashboard" data-group="monitor">
      <h2>${esc(t('settings.dashboard'))}</h2>
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
      ${widgetPicker()}
    </section>

    ${sitesPanel()}

    ${examplePanel()}

    <section class="panel" data-panel="access" data-group="access">
      <h2>${esc(t('settings.security'))}</h2>
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
      <h2>${esc(t('users.title'))}</h2>
      ${usersUI.panel({ users: state.users, me: state.session?.user })}
    </section>` : ''}

    ${backupPanel()}

    <section class="panel" data-panel="help" data-group="help">
      <h2>${esc(t('settings.help'))}</h2>
      <p class="muted">${esc(t('settings.tourHint'))}</p>
      <div class="form-actions">
        <button class="btn" data-action="tour">${esc(t('settings.tourStart'))}</button>
      </div>
    </section>

    <section class="panel" data-panel="diagnostics" data-group="help">
      <h2>${esc(t('settings.diagnostics'))}</h2>
      <p class="muted">${esc(t('settings.doctorHint'))}</p>
      <div class="form-actions">
        <button class="btn" data-action="doctor-run">${esc(t('settings.doctorRun'))}</button>
      </div>
      <div id="doctor-result"></div>
      <div class="key-row">
        <code id="doctor-command">docker exec weatherglass weatherglass doctor</code>
        <button class="btn" data-action="copy-doctor">${esc(t('settings.copy'))}</button>
      </div>
    </section>

    <section class="panel" data-panel="about" data-group="help">
      <h2>${esc(t('settings.about'))}</h2>
      <dl class="kv">
        <dt>Version</dt><dd>${esc(runtime.version || '—')}</dd>
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
$('#settings-body').addEventListener('click', onSettingsClick);
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
    link.download = filenameFrom(res.headers.get('Content-Disposition')) || 'weatherglass-backup.json';
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
      openSiteEditor(null);
    } else if (action === 'site-edit') {
      const site = state.sites.find((s) => s.name === button.dataset.name);
      if (site) openSiteEditor(site);
    } else if (action === 'site-delete') {
      const site = state.sites.find((s) => s.name === button.dataset.name);
      if (site) await removeSite(site);
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
    return;
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
}

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
  const data = state.exampleData || { sensors: 0, results: 0, incidents: 0 };
  if (!confirmDialog(t('examples.confirm', { sensors: data.sensors }))) return;
  const removed = await api('/api/examples', { method: 'DELETE' });
  // The counts that come back are what was actually there, which is not
  // necessarily what the panel was showing: somebody may have added or removed a
  // sensor in another tab in between.
  toast(t('examples.removed', removed));
  state.exampleData = { sensors: 0, results: 0, incidents: 0 };
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
