// The map.
//
// Sites have coordinates, so they can be drawn where they are. This is a
// slippy map written out by hand — Web Mercator, tiles fetched straight from a
// tile server, drag and zoom — because the alternative is a mapping library,
// and a mapping library is a build step, a dependency and half a megabyte for
// what is forty lines of arithmetic.
//
// Two rules shape the whole file:
//
//   * The markers depend on the projection and on nothing else. Not on a tile
//     having loaded, not on the network being up. A homelab behind a firewall
//     that cannot reach the tile server still gets an honest picture: the
//     graticule the stylesheet paints underneath, every place in the right spot
//     relative to every other, and one sentence saying the tiles are missing.
//     An empty grey rectangle with no explanation is the failure mode this is
//     built to avoid.
//
//   * Colour already means status everywhere else in the dashboard, so it means
//     status here too, and the kind of place — in the house or somewhere else —
//     is carried by shape: a filled disc against a hollow diamond. Shape also
//     survives being printed, being photographed and being seen by somebody who
//     cannot tell red from green.

import { t } from './i18n.js';
import { esc } from './charts.js';
import { worstStatus } from './layouts.js';
import { overlayHost } from './tooltip.js';

const TILE = 256;
const MIN_ZOOM = 1;
const MAX_ZOOM = 19;
// Where a single site (or a cluster of them in one street) starts out. Going
// straight to 15 because the arithmetic allows it would drop the reader onto one
// rooftop with no idea which town it is in.
const START_ZOOM = 11;

const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);

// --- Web Mercator, in world pixels at a given zoom -------------------------

function lonToX(lon, z) {
  return ((lon + 180) / 360) * TILE * 2 ** z;
}

function latToY(lat, z) {
  const s = Math.sin((clamp(lat, -85.05112878, 85.05112878) * Math.PI) / 180);
  return (0.5 - Math.log((1 + s) / (1 - s)) / (4 * Math.PI)) * TILE * 2 ** z;
}

function xToLon(x, z) {
  return (x / (TILE * 2 ** z)) * 360 - 180;
}

function yToLat(y, z) {
  const n = Math.PI - (2 * Math.PI * y) / (TILE * 2 ** z);
  return (180 / Math.PI) * Math.atan(0.5 * (Math.exp(n) - Math.exp(-n)));
}

/**
 * tileAddress fills in a tile template.
 *
 * {z}, {x} and {y} are the address. {s} is optional and rotates through a, b and
 * c — the convention every tile server that offers subdomains uses, and the
 * reason a template is allowed to carry a placeholder this file otherwise has no
 * use for. The server refuses a template without the first three, so there is
 * nothing to check here.
 */
function tileAddress(template, z, x, y) {
  let url = template;
  if (url.includes('{s}')) {
    const subs = ['a', 'b', 'c'];
    url = url.replace('{s}', subs[Math.abs(x + y) % subs.length]);
  }
  return url.replace('{z}', z).replace('{x}', x).replace('{y}', y);
}

/**
 * mount draws the map into `host` and returns the function that stops it.
 *
 * Everything is built here rather than in a render function elsewhere: a map is
 * stateful in a way markup is not — a centre, a zoom, a transform mid-drag, a
 * popover hanging off a marker — and splitting that across two places is how the
 * two views of it drift apart.
 */
export function mount(host, opts = {}) {
  if (!host) return () => {};

  const sites = (opts.sites || []).filter((s) => placed(s));
  const sensorsBySite = new Map();
  for (const sensor of opts.sensors || []) {
    const key = sensor.site || '';
    if (!sensorsBySite.has(key)) sensorsBySite.set(key, []);
    sensorsBySite.get(key).push(sensor);
  }

  const view = { z: START_ZOOM, cx: 0, cy: 0 };
  let dragging = null;
  let settleTimer = null;
  let tilesOK = 0;
  let tilesFailed = 0;

  // --- the furniture -------------------------------------------------------

  const canvas = document.createElement('div');
  canvas.className = 'map-canvas';
  canvas.tabIndex = 0;
  canvas.setAttribute('role', 'application');
  canvas.setAttribute('aria-label', t('map.aria'));

  const tiles = document.createElement('div');
  tiles.className = 'map-tiles';
  const markers = document.createElement('div');
  markers.className = 'map-markers';
  canvas.append(tiles, markers);

  const note = document.createElement('p');
  note.className = 'map-note';
  note.hidden = true;

  const controls = document.createElement('div');
  controls.className = 'map-controls';
  controls.innerHTML = `
    <button type="button" data-map="in" title="${esc(t('map.zoomIn'))}" aria-label="${esc(t('map.zoomIn'))}">+</button>
    <button type="button" data-map="out" title="${esc(t('map.zoomOut'))}" aria-label="${esc(t('map.zoomOut'))}">−</button>
    <button type="button" data-map="fit" title="${esc(t('map.reset'))}" aria-label="${esc(t('map.reset'))}">⌂</button>`;

  const legend = document.createElement('div');
  legend.className = 'map-legend';
  legend.innerHTML = `
    <span><i class="legend-dot k-local"></i>${esc(t('map.legend.local'))}</span>
    <span><i class="legend-dot k-external"></i>${esc(t('map.legend.external'))}</span>
    <span><i class="legend-dot st-up"></i>${esc(t('status.up'))}</span>
    <span><i class="legend-dot st-down"></i>${esc(t('status.down'))}</span>`;

  const credit = document.createElement('div');
  credit.className = 'map-attribution';
  credit.innerHTML = attributionMarkup(opts.attribution, opts.tileURL);

  host.append(canvas, controls, legend, note, credit);

  // Anything this module puts outside its own container — the line of sites that
  // could not be drawn — is remembered so that unmount can take it away again.
  const outside = [];
  const unplaced = (opts.sites || []).filter((s) => !placed(s));
  if (unplaced.length) {
    const line = document.createElement('div');
    line.className = 'map-unplaced';
    line.innerHTML = `<strong>${esc(t('map.unplaced', { count: unplaced.length }))}</strong> ` +
      `${esc(t('map.unplacedHint'))} ${unplaced.map((s) => esc(s.name)).join(', ')}`;
    host.after(line);
    outside.push(line);
  }

  // --- the view ------------------------------------------------------------

  function fit() {
    const w = host.clientWidth || 640;
    const h = host.clientHeight || 400;
    if (!sites.length) {
      view.z = 2;
      view.cx = TILE * 2;
      view.cy = TILE * 2;
      return;
    }
    const top = Math.min(START_ZOOM, MAX_ZOOM);
    for (let z = top; z >= MIN_ZOOM; z--) {
      const xs = sites.map((s) => lonToX(s.lon, z));
      const ys = sites.map((s) => latToY(s.lat, z));
      const dx = Math.max(...xs) - Math.min(...xs);
      const dy = Math.max(...ys) - Math.min(...ys);
      if (dx <= w * 0.8 && dy <= h * 0.8) {
        view.z = z;
        view.cx = (Math.max(...xs) + Math.min(...xs)) / 2;
        view.cy = (Math.max(...ys) + Math.min(...ys)) / 2;
        return;
      }
    }
    view.z = MIN_ZOOM;
    view.cx = TILE;
    view.cy = TILE;
  }

  /** Keep the centre inside the world; centre it when the world is smaller. */
  function clampCentre() {
    const w = host.clientWidth || 640;
    const h = host.clientHeight || 400;
    const span = TILE * 2 ** view.z;
    view.cx = span <= w ? span / 2 : clamp(view.cx, w / 2, span - w / 2);
    view.cy = span <= h ? span / 2 : clamp(view.cy, h / 2, span - h / 2);
  }

  function draw() {
    const w = host.clientWidth || 640;
    const h = host.clientHeight || 400;
    const span = 2 ** view.z;
    const left = view.cx - w / 2;
    const top = view.cy - h / 2;

    tilesOK = 0;
    tilesFailed = 0;

    let tileHTML = '';
    const x0 = Math.floor(left / TILE);
    const x1 = Math.floor((left + w) / TILE);
    const y0 = Math.floor(top / TILE);
    const y1 = Math.floor((top + h) / TILE);
    for (let x = x0; x <= x1; x++) {
      if (x < 0 || x >= span) continue;
      for (let y = y0; y <= y1; y++) {
        if (y < 0 || y >= span) continue;
        const px = (x * TILE - left).toFixed(1);
        const py = (y * TILE - top).toFixed(1);
        tileHTML += `<img class="map-tile" alt="" draggable="false" loading="lazy" decoding="async"` +
          ` style="left:${px}px;top:${py}px" src="${esc(opts.tileURL ? tileAddress(opts.tileURL, view.z, x, y) : '')}">`;
      }
    }
    tiles.innerHTML = tileHTML;
    drawMarkers();
    updateNote();
  }

  /**
   * drawMarkers paints the pins, which is the only part of the map a probe can
   * change.
   *
   * Kept apart from draw() because the two are redrawn for different reasons: the
   * tiles move when somebody pans or zooms, the pins also change colour when a
   * sensor goes down. Rebuilding the tiles for that would re-request every image
   * on the screen once a minute, to draw the same pictures.
   *
   * The viewport is measured here rather than handed down from draw(). Both
   * callers want the same rectangle, and the one time it was passed as arguments
   * the height was left out of the list — which cost every pin on the map, because
   * a module is strict and reading a name that was never declared throws instead of
   * yielding undefined. A function that cannot be called with the wrong number of
   * arguments cannot fail that way.
   */
  function drawMarkers() {
    const w = host.clientWidth || 640;
    const h = host.clientHeight || 400;
    const left = view.cx - w / 2;
    const top = view.cy - h / 2;

    let markerHTML = '';
    for (const site of sites) {
      const px = lonToX(site.lon, view.z) - left;
      const py = latToY(site.lat, view.z) - top;
      if (px < -60 || px > w + 60 || py < -60 || py > h + 60) continue;
      const list = sensorsBySite.get(site.name) || [];
      const status = list.length ? worstStatus(list) : 'unknown';
      const kind = site.kind === 'external' ? 'k-external' : 'k-local';
      markerHTML += `<button type="button" class="site-marker ${kind} st-${esc(status)}"` +
        ` data-site="${esc(site.name)}" style="left:${px.toFixed(1)}px;top:${py.toFixed(1)}px"` +
        ` title="${esc(site.name)}">` +
        `<span class="site-marker-label">${esc(site.name)}</span></button>`;
    }
    markers.innerHTML = markerHTML;
  }

  /**
   * updateNote says out loud that the tiles are missing.
   *
   * The check is deliberately "did any tile fail while none succeeded" rather
   * than "did one tile fail": a refusal by the content policy or an unreachable
   * host makes every tile fail, and a single stray failure is not worth a
   * banner. It runs a moment after the last image settles, because the answer
   * is not known until the browser has stopped asking.
   */
  function updateNote() {
    clearTimeout(settleTimer);
    settleTimer = setTimeout(() => {
      const offline = tilesFailed > 0 && tilesOK === 0;
      note.hidden = !offline;
      if (offline) note.textContent = t('map.offline');
      else if (!sites.length) {
        note.hidden = false;
        note.textContent = t('map.empty');
      }
    }, 400);
  }

  // The one error listener for every tile there will ever be: images are
  // replaced wholesale on each redraw, so a listener per image would be a
  // listener per redraw. `error` does not bubble, hence the capture phase.
  tiles.addEventListener('error', (event) => {
    const img = event.target;
    if (!img || !img.classList || !img.classList.contains('map-tile')) return;
    img.hidden = true;
    tilesFailed += 1;
    updateNote();
  }, true);

  tiles.addEventListener('load', (event) => {
    const img = event.target;
    if (img && img.classList && img.classList.contains('map-tile')) tilesOK += 1;
  }, true);

  // --- zoom ----------------------------------------------------------------

  /**
   * zoomTo changes the zoom while keeping one point on the screen still.
   *
   * Without the anchor, zooming by wheel moves the thing the pointer was on out
   * from under it, and the reader has to chase it. The world coordinate under
   * the cursor is read at the old zoom and written back at the new one.
   */
  function zoomTo(next, px, py) {
    const z = clamp(Math.round(next), MIN_ZOOM, MAX_ZOOM);
    if (z === view.z) return;
    const w = host.clientWidth || 640;
    const h = host.clientHeight || 400;
    const lon = xToLon(view.cx - w / 2 + px, view.z);
    const lat = yToLat(view.cy - h / 2 + py, view.z);
    view.z = z;
    view.cx = lonToX(lon, z) - px + w / 2;
    view.cy = latToY(lat, z) - py + h / 2;
    clampCentre();
    draw();
  }

  function onWheel(event) {
    event.preventDefault();
    const box = canvas.getBoundingClientRect();
    zoomTo(view.z + (event.deltaY < 0 ? 1 : -1), event.clientX - box.left, event.clientY - box.top);
  }
  canvas.addEventListener('wheel', onWheel, { passive: false });

  controls.addEventListener('click', (event) => {
    const button = event.target.closest('[data-map]');
    if (!button) return;
    const w = host.clientWidth || 640;
    const h = host.clientHeight || 400;
    if (button.dataset.map === 'in') zoomTo(view.z + 1, w / 2, h / 2);
    else if (button.dataset.map === 'out') zoomTo(view.z - 1, w / 2, h / 2);
    else {
      fit();
      draw();
    }
  });

  canvas.addEventListener('keydown', (event) => {
    const step = 60;
    const pan = { ArrowLeft: [step, 0], ArrowRight: [-step, 0], ArrowUp: [0, step], ArrowDown: [0, -step] }[event.key];
    if (pan) {
      view.cx += pan[0];
      view.cy += pan[1];
      clampCentre();
      draw();
      event.preventDefault();
      return;
    }
    if (event.key === '+' || event.key === '=') zoomTo(view.z + 1, canvas.clientWidth / 2, canvas.clientHeight / 2);
    else if (event.key === '-' || event.key === '_') zoomTo(view.z - 1, canvas.clientWidth / 2, canvas.clientHeight / 2);
    else return;
    event.preventDefault();
  });

  // --- drag ----------------------------------------------------------------

  canvas.addEventListener('pointerdown', (event) => {
    // A press that starts on a marker is a press on a button, not a drag: if it
    // were treated as one, the redraw on pointerup would replace the marker
    // before the click could land on it.
    if (event.button !== 0 || event.target.closest('.site-marker')) return;
    dragging = { x: event.clientX, y: event.clientY, dx: 0, dy: 0 };
    canvas.classList.add('is-dragging');
    canvas.setPointerCapture(event.pointerId);
  });

  canvas.addEventListener('pointermove', (event) => {
    if (!dragging) return;
    dragging.dx = event.clientX - dragging.x;
    dragging.dy = event.clientY - dragging.y;
    // The whole layer moves as one compositor operation; nothing is recomputed
    // and no tile is fetched until the finger comes off.
    const shift = `translate(${dragging.dx}px, ${dragging.dy}px)`;
    tiles.style.transform = shift;
    markers.style.transform = shift;
  });

  function endDrag(event) {
    if (!dragging) return;
    const { dx, dy } = dragging;
    dragging = null;
    canvas.classList.remove('is-dragging');
    try {
      canvas.releasePointerCapture(event.pointerId);
    } catch {
      /* the pointer was already released */
    }
    tiles.style.transform = '';
    markers.style.transform = '';
    view.cx -= dx;
    view.cy -= dy;
    clampCentre();
    draw();
  }

  canvas.addEventListener('pointerup', endDrag);
  canvas.addEventListener('pointercancel', endDrag);

  // --- markers -------------------------------------------------------------

  let popover = null;

  function closePopover() {
    if (!popover) return;
    popover.remove();
    popover = null;
  }

  /**
   * openMarker lists what is at one place.
   *
   * A marker stands for a site, and a site is usually several sensors, so
   * opening "the" sensor would be picking one at random. The list is the honest
   * answer, and each entry opens that sensor exactly as clicking its card does.
   */
  function openMarker(site, anchor) {
    closePopover();
    const list = sensorsBySite.get(site.name) || [];
    const pop = document.createElement('div');
    pop.className = 'map-pop';
    pop.innerHTML = `
      <div class="map-pop-head">${esc(site.name)}</div>
      ${list.length ? list.map((sensor) => `
        <button class="map-pop-item" type="button" data-sensor="${sensor.id}">
          <span class="legend-dot st-${esc(sensor.status)}" aria-hidden="true"></span>
          <span class="map-pop-name">${esc(sensor.name)}</span>
        </button>`).join('')
        : `<p class="muted small">${esc(t('map.noSensors'))}</p>`}`;
    overlayHost().append(pop);

    const at = anchor.getBoundingClientRect();
    pop.style.left = '0px';
    pop.style.top = '0px';
    const box = pop.getBoundingClientRect();
    const left = clamp(at.left + at.width / 2 - box.width / 2, 8, window.innerWidth - box.width - 8);
    const top = at.bottom + 8 + box.height > window.innerHeight - 8
      ? Math.max(8, at.top - 8 - box.height)
      : at.bottom + 8;
    pop.style.left = `${left}px`;
    pop.style.top = `${top}px`;

    pop.addEventListener('click', (event) => {
      const button = event.target.closest('[data-sensor]');
      if (!button) return;
      closePopover();
      opts.onOpenSensor(Number(button.dataset.sensor));
    });
    popover = pop;
  }

  markers.addEventListener('click', (event) => {
    const button = event.target.closest('.site-marker');
    if (!button) return;
    // Otherwise the click also reaches the canvas, and in a dashboard that is
    // where "dismiss whatever is open" lives.
    event.stopPropagation();
    const site = sites.find((s) => s.name === button.dataset.site);
    if (!site) return;
    if (popover && popover.dataset.site === site.name) closePopover();
    else {
      openMarker(site, button);
      popover.dataset.site = site.name;
    }
  });

  function onDocumentClick(event) {
    if (popover && !event.target.closest('.map-pop')) closePopover();
  }
  function onKey(event) {
    if (event.key === 'Escape') closePopover();
  }
  document.addEventListener('click', onDocumentClick);
  document.addEventListener('keydown', onKey);

  function onResize() {
    clampCentre();
    draw();
  }
  window.addEventListener('resize', onResize);

  // --- go ------------------------------------------------------------------

  fit();
  clampCentre();
  draw();

  /**
   * refresh recolours the pins from a fresh sensor list.
   *
   * It touches neither the tiles nor the view. Both of those are where the reader
   * put them, and a probe every minute must not move the map under somebody who is
   * looking at it, nor make it ask its tile server for the same pictures again.
   * What a probe changes on this screen is the colour of a pin, and that is what is
   * redrawn.
   */
  function refresh(sensors) {
    sensorsBySite.clear();
    for (const sensor of sensors || []) {
      const key = sensor.site || '';
      if (!sensorsBySite.has(key)) sensorsBySite.set(key, []);
      sensorsBySite.get(key).push(sensor);
    }
    drawMarkers();
  }

  /** Stop the map: drop the listeners, the popover and anything it added outside. */
  function unmount() {
    clearTimeout(settleTimer);
    closePopover();
    canvas.removeEventListener('wheel', onWheel);
    window.removeEventListener('resize', onResize);
    document.removeEventListener('click', onDocumentClick);
    document.removeEventListener('keydown', onKey);
    for (const node of outside) node.remove();
    host.innerHTML = '';
  }

  return { unmount, refresh };
}

/** A site is drawable when it has both coordinates — half a pair is never stored. */
function placed(site) {
  return typeof site.lat === 'number' && typeof site.lon === 'number';
}

/**
 * attributionMarkup renders the credit line.
 *
 * The default tiles are OpenStreetMap's, and their licence requires the credit
 * to be visible and to link to the copyright page. When somebody points the map
 * at their own tile server the string they configured is written out as plain
 * text: it is their licence to satisfy, not ours to guess at.
 */
function attributionMarkup(attribution, tileURL) {
  const text = attribution || '© OpenStreetMap contributors';
  if (tileURL && tileURL.includes('openstreetmap.org')) {
    return `<a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">${esc(text)}</a>`;
  }
  return esc(text);
}
