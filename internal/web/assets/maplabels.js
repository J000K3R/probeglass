// The names of the countries, the first-order administrative areas — states, provinces,
// the German Bundesländer — the cities, the towns and Austria's political districts,
// drawn on the map.
//
// They come from labels.js, regions.js, cities.js, towns.js and districts.js, and they
// are drawn in screen coordinates: one absolutely positioned span each, in a layer over
// the tiles. The alternative — text in the SVG that is scaled with the map — would make a
// name bigger as somebody zooms in, and a 40-pixel "Bayern" is not a map anybody wants.
//
// Four things decide what is on screen, and they are all here rather than spread
// over the callers:
//
//   * The zoom a name belongs at. It comes from the data, not from a rule made up
//     here: Natural Earth says when its own map first writes each name, each
//     province name carries the rank that puts it in the same band as its border,
//     and a district name belongs at the zoom its outline is first drawn at. The
//     towns carry a zoom the generator derived from their population, because
//     GeoNames publishes no such opinion — see townZoom in cmd/worldgen.
//
//   * Whether it is on screen at all. Anything outside the viewport is skipped before
//     it is built, which is what keeps a world of 46 000 names cheap: at zoom 11 the
//     viewport holds a handful of them.
//
//   * Whether it collides. Names are laid down most important first — countries, then
//     provinces, then cities, then towns, then districts, and within each the source's
//     own ranking — and one that would overlap something already placed is dropped. A
//     map with half the names on it is a map; a map with all of them is a grey smear.
//
//   * Whether it is a duplicate of something already placed. That is a second question
//     and a different answer: a name that is already on the map within half a degree of
//     this one is the same city arriving from a second source, and unlike a collision it
//     is dropped however far apart the two boxes are. See samePlace, which is where the
//     only measurement in this file lives.
//
//   * How heavy it is. A district name is a quiet grey word on the area it names and a
//     city name is a dark one, which is a rule in the stylesheet rather than here — but
//     the order they are placed in belongs here, and it is the same rule: the loud name
//     wins the square inch and the quiet one gives way.
//
// The lists are also levels of zoom, and that is the other reason for the order: a country
// name arrives at zoom 2 and stays, a province name at 4 or 5, a city at 6 and deeper,
// and a district at 8. Vorarlberg is one province, four districts and, at zoom 8, one
// city — Bregenz. Where two of them want the same square inch the deeper one gives way,
// which is the right way round: "Vorarlberg" over a village is a worse map than a village
// with no label.
//
// The towns are the fifth list and they are the one that is a supplement rather than a
// layer: towns.js holds the places GeoNames has and Natural Earth does not, and it is
// drawn immediately after the cities as part of the same layer of places, under the city
// rule in the stylesheet and the name k-city — so a town is a city on this map, and the
// browser's own tests count the two together, which is also the honest count of places
// on screen. What the towns layer brings is the names that are in one file only: "Krems
// an der Donau", "Wiener Neustadt", "Wels".
//
// The two files overlap on 5 060 names, and that is the one thing this module has to
// handle that nothing above it does. Both sources publish a preferred romanized spelling,
// so the duplicate is the same word — "Vienna", "Munich", "Tokyo" — but the two put the
// anchor at different points of the same city, up to forty kilometres apart, which at a
// deep zoom is two names on the page that do not touch and are both drawn. Since 1c that
// is not left to the collision pass: samePlace drops a place whose name is already on the
// map within half a degree, which is the width of the gap the two files leave between the
// pairs that are one city and the pairs that are two — and there are 194 of the latter,
// which have to survive it.
//
// The districts are the one list that is drawn last rather than in its zoom order, because
// they are the only names here that are not a place: a district is an area, its name is
// anchored at the middle of that area rather than at any town, and it is drawn faintly so
// that the area reads through it. That is a background layer, and a background layer
// yields: where a city and the district around it both want the spot, the city is the one
// that is being looked for. See the header of districts.js for where the anchor comes from.
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
import { WORLD_TOWN_NAMES, WORLD_TOWN_POINTS } from './towns.js';
import { WORLD_DISTRICT_NAMES, WORLD_DISTRICT_POINTS } from './districts.js';

// The gap left between two names that are both drawn. Each box is measured off the
// page and is therefore exact, but two words touching edge to edge still read as one
// line of text, so a little air is left between the boxes before one is accepted.
const LABEL_GAP = 2;

// A name whose anchor is further than this outside the view is not built at all. It is
// a margin rather than the name's own box because the box is not known until the name
// is on the page, and what this is for is to keep a world of 46 000 names cheap: at any
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
 * provinces, then the cities, then the towns, then the districts, and stops at the
 * first one that does not fit.
 */
export function fill(node, project) {
  const countries = read(WORLD_LABEL_POINTS, WORLD_LABEL_NAMES).map((l) => ({ ...l, kind: 'country' }));
  const regions = read(WORLD_REGION_POINTS, WORLD_REGION_NAMES).map((l) => ({ ...l, kind: 'region' }));
  const cities = read(WORLD_CITY_POINTS, WORLD_CITY_NAMES).map((l) => ({ ...l, kind: 'city' }));
  const towns = read(WORLD_TOWN_POINTS, WORLD_TOWN_NAMES).map((l) => ({ ...l, kind: 'city' }));
  const districts = read(WORLD_DISTRICT_POINTS, WORLD_DISTRICT_NAMES)
    .map((l) => ({ ...l, kind: 'district', stem: l.name.replace(/\(Stadt\)$/, '') }));

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
      const placed = [];
      const fragment = document.createDocumentFragment();
      for (const list of [countries, regions, cities, towns, districts]) {
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
          placed.push(label);
        }
      }
      node.textContent = '';
      node.append(fragment);

      // Every box in one read. The spans sit at fixed coordinates and are taken out by
      // remove(), which cannot move their neighbours, so each box stays true for as
      // long as this pass lasts.
      const boxes = built.map((el) => el.getBoundingClientRect());
      const kept = [];
      const keptPlaces = [];
      for (let i = 0; i < built.length; i++) {
        // Two ways to be a duplicate, and the box is only one of them. The other is the
        // name: cities.js and towns.js both name the same city in hundreds of the places
        // they overlap, and the two sources put the anchor at different points in it —
        // up to forty kilometres apart for a city the size of Dubai, which at zoom 11 is
        // far enough for two "Salzburg"s to sit on the page at once without touching.
        // See samePlace for why that is measured and not guessed.
        if (keptPlaces.some((other) => samePlace(other, placed[i])) ||
            kept.some((other) => clashes(boxes[i], other))) {
          built[i].remove();
          continue;
        }
        kept.push(boxes[i]);
        keptPlaces.push(placed[i]);
      }
    },
  };
}

// SAME_PLACE is how far apart two labels with the same name can be and still be the same
// place: half a degree, which is about fifty kilometres.
//
// It is not a guess — it is read off the two files. Of the 5 060 names that are in both
// cities.js and towns.js, the pairs describing one place are never more than 43 km apart,
// and the pairs describing two different places with the same name are never less than 77
// — Winton in Australia against Winton in England, Scottsdale in Arizona against the one
// in Tasmania, and 194 more of those, which have to stay. Half a degree is the middle of
// that gap, and it is wide enough to cover the two label points of a large city and narrow
// enough that no homonym in either file falls inside it.
const SAME_PLACE = 0.5;

/**
 * samePlace reports whether two labels are the same place named twice.
 *
 * The rule is about the two layers where a name really is the same place's, and nowhere
 * else. Two places: cities.js and towns.js both name the same city in hundreds of the
 * places they overlap, and that is the duplicate this was written for. And a district and
 * the city it is named after: Austria's statutory cities are districts of their own called
 * "Salzburg(Stadt)", "Graz(Stadt)", "Krems an der Donau(Stadt)", and the four districts of
 * Vorarlberg are named Bregenz, Dornbirn, Feldkirch and Bludenz after the towns that give
 * them their names. Drawn together, those are one word twice — a grey one under a dark one
 * — and the grey one is the one that can go, because the district is an area and the area
 * is still on the map as its own outline.
 *
 * What it deliberately does not cover is anything above those two layers: a country and
 * its capital share a name — Luxembourg, Djibouti, Guatemala, Singapore — and so does a
 * Bundesland and the city inside it, and their anchors are close enough that a rule
 * ignoring the kind would delete the city at every zoom past two, which is the zoom
 * somebody is looking at when they want it.
 *
 * The distance is the real one and not the difference of the coordinates: a degree of
 * longitude is a degree of latitude times the cosine of the latitude, and at the latitude
 * of Vienna that is a factor of two thirds — which is the difference between a rule that
 * separates the homonyms and one that drops a town.
 */
function samePlace(a, b) {
  // Either order: the caller has the kept one and the candidate, and which of the two is
  // the district depends on which layer is being placed.
  const place = a.kind === 'city' ? a : (b.kind === 'city' ? b : null);
  if (!place) return false;
  const other = place === a ? b : a;

  let name = other.name;
  if (other.kind === 'district') {
    name = other.stem;
  } else if (other.kind !== 'city') {
    // A country or a Bundesland, which is where this rule stops.
    return false;
  }
  if (name !== place.name) return false;

  const dx = (a.lon - b.lon) * Math.cos((a.lat * Math.PI) / 180);
  const dy = a.lat - b.lat;
  return dx * dx + dy * dy < SAME_PLACE * SAME_PLACE;
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
