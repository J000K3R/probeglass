// Field help.
//
// Every field used to carry a paragraph underneath explaining what it was for.
// Read once, noise forever after — and on the sensor form the explanations were
// longer than the form. They move behind a small "?" beside the label instead:
// one click, or Enter on the button, opens a popover with what the field is and
// one example value, and Escape or an outside click closes it.
//
// The popover is not hover-only. A tooltip that needs a mouse is unreachable on
// a tablet and invisible to a keyboard, and this is a dashboard people open on
// whatever is nearest.

import { t } from './i18n.js';

let open = null;

function close() {
  if (!open) return;
  open.panel.remove();
  open.trigger.setAttribute('aria-expanded', 'false');
  open = null;
}

// Field help comes from two places: the dashboard's own catalogue, and the
// channel definitions the server sends (internal/notify), which carry their own
// Help and Example sentences. The attribute holds a key or a literal, and one
// flag says which — so a future catalogue key can never accidentally swallow a
// server sentence that happens to look like one.
function textOf(trigger, attr) {
  const raw = trigger.dataset[attr];
  if (!raw) return '';
  return trigger.dataset.helpLiteral ? raw : t(raw);
}

/** Place the panel below its trigger, flipping up and clamping on screen. */
function position(panel, trigger) {
  const margin = 10;
  // Measure at the origin first: the panel is fixed, so this gives its real size
  // without the placement affecting it.
  panel.style.left = '0px';
  panel.style.top = '0px';
  const box = panel.getBoundingClientRect();
  const at = trigger.getBoundingClientRect();

  let left = at.left;
  if (left + box.width > window.innerWidth - margin) left = window.innerWidth - margin - box.width;
  if (left < margin) left = margin;

  let top = at.bottom + 6;
  if (top + box.height > window.innerHeight - margin) top = at.top - 6 - box.height;
  if (top < margin) top = margin;

  panel.style.left = `${left}px`;
  panel.style.top = `${top}px`;
}

// Where an overlay is attached while a dialog is open.
//
// A modal <dialog> lives in the top layer and paints its own backdrop above
// everything in the normal flow, so anything appended to <body> while the sensor
// form is open is drawn underneath that backdrop and never seen. Hanging it
// inside the open dialog puts it in the same top-layer subtree as the form it
// belongs to. The overlays are positioned `fixed`, so being nested changes
// nothing about where they land.
//
// Both overlays in the dashboard use this: the field help popover and, through
// app.js, the toasts — a test result that arrives while the form is open is the
// one toast nobody can afford to miss.
export function overlayHost(fallback = document.body) {
  return document.querySelector('dialog[open]') || fallback;
}

function show(trigger) {
  close();
  const panel = document.createElement('div');
  panel.className = 'help-pop';
  panel.setAttribute('role', 'tooltip');

  const body = textOf(trigger, 'help');
  if (body) {
    const el = document.createElement('p');
    el.className = 'help-body';
    el.textContent = body;
    panel.append(el);
  }
  const example = textOf(trigger, 'helpExample');
  if (example) {
    const row = document.createElement('p');
    row.className = 'help-eg';
    const label = document.createElement('span');
    label.textContent = t('help.example');
    const code = document.createElement('code');
    code.textContent = example;
    row.append(label, code);
    panel.append(row);
  }

  overlayHost().append(panel);
  position(panel, trigger);
  trigger.setAttribute('aria-expanded', 'true');
  open = { trigger, panel };
}

/** Wire the delegated listeners. Called once, from app.js. */
export function init() {
  document.addEventListener('click', (event) => {
    const trigger = event.target.closest('.help-dot');
    if (trigger) {
      // Inside a <label>, a click would otherwise also focus the input; and the
      // button is emitted with type="button" so it can never submit the form.
      event.preventDefault();
      event.stopPropagation();
      if (open && open.trigger === trigger) close();
      else show(trigger);
      return;
    }
    // A click anywhere else dismisses, including on another field.
    if (open && !event.target.closest('.help-pop')) close();
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && open) {
      const trigger = open.trigger;
      close();
      trigger.focus();
    }
  });

  // A popover anchored to a moving element would be left behind by a scroll.
  window.addEventListener('resize', close);
  document.addEventListener('scroll', close, true);
}

/**
 * helpButton renders the "?" that opens the popover, for text that lives in the
 * dashboard's catalogue: `key` names the explanation, and its example lives at
 * `<key>Example` when there is one.
 */
export function helpButton(key, exampleKey) {
  return `<button type="button" class="help-dot" data-help="${key}"` +
    `${exampleKey ? ` data-help-example="${exampleKey}"` : ''} ` +
    `aria-expanded="false" aria-label="${t('help.what')}">?</button>`;
}

/**
 * helpButtonText is the same control for text that arrived from the server,
 * such as the channel field descriptions in internal/notify.
 */
export function helpButtonText(help, example) {
  if (!help && !example) return '';
  return `<button type="button" class="help-dot" data-help-literal="1"` +
    ` data-help="${escAttr(help || '')}"` +
    `${example ? ` data-help-example="${escAttr(example)}"` : ''} ` +
    `aria-expanded="false" aria-label="${t('help.what')}">?</button>`;
}

function escAttr(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;');
}
