// The map.
//
// Sites have coordinates, so they can be drawn where they are. This is a
// slippy map written out by hand — Web Mercator, drag and zoom — because the
// alternative is a mapping library, and a mapping library is a build step, a
// dependency and half a megabyte for what is forty lines of arithmetic.
//
// Underneath the markers it draws a world of its own — the coastlines, the lakes and
// rivers, the country and province borders and their names — out of datasets that live
// in this directory rather than off a tile server, and that is a deliberate
// change of direction rather than a missing feature. The free tile servers a
// third-party application used to be able to lean on are gone: OpenStreetMap's
// usage policy stopped allowing them in 2025, and what their servers answer an
// application now is a picture of a refusal — HTTP 200, so the browser counts it
// as a tile that loaded, and the reader gets to look at a block notice where a
// country should be. An operator who does want real tiles can still enter a tile
// address in Settings and this file honours it. An operator who does not gets a
// map, and nobody anywhere hears which addresses a homelab is watching.
//
// Three rules shape the whole file:
//
//   * The markers depend on the projection and on nothing else. Not on a tile
//     having loaded, not on the network being up. A homelab behind a firewall
//     still gets an honest picture: the graticule the stylesheet paints
//     underneath, every place in the right spot relative to every other, and one
//     sentence saying what is missing. An empty grey rectangle with no
//     explanation is the failure mode this is built on avoiding.
//
//   * The map's own world is under the tiles, so a configured tile server covers
//     it and an unreachable one uncovers it — the tile images hide
//     themselves on error, and the world is what is left. The order is the whole
//     mechanism; nothing here decides when to show one or the other.
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
import { SVG_NS, createOutline } from './outline.js';

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
 * svgLayer builds an empty map layer.
 *
 * The positioning is the stylesheet's — `position: absolute; inset: 0` — and the
 * contents are the layer module's. Building it here rather than in the module is what
 * lets the layer be in the right place in the stack from the first frame, whether or
 * not the module that fills it has been fetched yet.
 */
function svgLayer(className) {
  const node = document.createElementNS(SVG_NS, 'svg');
  node.setAttribute('class', className);
  node.setAttribute('aria-hidden', 'true');
  node.setAttribute('focusable', 'false');
  return node;
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

  const outline = createOutline();
  // The water, the border and name layers are built empty here and filled by modules
  // that are fetched afterwards. Those files are nearly two megabytes between them and
  // are needed only in this view, so importing them at the top of this file would make
  // every page load in the dashboard pay for a map nobody opened. They arrive a frame
  // or two after the first draw, and until they do the map is a map: coastlines,
  // graticule and pins, exactly as it was before they existed.
  //
  // `borders: false` is the setting, which is labelled "Draw country, province and
  // district borders" and is about the lines and the names and nothing else. It is
  // decided here rather than after the fetch, because a layer somebody has turned off
  // must not be downloaded either.
  const wanted = opts.borders !== false;
  // The water is not behind that setting. It is the ground: a lake is where the map says
  // there is a lake whether or not the borders are on, and a country whose lakes and
  // rivers disappear when somebody turns the borders off is a map that drew a different
  // world without being asked to. So mapwater.js is imported either way.
  // The names are a second file again, and `labels: false` — which is how the header's
  // mini map mounts — has to keep it out of the browser's queue rather than merely off
  // the screen. A layer that is imported and then not filled still costs the operator
  // 220 KB of names for a 150-pixel widget, and the comment in widgets.js promises it
  // does not. So the import itself is what is conditional here.
  const withLabels = wanted && opts.labels !== false;
  const waterNode = svgLayer('map-water');
  const borderNode = svgLayer('map-borders');
  const labelNode = document.createElement('div');
  labelNode.className = 'map-labels';
  const tiles = document.createElement('div');
  tiles.className = 'map-tiles';
  const markers = document.createElement('div');
  markers.className = 'map-markers';
  // The order is the design: coastlines, then the water inside them, then the country
  // borders, then the province borders, then whatever tiles somebody configured, then
  // the names, then the pins. See the second rule at the top of the file.
  canvas.append(outline.node, waterNode, borderNode, tiles, labelNode, markers);

  // null until the module that fills the layer arrives, and one of them may arrive
  // without the others.
  let water = null;
  let borders = null;
  let labels = null;

  Promise.all([
    import('./mapwater.js'),
    wanted ? import('./mapborders.js') : Promise.resolve(null),
    withLabels ? import('./maplabels.js') : Promise.resolve(null),
  ]).then(([waterMod, borderMod, labelMod]) => {
    // The map may have been unmounted, or replaced by another mount, while the three
    // files were in flight. Filling a layer that is no longer on the page would
    // build 97 000 points for nobody.
    if (!canvas.isConnected) return;
    water = waterMod.fill(waterNode);
    // null when the setting is off, which is why the border layer is asked before it is
    // filled and not merely left off the screen.
    borders = borderMod ? borderMod.fill(borderNode) : null;
    // The names are not drawn in the header's mini map: it is a picture of where
    // things are, 150 pixels wide, and a place name on it is a word on top of the
    // thing it names. labelMod is null there, which is why this asks before it
    // fills.
    labels = labelMod ? labelMod.fill(labelNode, { lonToX, latToY }) : null;
    draw();
  }).catch((error) => {
    // The coastlines, the graticule and the pins are all still here, which is why
    // this is a console line and not a failed map.
    console.error('probeglass: could not draw the water, the borders and the names', error);
  });

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
    <span><i class="legend-dot st-warn"></i>${esc(t('status.warn'))}</span>
    <span><i class="legend-dot st-down"></i>${esc(t('status.down'))}</span>`;

  const credit = document.createElement('div');
  credit.className = 'map-attribution';
  credit.innerHTML = attributionMarkup(opts.attribution, opts.tileURL);
  // With no tile server there is nobody to credit, and an empty bordered box in
  // the corner reads as something that failed to load.
  credit.hidden = credit.innerHTML === '';

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

    outline.setView(view.z, left, top);
    if (water) water.setView(view.z, left, top);
    if (borders) borders.setView(view.z, left, top);

    // Without a tile address there is no image to fetch, and this is not a loop
    // that quietly finds nothing to do: `src=""` resolves to the page's own
    // address, so it would have the browser request the dashboard again, once per
    // tile on the screen, at every zoom step. The tiles layer stays empty instead.
    let tileHTML = '';
    if (opts.tileURL) {
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
            ` style="left:${px}px;top:${py}px" src="${esc(tileAddress(opts.tileURL, view.z, x, y))}">`;
        }
      }
    }
    tiles.innerHTML = tileHTML;
    // After the tiles, because a name has to be readable over them, and before the
    // pins, because a pin is the thing the map is actually about.
    if (labels) labels.draw(view.z, left, top, w, h);
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
      // A site whose sensors are all inside maintenance windows is marked as such,
      // so that a pin which is red for a reason somebody arranged does not send
      // anybody to look at the map. Every one of them, not any: one sensor excused
      // among nine that are fine is not what the pin is about.
      const allExpected = list.length > 0 && list.every((c) => c.excuse);
      const kind = site.kind === 'external' ? 'k-external' : 'k-local';
      markerHTML += `<button type="button" class="site-marker ${kind} st-${esc(status)}${allExpected ? ' in-excused in-maintenance' : ''}"` +
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
    // and no tile is fetched until the finger comes off. The coastlines move with
    // it, on the same three lines of transform: they are a layer of the map like
    // any other, and a view that jumped back to where it started dragging would be
    // worse than no map at all. Every layer the map has is in this list — a border
    // or a name left behind would slide against the country it belongs to.
    const shift = `translate(${dragging.dx}px, ${dragging.dy}px)`;
    outline.node.style.transform = shift;
    waterNode.style.transform = shift;
    borderNode.style.transform = shift;
    tiles.style.transform = shift;
    labelNode.style.transform = shift;
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
    outline.node.style.transform = '';
    waterNode.style.transform = '';
    borderNode.style.transform = '';
    tiles.style.transform = '';
    labelNode.style.transform = '';
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
 * There is nothing to credit unless somebody configured a tile server, so with no
 * address this returns nothing at all and the corner stays empty. When there is
 * one, the string they configured is written out as plain text: it is their
 * licence to satisfy, not ours to guess at — with one exception. A tile address
 * containing openstreetmap.org is linked to the copyright page, because that
 * attribution is not optional under OSM's terms and a credit that does not link to
 * it does not satisfy them.
 */
function attributionMarkup(attribution, tileURL) {
  if (!tileURL) return '';
  const text = attribution || '© OpenStreetMap contributors';
  if (isOpenStreetMapTileURL(tileURL)) {
    return `<a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">${esc(text)}</a>`;
  }
  return esc(text);
}

/**
 * isOpenStreetMapTileURL says whether a tile address is served by OSM itself.
 *
 * The test is on the parsed host, not on a substring: "openstreetmap.org" may
 * appear in a query string, in a path, or in somebody else's domain such as
 * "openstreetmap.org.example.com". Tile templates also carry {s}, {z}, {x} and
 * {y}, which are not valid host characters, so they are replaced before the
 * address is parsed.
 */
function isOpenStreetMapTileURL(tileURL) {
  try {
    const parsed = new URL(String(tileURL).replace(/\{[^}]*\}/g, 'x'));
    const host = parsed.hostname.toLowerCase();
    return host === 'openstreetmap.org' || host.endsWith('.openstreetmap.org');
  } catch {
    return false;
  }
}
