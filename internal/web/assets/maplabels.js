// The names of the countries, the first-order administrative areas — states, provinces,
// the German Bundesländer — and the cities, drawn on the map.
//
// They come from labels.js, regions.js and cities.js, and they are drawn in screen
// coordinates: one absolutely positioned span each, in a layer over the tiles. The
// alternative — text in the SVG that is scaled with the map — would make a name bigger
// as somebody zooms in, and a 40-pixel "Bayern" is not a map anybody wants.
//
// Three things decide what is on screen, and they are all here rather than spread
// over the callers:
//
//   * The zoom a name belongs at. It comes from the data, not from a rule made up
//     here: Natural Earth says when its own map first writes each name, and each
//     province name carries the rank that puts it in the same band as its border.
//
//   * Whether it is on screen at all. Anything outside the viewport is skipped before
//     it is built, which is what keeps a world of 4 700 names cheap: at zoom 11 the
//     viewport holds a handful of them.
//
//   * Whether it collides. Names are laid down most important first — countries, then
//     provinces, then cities, and within each the source's own ranking — and one that
//     would overlap something already placed is dropped. A map with half the names on
//     it is a map; a map with all of them is a grey smear.
//
// The three lists are also three levels of zoom, and that is the other reason for the
// order: a country name arrives at zoom 2 and stays, a province name at 4 or 5, a city
// at 6 and deeper. Vorarlberg is one province and, at zoom 8, one city — Bregenz. Where
// two of them want the same square inch the deeper one gives way, which is the right way
// round: "Vorarlberg" over a village is a worse map than a village with no label.
//
// What a name takes up is measured, not guessed: the candidates are put on the page and
// their boxes asked for in one batch, and the ones that would cover a name already kept
// come out again. See draw() for why that is one layout and not one per name — an
// estimate from the character count was the first attempt and it drew names on top of
// each other, because a word of wide letters is wider than its length suggests.

import { WORLD_LABEL_NAMES, WORLD_LABEL_POINTS } from './labels.js';
import { cursor } from './outline.js';
import { WORLD_REGION_NAMES, WORLD_REGION_POINTS } from './regions.js';
import { WORLD_CITY_NAMES, WORLD_CITY_POINTS } from './cities.js';

// The gap left between two names that are both drawn. Each box is measured off the
// page and is therefore exact, but two words touching edge to edge still read as one
// line of text, so a little air is left between the boxes before one is accepted.
const LABEL_GAP = 2;

// A name whose anchor is further than this outside the view is not built at all. It is
// a margin rather than the name's own box because the box is not known until the name
// is on the page, and what this is for is to keep a world of 4 700 names cheap: at any
// zoom somebody is actually looking at, the viewport holds a handful of them.
const EDGE_MARGIN = 120;

// However deep the zoom goes, this many names is the most that will be built in one
// frame. It is a ceiling rather than a target — the collision pass is what decides how
// many of them are kept — and it exists so that a corrupt or unusual file cannot turn
// a pan into a stall.
const MAX_LABELS = 400;

// Nothing is drawn above the shallowest zoom in the files, and at zoom 1 the whole
// world is 512 pixels wide: a country name there is wider than the country.
const MIN_LABEL_ZOOM = 2;

/**
 * read unpacks one name stream.
 *
 * The format is documented in cmd/worldgen: a group count, then per group its zoom,
 * its rank and a label count, then that many coordinates as one delta chain running
 * through the whole file. The names are a JSON array in the same order.
 */
function read(points, names) {
  const read = cursor(points);
  const groups = read.value();
  const out = [];
  let n = 0;
  for (let g = 0; g < groups; g++) {
    const zoom = read.value();
    const rank = read.value();
    for (let i = read.value(); i > 0; i--) {
      const [lon, lat] = read.point();
      out.push({ lon, lat, rank, zoom, name: names[n++] });
    }
  }
  return out;
}

/**
 * fill builds the label layer inside a node the map has already put on the page.
 *
 * `project` is the map's own Web Mercator pair, lonToX(lon, z) and latToY(lat, z) —
 * the same two functions the pins are placed with. Passing them in rather than
 * importing a second copy is what keeps a name on its own country.
 *
 * Both streams arrive sorted by zoom and then by importance, which is the order they
 * are placed in, so nothing here sorts anything: it walks the countries, then the
 * provinces, then the cities, and stops at the first one that does not fit.
 */
export function fill(node, project) {
  const countries = read(WORLD_LABEL_POINTS, WORLD_LABEL_NAMES).map((l) => ({ ...l, kind: 'country' }));
  const regions = read(WORLD_REGION_POINTS, WORLD_REGION_NAMES).map((l) => ({ ...l, kind: 'region' }));
  const cities = read(WORLD_CITY_POINTS, WORLD_CITY_NAMES).map((l) => ({ ...l, kind: 'city' }));

  return {
    /** draw places the names for one view. Called from the map's own draw(). */
    draw(z, left, top, width, height) {
      if (z < MIN_LABEL_ZOOM) {
        node.textContent = '';
        return;
      }

      // Two passes, and the order is the point: a name's width is a font measurement,
      // and the only honest way to get it is to put the word on the page and ask. So
      // every candidate that could be drawn is built first, and then — in one batch,
      // after the whole batch is in the document — their boxes come back at once and
      // the ones that would cover a name already kept come out again. Nothing is
      // painted in between: the browser lays out once, when the first box is asked
      // for, and does not draw the frame until this function has returned. That is one
      // forced layout for the batch rather than one per name, which is what made the
      // measuring approach dear enough to be worth an estimate in the first place.
      const built = [];
      const fragment = document.createDocumentFragment();
      for (const list of [countries, regions, cities]) {
        for (const label of list) {
          if (built.length >= MAX_LABELS) break;
          // Sorted by zoom, so the first name that is too fine ends the list.
          if (label.zoom > z) break;

          const x = project.lonToX(label.lon, z) - left;
          const y = project.latToY(label.lat, z) - top;
          if (x < -EDGE_MARGIN || y < -EDGE_MARGIN ||
              x > width + EDGE_MARGIN || y > height + EDGE_MARGIN) {
            continue;
          }

          const el = document.createElement('span');
          el.className = `map-label k-${label.kind}`;
          // Centred on the anchor with the transform in the stylesheet, so the name
          // lands where the source put it: left and top are its middle, not its corner.
          el.style.left = `${x.toFixed(1)}px`;
          el.style.top = `${y.toFixed(1)}px`;
          el.textContent = label.name;
          fragment.append(el);
          built.push(el);
        }
      }
      node.textContent = '';
      node.append(fragment);

      // Every box in one read. The spans sit at fixed coordinates and are taken out by
      // remove(), which cannot move their neighbours, so each box stays true for as
      // long as this pass lasts.
      const boxes = built.map((el) => el.getBoundingClientRect());
      const kept = [];
      for (let i = 0; i < built.length; i++) {
        if (kept.some((other) => clashes(boxes[i], other))) {
          built[i].remove();
          continue;
        }
        kept.push(boxes[i]);
      }
    },
  };
}

/**
 * clashes reports whether two names would touch.
 *
 * The gap is what keeps two words that fit exactly next to each other from reading as
 * one: without it "Bayern" and the name beside it are a single line of text at the
 * boundary between the two, and the map is worse for having drawn both.
 */
function clashes(a, b) {
  return a.left < b.right + LABEL_GAP && b.left < a.right + LABEL_GAP &&
    a.top < b.bottom + LABEL_GAP && b.top < a.bottom + LABEL_GAP;
}
