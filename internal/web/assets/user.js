// The account area in the top right.
//
// Signing out used to live at the bottom of the Settings page, which is where
// nobody looks for it: it is not a setting, and the one moment it is wanted is
// the moment somebody is leaving. It moves here, with the name and the role
// beside it, and the three switches people actually flip day to day — theme,
// density, language — come along so that changing the theme does not mean a trip
// to a settings page.
//
// A leaf module, like users.js: it imports the catalogue, the escape helper and
// the overlay rule, and never app.js. Everything it needs from the dashboard
// arrives as an option, which is what keeps it testable on its own and keeps the
// dependency arrows one-way.

import { t, available, nameOf } from './i18n.js';
import { esc } from './charts.js';
import { overlayHost } from './tooltip.js';

// One menu at a time, module-wide: it is a popover, not a pane, and there is
// only ever one signed-in account.
let current = null; // { host, opts, menu }

/**
 * mount wires the button and paints it. Called once, from app.js.
 *
 * `session` is a function rather than the session object: signing in again
 * replaces the object, and a menu holding the old one would greet the new
 * account with the old account's name.
 */
export function mount(host, opts) {
  if (!host) return;
  current = { host, opts, menu: null };
  host.addEventListener('click', (event) => {
    // The document listener below would otherwise see this same click and close
    // the menu in the act of opening it.
    event.preventDefault();
    event.stopPropagation();
    toggle();
  });
  document.addEventListener('click', (event) => {
    if (!current.menu) return;
    if (event.target.closest('.user-menu') || event.target.closest('#user-menu-toggle')) return;
    close();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && current.menu) {
      close();
      host.focus();
    }
  });
  // An anchored popover left behind by a scroll is worse than one that closes.
  window.addEventListener('resize', close);
  document.addEventListener('scroll', close, true);
  paint();
}

/** Repaint the chip from the session. Safe to call whenever the session changes. */
export function paint() {
  if (!current) return;
  const user = account();
  const { host } = current;
  const avatar = host.querySelector('.avatar');
  const name = host.querySelector('.user-chip-name');
  const label = (user && user.username) || '';

  if (avatar) avatar.textContent = initials(label);
  if (name) name.textContent = label;
  const aria = user
    ? t('user.menuAria', { name: label })
    : t('user.menuAria', { name: t('user.unknown') });
  host.setAttribute('aria-label', aria);
  host.title = aria;
}

/**
 * repaint redraws the chip, and the menu with it while one is open.
 *
 * The menu is built when it is opened, which is the right answer for a theme
 * changed in another tab and the wrong one for a word. The language can be
 * switched from the menu itself: a menu that goes on saying "Theme", "Language"
 * and "Sign out" in the language the person has just left is a menu that has to
 * be closed and reopened before it can be read — and the control that is being
 * used is inside it.
 *
 * Rebuilding the markup throws away the focus along with the old nodes, so the
 * setting that had it gets it back. That is the select the person is in the
 * middle of using.
 */
export function repaint() {
  if (!current) return;
  paint();
  const { menu } = current;
  if (!menu) return;
  const focused = document.activeElement;
  const key = menu.contains(focused) && focused.dataset ? focused.dataset.setting : '';
  menu.innerHTML = menuMarkup();
  if (key) menu.querySelector(`[data-setting="${key}"]`)?.focus();
  // The menu is anchored under the chip, and its height changes with the words.
  place(menu);
}

/** The account the chip is describing, or null while the session is unknown. */
function account() {
  const session = current && current.opts.session();
  return (session && session.user) || null;
}

/**
 * initials is what goes in the avatar.
 *
 * A username is a single token — "anna", "j.k3r", "ops" — so the first letter is
 * the whole of it, and taking more would mean inventing a rule for where one
 * name ends and the next begins. The circle is a mark, not a name tag; the name
 * is written out beside it.
 */
function initials(username) {
  return (username || '?').trim().slice(0, 1).toUpperCase() || '?';
}

function toggle() {
  if (current.menu) close();
  else open();
}

function close() {
  if (!current || !current.menu) return;
  current.menu.remove();
  current.menu = null;
  current.host.setAttribute('aria-expanded', 'false');
}

function open() {
  const menu = document.createElement('div');
  menu.className = 'user-menu';
  menu.setAttribute('role', 'menu');
  // Built on open rather than kept in memory: a theme changed in another tab
  // arrives over the event stream and repaints the page, and a menu holding its
  // own copy of the old value would offer to change it back.
  menu.innerHTML = menuMarkup();
  menu.addEventListener('click', onMenuClick);
  menu.addEventListener('change', onMenuChange);

  // The overlay rule: inside an open dialog when there is one, on the body
  // otherwise. Nothing else may be attached to the body while a modal is up.
  overlayHost().append(menu);
  place(menu);

  current.menu = menu;
  current.host.setAttribute('aria-expanded', 'true');
  menu.querySelector('button')?.focus();
}

/** Put the menu under the chip, right-aligned, and on screen whatever the width. */
function place(menu) {
  const margin = 8;
  const at = current.host.getBoundingClientRect();
  menu.style.left = '0px';
  menu.style.top = '0px';
  const box = menu.getBoundingClientRect();

  let left = at.right - box.width;
  if (left + box.width > window.innerWidth - margin) left = window.innerWidth - margin - box.width;
  if (left < margin) left = margin;

  let top = at.bottom + 8;
  if (top + box.height > window.innerHeight - margin) top = Math.max(margin, at.top - 8 - box.height);

  menu.style.left = `${left}px`;
  menu.style.top = `${top}px`;
}

function menuMarkup() {
  const { ui, roleLabel } = current.opts;
  const user = account();
  const role = user ? (roleLabel ? roleLabel(user.role) : t(`users.role.${user.role}`)) : '';

  return `
    <div class="user-menu-head">
      <div class="user-menu-name">${esc((user && user.username) || t('user.unknown'))}</div>
      ${role ? `<div class="user-menu-role">${esc(role)}</div>` : ''}
    </div>
    ${field('theme', ui.theme, ['auto', 'dark', 'light'])}
    ${field('density', ui.density, ['comfortable', 'compact'])}
    ${available.length > 1 ? field('language', ui.language, available, nameOf) : ''}
    <div class="user-menu-sep"></div>
    <button class="user-menu-item" type="button" data-menu="settings" role="menuitem">
      ${esc(t('user.settings'))}
    </button>
    <button class="user-menu-item is-danger" type="button" data-menu="signout" role="menuitem">
      ${esc(t('nav.signOut'))}
    </button>`;
}

/**
 * field is one quick switch.
 *
 * It is the same `<select data-setting>` the settings page uses, so both go
 * through the one write path in app.js — including its rollback, which is what
 * makes a refused change put the old value back in the control that asked.
 *
 * `label` is how an option is written, and it defaults to the message
 * `settings.<name>.<value>` — the naming the whole settings page uses. The
 * language switch is the one caller that hands in its own, because a language is
 * named in its own language and cannot be looked up in the one being left.
 */
function field(name, value, options, label = (o) => t(`settings.${name}.${o}`)) {
  const label0 = t(`settings.${name}`);
  return `
    <label class="user-menu-field"><span>${esc(label0)}</span>
      <select data-setting="${name}" aria-label="${esc(label0)}">
        ${options.map((o) => `<option value="${o}"${o === value ? ' selected' : ''}>${esc(label(o))}</option>`).join('')}
      </select></label>`;
}

function onMenuChange(event) {
  const select = event.target.closest('[data-setting]');
  if (!select) return;
  void current.opts.onChange(select.dataset.setting, select.value, select);
}

function onMenuClick(event) {
  const button = event.target.closest('[data-menu]');
  if (!button) return;
  // Close first: both of these leave the chip, and a menu left hanging over the
  // page it just navigated away from is a stray element nobody can dismiss.
  const what = button.dataset.menu;
  close();
  if (what === 'settings') current.opts.onSettings();
  else if (what === 'signout') void current.opts.onSignOut();
}
