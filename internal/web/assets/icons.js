// Icons: the symbol beside a sensor, a site or a group, and the field that picks
// one.
//
// A symbol is one string in three forms, and the server is where that is decided:
// empty for none, `builtin:router` for one of the drawings that ship inside the
// binary, and `upload:7` for an image somebody put in this installation's database.
// The prefix is the point — a built-in is a name the program knows and an upload is
// a row in one database, and a bare `router` that meant both would be ambiguous the
// first time somebody uploaded an image and called it that.
//
// The two are drawn differently on purpose, and that difference is the whole reason
// the forms are told apart here at all. A built-in is line art, so it is served
// through a CSS mask and coloured by the text around it: one file then works in
// both themes and under all six accents, and it looks like part of the dashboard
// rather than like a picture pasted onto it. An upload is somebody's own image with
// its own colours — a logo, a photograph of the actual box — and masking that would
// throw away the one thing it was chosen for. So it is drawn as it is.
//
// The catalogue — the uploaded images, the built-in names and the limits — is
// fetched the first time a picker is opened and not on page load: a dashboard
// nobody is editing should not pay for a list of images.

import { t } from './i18n.js';
import { esc } from './charts.js';

const BUILTIN = 'builtin:';
const UPLOAD = 'upload:';

// A placeholder where a symbol would be, so that "none" is a visible choice rather
// than a gap: a dashed square in the slot, and the words "No icon" beside it.
const EMPTY_SLOT = '<span class="icon icon-empty" aria-hidden="true"></span>';

// catalogue is everything the picker offers, filled by load(). `loading` holds the
// request in flight, so that ten pickers opened at once ask for one list.
let catalogue = { icons: [], builtin: [], mediaTypes: [], maxBytes: 0, maxSide: 0 };
let loading = null;
let loaded = false;

// panelSeq numbers the search field's label id, and searchSeq orders the answers
// so that a slow reply to "pi" cannot overwrite a fast one to "pihole".
let panelSeq = 0;
let searchSeq = 0;
let searchTimer = null;

// api, toast and confirmDialog arrive from app.js in mount(), the way the other
// modules get them: the dashboard has one request function with one timeout policy
// and one way of asking a question, and a second of either here would be a second
// thing to keep in step.
let api = null;
let toast = () => {};
let confirmDialog = (message) => window.confirm(message);

/** The URL a symbol is drawn from, or "" when there is nothing to draw. */
export function iconURL(ref) {
  if (!ref) return '';
  if (ref.startsWith(BUILTIN)) {
    const name = ref.slice(BUILTIN.length);
    // Only a plain name can become a path: a reference carrying a slash or a dot is
    // not one of ours, and is not turned into a URL that would fetch something else.
    return /^[a-z0-9-]+$/.test(name) ? `/assets/icons/${name}.svg` : '';
  }
  if (ref.startsWith(UPLOAD)) {
    const id = ref.slice(UPLOAD.length);
    return /^[0-9]+$/.test(id) ? `/api/icons/${id}` : '';
  }
  return '';
}

/** isBuiltin reports whether a symbol is a drawing that follows the theme. */
export function isBuiltin(ref) { return typeof ref === 'string' && ref.startsWith(BUILTIN); }

/** The word for a symbol: the built-in's name, or the uploaded image's own. */
export function iconLabel(ref) {
  if (!ref) return '';
  if (isBuiltin(ref)) return t(`icons.name.${ref.slice(BUILTIN.length)}`);
  if (ref.startsWith(UPLOAD)) {
    const id = Number(ref.slice(UPLOAD.length));
    const found = catalogue.icons.find((i) => i.id === id);
    if (found && found.name) return found.name;
  }
  return '';
}

/**
 * iconMarkup draws one symbol.
 *
 * A drawing is a `<span>` with the file as a mask, so its colour is the colour of
 * the text it sits beside; an image is an `<img>`. Both carry the same classes, so
 * one rule in the stylesheet sizes them.
 *
 * It is `aria-hidden` by default: a symbol sits beside the name it belongs to, and
 * the name is what a screen reader should read. Where there is no name — the
 * picker's grid — the caller passes `title` and gets one.
 */
export function iconMarkup(ref, { size = 18, cls = '', title = '' } = {}) {
  const url = iconURL(ref);
  if (!url) return '';
  const attr = title ? ` title="${esc(title)}"` : '';
  const box = `width:${size}px;height:${size}px`;
  if (isBuiltin(ref)) {
    return `<span class="icon icon-drawn${cls ? ' ' + cls : ''}" style="--icon:url('${url}');${box}"${attr} aria-hidden="true"></span>`;
  }
  return `<img class="icon icon-image${cls ? ' ' + cls : ''}" src="${esc(url)}" style="${box}" alt=""${attr}>`;
}

/**
 * iconFieldMarkup is the picker as it stands in a form: the current symbol, the
 * value the form will send, and an empty panel that is filled the first time it is
 * opened.
 *
 * The value travels in a hidden input named like any other field, so a form reads
 * it the way it reads everything else and readSensorForm needs no special case.
 */
export function iconFieldMarkup(ref = '', { name = 'icon' } = {}) {
  const label = ref ? iconLabel(ref) : t('icons.none');
  return `
    <div class="icon-field" data-icon-field>
      <button type="button" class="icon-button" data-icon-open aria-haspopup="true" aria-expanded="false">
        <span class="icon-slot">${iconMarkup(ref, { size: 18 }) || EMPTY_SLOT}</span>
        <span class="icon-button-label" data-icon-label>${esc(label)}</span>
        <svg class="icon-caret" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
          <path d="M2.5 4.5 6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8"
                stroke-linecap="round" stroke-linejoin="round"/></svg>
      </button>
      <input type="hidden" name="${esc(name)}" value="${esc(ref)}">
      <div class="icon-panel" hidden></div>
    </div>`;
}

/** readIconField is the value a form's picker holds, "" when the form has none. */
export function readIconField(form, name = 'icon') {
  const input = form && form.elements ? form.elements[name] : null;
  return input ? input.value : '';
}

/**
 * groupOf is what this installation knows about a group beyond its name.
 *
 * A group is a word typed on a sensor, so the answer is often null: the sensor
 * names a group nobody has ever said anything about, which is the ordinary state of
 * a fresh installation and has to draw the way it always did.
 */
export function groupOf(groups, name) {
  return (groups || []).find((g) => g.name === name) || null;
}

/**
 * colourAttr is the attribute a heading carries when the group has a colour of its
 * own, and nothing at all when it has not.
 *
 * Empty rather than a default, so that a heading nobody has given a colour keeps the
 * colour the stylesheet gives it — see the rule in style.css.
 */
export function colourAttr(group) {
  return group && group.colour ? ` data-colour="${esc(group.colour)}"` : '';
}

/** groupIcon is the symbol for a group's heading, "" when it has none. */
export function groupIcon(group, size = 16) {
  return group ? iconMarkup(group.icon, { size, cls: ' icon-lead' }) : '';
}

/** load fetches the catalogue once, or again after an upload or a delete. */
async function load(force = false) {
  if (loaded && !force) return catalogue;
  if (loading) return loading;
  loading = api('/api/icons')
    .then((data) => {
      catalogue = {
        icons: data.icons || [],
        builtin: data.builtin || [],
        mediaTypes: data.media_types || [],
        maxBytes: data.max_bytes || 0,
        maxSide: data.max_side || 0,
      };
      loaded = true;
      return catalogue;
    })
    .finally(() => { loading = null; });
  return loading;
}

/**
 * panelMarkup is the open picker: "no icon", the built-in drawings, and the
 * uploaded images with the way to add one.
 *
 * The built-ins carry their names and not only their drawing. A grid of
 * twenty-six line drawings with no words under them is a guessing game, and the set
 * is a rack in a sensible order, so the words double as the order somebody reads it
 * in.
 */
function panelMarkup(current) {
  // An id for the search field's label. It is made per render rather than fixed
  // because two forms with a picker can be in the document at once — the sensor
  // dialog and the group editor — and two elements sharing an id is the kind of
  // mistake that only shows up as a label pointing at the wrong field.
  const panelID = `icon-search-${++panelSeq}`;
  const builtin = catalogue.builtin.map((name) => {
    const ref = BUILTIN + name;
    return `<button type="button" class="icon-choice${ref === current ? ' is-current' : ''}"
      data-icon-set="${esc(ref)}" title="${esc(t(`icons.name.${name}`))}">
      ${iconMarkup(ref, { size: 20 })}<span>${esc(t(`icons.name.${name}`))}</span></button>`;
  }).join('');

  const uploaded = catalogue.icons.length
    ? catalogue.icons.map((ic) => `
      <span class="icon-choice-wrap">
        <button type="button" class="icon-choice icon-choice-wide${UPLOAD + ic.id === current ? ' is-current' : ''}"
          data-icon-set="${UPLOAD}${ic.id}" title="${esc(ic.name || String(ic.id))}">
          ${iconMarkup(UPLOAD + ic.id, { size: 20 })}
          <span>${esc(ic.name || t('icons.uploadedGroup'))}</span></button>
        <button type="button" class="icon-choice-x" data-icon-delete="${ic.id}"
          title="${esc(t('icons.delete'))}" aria-label="${esc(t('icons.delete'))}">&times;</button>
      </span>`).join('')
    : `<div class="empty empty-inline"><p>${esc(t('icons.empty'))}</p></div>`;

  // The accept list is the server's, so a format the upload route would refuse is
  // not offered by the file picker either.
  const accept = catalogue.mediaTypes.join(',');
  return `
    <div class="icon-search">
      <label class="icon-search-label" for="${esc(panelID)}">${esc(t('icons.search'))}</label>
      <input type="search" id="${esc(panelID)}" data-icon-query
             placeholder="${esc(t('icons.searchPlaceholder'))}" autocomplete="off" spellcheck="false">
      <div class="icon-grid icon-grid-online" data-icon-results hidden></div>
      <p class="icon-search-note" data-icon-note hidden></p>
    </div>
    <div class="icon-grid">
      <button type="button" class="icon-choice icon-choice-none${current ? '' : ' is-current'}"
        data-icon-set="">${EMPTY_SLOT}<span>${esc(t('icons.none'))}</span></button>
    </div>
    <p class="icon-panel-title">${esc(t('icons.builtinGroup'))}</p>
    <div class="icon-grid">${builtin}</div>
    <p class="icon-panel-title">${esc(t('icons.uploadedGroup'))}</p>
    <div class="icon-grid icon-grid-uploads">${uploaded}</div>
    <div class="icon-panel-foot">
      <label class="btn btn-ghost icon-upload">
        <input type="file" accept="${esc(accept)}" data-icon-upload hidden>
        ${esc(t('icons.upload'))}
      </label>
      <span class="muted small">${esc(t('icons.hint', {
        kb: Math.round(catalogue.maxBytes / 1024), px: catalogue.maxSide,
      }))}</span>
    </div>
    <p class="icon-credit">${esc(t('icons.credit'))}
      <a href="https://dashboardicons.com/" target="_blank" rel="noopener noreferrer">dashboardicons.com</a></p>`;
}

/** fieldOf is the picker a node belongs to. */
const fieldOf = (node) => node.closest('[data-icon-field]');

/** valueOf is what a picker currently holds. */
function valueOf(field) {
  const input = field.querySelector('input[type="hidden"]');
  return input ? input.value : '';
}

/** closePanel puts a picker back the way it was found. */
function closePanel(field) {
  const panel = field.querySelector('.icon-panel');
  if (!panel || panel.hidden) return;
  panel.hidden = true;
  panel.innerHTML = '';
  const button = field.querySelector('[data-icon-open]');
  if (button) button.setAttribute('aria-expanded', 'false');
}

/** closeAllPanels closes every open picker, since only one may be open at a time. */
function closeAllPanels(except) {
  for (const field of document.querySelectorAll('[data-icon-field]')) {
    if (field !== except) closePanel(field);
  }
}

/** setField writes a value into the picker: the hidden input, the slot and the word. */
function setField(field, ref) {
  const input = field.querySelector('input[type="hidden"]');
  if (input) input.value = ref;
  const slot = field.querySelector('.icon-slot');
  if (slot) slot.innerHTML = iconMarkup(ref, { size: 18 }) || EMPTY_SLOT;
  const label = field.querySelector('[data-icon-label]');
  if (label) label.textContent = ref ? iconLabel(ref) : t('icons.none');
}

/** renderPanel fills an open picker with the catalogue. */
function renderPanel(field, current) {
  const panel = field.querySelector('.icon-panel');
  if (!panel) return;
  panel.innerHTML = panelMarkup(current);
}

/**
 * upload posts one file and selects it.
 *
 * The body is FormData, which is what a file input goes into without the dashboard
 * encoding anything, and nothing here says what the image is: the filename and the
 * type are the browser's guesses, and the server reads the bytes and records what it
 * found. A refusal comes back as a sentence — "an icon has to be a PNG, a JPEG or a
 * GIF; this looks like text/plain" — and is shown as it is, because it already says
 * what to do about it.
 */
async function upload(field, file) {
  field.classList.add('is-busy');
  const body = new FormData();
  body.append('file', file, file.name);
  try {
    const answer = await api('/api/icons', { method: 'POST', body });
    await load(true);
    setField(field, UPLOAD + answer.id);
    renderPanel(field, UPLOAD + answer.id);
    toast(t('icons.uploadDone'));
  } catch (err) {
    toast(err.message, 'error');
  } finally {
    field.classList.remove('is-busy');
  }
}

/**
 * remove deletes an uploaded image, asking twice when something still wears it.
 *
 * The second question is asked in the words of what will happen — the image goes
 * and the rows that pointed at it are emptied — rather than as "try again?", and it
 * is only asked because the server refuses the first attempt with a 409. Nothing
 * breaks either way: the reference is a string, so an image that is gone simply
 * draws nothing.
 */
async function remove(field, id) {
  const ic = catalogue.icons.find((i) => i.id === id);
  const name = (ic && ic.name) || String(id);
  if (!confirmDialog(t('icons.deleteConfirm', { name }))) return;
  try {
    await api(`/api/icons/${id}`, { method: 'DELETE' });
  } catch (err) {
    if (err.status !== 409) {
      toast(err.message, 'error');
      return;
    }
    if (!confirmDialog(t('icons.deleteInUse', { name }))) return;
    try {
      await api(`/api/icons/${id}?clear=1`, { method: 'DELETE' });
    } catch (again) {
      toast(again.message, 'error');
      return;
    }
  }
  await load(true);
  // This form's own value may have been the image that just went: the server has
  // taken it off every row that wore it, and the form in front of the person is one
  // of them.
  const current = valueOf(field);
  if (current === UPLOAD + id) setField(field, '');
  renderPanel(field, valueOf(field));
  toast(t('icons.deleted'));
}

/**
 * search looks the term up in the online catalogue and draws what comes back.
 *
 * The catalogue is the one at dashboardicons.com, and the server asks it — not the
 * browser. That is what keeps the dashboard's content policy as it is, and it means
 * an installation with no route out gets one sentence instead of a grid of broken
 * images: the answer carries an `offline` flag, and the sentence under the field
 * says where the icons are instead.
 *
 * Choosing one is an import, not a reference. The picture is fetched once and
 * stored as an ordinary uploaded icon, so from then on it draws with no network at
 * all and travels in the one SQLite file a backup copies.
 *
 * Only the last search may answer: the sequence number is compared after the await,
 * because typing "pihole" sends a request per prefix and the answers do not
 * necessarily arrive in order.
 */
async function search(field, term) {
  const results = field.querySelector('[data-icon-results]');
  const note = field.querySelector('[data-icon-note]');
  if (!results || !note) return;
  const seq = ++searchSeq;
  if (term.length < 2) {
    results.hidden = true;
    results.innerHTML = '';
    note.hidden = true;
    return;
  }
  results.hidden = false;
  results.innerHTML = `<div class="loading loading-inline">${esc(t('icons.searching'))}</div>`;
  try {
    const data = await api(`/api/icons/search?q=${encodeURIComponent(term)}`);
    if (seq !== searchSeq) return;
    if (data.offline) {
      results.hidden = true;
      results.innerHTML = '';
      note.hidden = false;
      note.innerHTML = `${esc(t('icons.searchOffline'))} ` +
        `<a href="https://dashboardicons.com/" target="_blank" rel="noopener noreferrer">dashboardicons.com</a>`;
      return;
    }
    note.hidden = true;
    if (!data.results.length) {
      results.innerHTML = `<div class="empty empty-inline"><p>${esc(t('icons.searchEmpty'))}</p></div>`;
      return;
    }
    results.innerHTML = data.results.map((icon) => `
      <button type="button" class="icon-choice icon-choice-wide"
        data-icon-import="${esc(icon.name)}" title="${esc(icon.name)}">
        <img class="icon icon-image" src="/api/icons/preview/${encodeURIComponent(icon.name)}"
             width="20" height="20" alt="" loading="lazy">
        <span>${esc(icon.name)}</span>
      </button>`).join('');
  } catch (err) {
    if (seq !== searchSeq) return;
    results.hidden = true;
    note.hidden = false;
    note.textContent = err.message;
  }
}

/**
 * importOnline takes one icon out of the catalogue and stores it like an upload.
 *
 * The button it was clicked on is disabled while the request is out, because the
 * import is a network round trip and a second click would store the picture twice.
 */
async function importOnline(field, name, button) {
  if (button) button.disabled = true;
  try {
    const answer = await api('/api/icons/import', { method: 'POST', body: { name } });
    await load(true);
    setField(field, UPLOAD + answer.id);
    closePanel(field);
    toast(t('icons.importDone', { name }));
  } catch (err) {
    toast(err.message, 'error');
  } finally {
    if (button) button.disabled = false;
  }
}

/**
 * mount wires the pickers up.
 *
 * One listener set on the document rather than one per field, because the fields are
 * built inside dialogs that are re-rendered on every open: a listener attached to
 * the markup would be attached to an element that is gone by the time the next
 * dialog appears.
 */
export function mount(options) {
  api = options.api;
  if (options.toast) toast = options.toast;
  if (options.confirm) confirmDialog = options.confirm;

  document.addEventListener('click', (event) => {
    const open = event.target.closest('[data-icon-open]');
    if (open) {
      const field = fieldOf(open);
      const panel = field.querySelector('.icon-panel');
      const wasOpen = panel && !panel.hidden;
      closeAllPanels(field);
      if (wasOpen) {
        closePanel(field);
        return;
      }
      open.setAttribute('aria-expanded', 'true');
      panel.hidden = false;
      panel.innerHTML = `<div class="loading loading-inline">${esc(t('icons.loading'))}</div>`;
      load()
        .then(() => renderPanel(field, valueOf(field)))
        .catch((err) => { panel.innerHTML = `<p class="form-error">${esc(err.message)}</p>`; });
      return;
    }

    const pick = event.target.closest('[data-icon-set]');
    if (pick) {
      const field = fieldOf(pick);
      setField(field, pick.dataset.iconSet);
      closePanel(field);
      return;
    }

    // An icon from the online catalogue: stored first, then chosen. It becomes an
    // upload in every sense, so the rest of the picker treats it as one.
    const online = event.target.closest('[data-icon-import]');
    if (online) {
      importOnline(fieldOf(online), online.dataset.iconImport, online);
      return;
    }

    const drop = event.target.closest('[data-icon-delete]');
    if (drop) {
      remove(fieldOf(drop), Number(drop.dataset.iconDelete));
      return;
    }

    // A click anywhere else closes the open picker, which is what makes the panel
    // behave like a menu rather than like part of the form.
    if (!event.target.closest('.icon-panel')) closeAllPanels(null);
  });

  document.addEventListener('change', (event) => {
    const input = event.target.closest('[data-icon-upload]');
    if (!input || !input.files || !input.files.length) return;
    upload(fieldOf(input), input.files[0]);
    // Cleared so that choosing the same file twice fires a change both times.
    input.value = '';
  });

  // Typing searches the online catalogue, a moment after the typing stops. Two
  // characters is the floor: one letter matches most of the catalogue and would be
  // a request per keystroke for a list nobody can read.
  document.addEventListener('input', (event) => {
    const input = event.target.closest('[data-icon-query]');
    if (!input) return;
    const field = fieldOf(input);
    const term = input.value.trim();
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => search(field, term), 350);
  });

  document.addEventListener('keydown', (event) => {
    // Enter in the search field is a search, not the dialog's save: the picker
    // lives inside a form, and a form submits on Enter unless somebody says no.
    const query = event.target.closest('[data-icon-query]');
    if (query && event.key === 'Enter') {
      event.preventDefault();
      clearTimeout(searchTimer);
      search(fieldOf(query), query.value.trim());
      return;
    }
    if (event.key === 'Escape') closeAllPanels(null);
  });}
