// The world's coastlines, for the map to draw when no tile server is configured.
//
// A slippy map needs something under its markers, and until now that something was
// somebody else's tile server. There is no longer a free one that a third-party
// application may use: OpenStreetMap closed that door in July 2025, and every
// alternative wants an account. So the map draws the world itself, from a dataset
// that shipped in the binary — which is also the only version of this that works on
// a homelab with no internet at all, and the only one that tells nobody which
// addresses somebody is watching.
//
// The data is in world.js: rings of coordinates, delta-coded, generated from Natural
// Earth's 1:50m land layer by cmd/worldgen. The provenance is in the header there.
// The country and province borders and the place names come from the same generator
// and are drawn by borders.js and labels.js, which read their own files.
//
// This file is also where the projection lives: lonToX, latToY and the zoom-4 world
// they are expressed in. The other layers draw with those rather than with a second
// copy of the arithmetic, because two copies of a projection that disagree by a pixel
// put a border next to its country.
//
// Two things about the drawing are worth knowing:
//
//   * The path is in the map's own world coordinates at zoom 4 — the same Web
//     Mercator arithmetic map.js uses. Zooming scales the group rather than
//     rebuilding the path, so a wheel click is a transform on 35 000 points and not a
//     35 000-point string rebuilt; `vector-effect: non-scaling-stroke` keeps the
//     coastline one pixel wide at every level.
//
//   * It is deliberately coarse. 1:50m at a tolerance of 0.02° is within a pixel of
//     the truth to about zoom 6 and visibly angular by about zoom 10. Somebody who
//     needs a coastline past that enters a tile URL in Settings, and that field is
//     there for exactly that.

import { WORLD_DATA, WORLD_RINGS, WORLD_SCALE } from './world.js';

const TILE = 256;

// The zoom the path coordinates are expressed in, and its extent in world pixels.
// Four is a compromise: high enough that a round coordinate is not a visible step,
// low enough that the coordinates stay small in the encoded file.
export const BASE_ZOOM = 4;
export const BASE_SPAN = TILE * 2 ** BASE_ZOOM;

export const SVG_NS = 'http://www.w3.org/2000/svg';

// The alphabet worldgen writes in, and a reverse lookup built once. A char code
// outside it means the file and this decoder disagree about the format, which is a
// broken build rather than a runtime condition — so it throws, loudly, and the map
// module catches it and draws no coastlines rather than no map.
const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
const CODE = (() => {
  const table = new Int8Array(128).fill(-1);
  for (let i = 0; i < ALPHABET.length; i++) table[ALPHABET.charCodeAt(i)] = i;
  return table;
})();

const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);

/** lonToX is the projection map.js draws markers with. The two have to agree. */
export function lonToX(lon) {
  return ((lon + 180) / 360) * BASE_SPAN;
}

export function latToY(lat) {
  const s = Math.sin((clamp(lat, -85.05112878, 85.05112878) * Math.PI) / 180);
  return (0.5 - Math.log((1 + s) / (1 - s)) / (4 * Math.PI)) * BASE_SPAN;
}

/**
 * cursor reads the packed streams cmd/worldgen writes.
 *
 * The format is documented there. What lives here is the mechanism all three of its
 * shapes share: a varint is base 63 in six-bit characters closed by a chunk of 63, and
 * coordinates arrive as deltas from the point before, so a reader accumulates from
 * zero and the first point of a chain is an absolute one with no special case to
 * forget.
 *
 * The two ways to read a chain are the two the format has. `line()` starts a fresh one
 * — a coastline ring, a border line, each beginning absolute — and `point()` continues
 * the one that is running, which is how the name streams run a single chain from the
 * first label to the last, groups and all.
 */
export function cursor(data) {
  let pos = 0;
  let lon = 0;
  let lat = 0;

  function value() {
    let v = 0;
    // Twelve chunks is far more than any coordinate needs and far less than a loop
    // over a corrupt file would take. A string that never closes would otherwise spin
    // forever inside an animation frame.
    for (let n = 0; n < 12; n++) {
      const c = CODE[data.charCodeAt(pos++)] ?? -1;
      if (c < 0) throw new Error('map data is truncated or not in the expected alphabet');
      if (c === 63) return v;
      v = v * 63 + c;
    }
    throw new Error('map data has an unterminated number');
  }

  const unzigzag = (u) => (u % 2 ? -(u - 1) / 2 - 1 : u / 2);

  /** point advances the running coordinate and returns it in degrees. */
  function point() {
    lon += unzigzag(value());
    lat += unzigzag(value());
    return [lon / WORLD_SCALE, lat / WORLD_SCALE];
  }

  return {
    value,
    point,
    /** line reads a point count and then that many points, as their own chain. */
    line() {
      const points = value();
      const out = new Float64Array(points * 2);
      lon = 0;
      lat = 0;
      for (let j = 0; j < points; j++) {
        const p = point();
        out[j * 2] = p[0];
        out[j * 2 + 1] = p[1];
      }
      return out;
    },
  };
}

/** decode reads the rings of world.js back out of it. */
export function decode(data = WORLD_DATA) {
  const read = cursor(data);
  const count = read.value();
  const rings = [];
  for (let i = 0; i < count; i++) rings.push(read.line());
  return rings;
}

/**
 * pathData turns lines of coordinates into one path string in the zoom-4 world.
 *
 * `closed` is whether each line comes back to where it started. A coastline ring does
 * and a border does not, and the difference is a `Z` — which for a border is a line
 * drawn straight across the country to its own beginning.
 */
export function pathData(lines, closed = true) {
  const parts = [];
  for (const line of lines) {
    let head = true;
    for (let i = 0; i < line.length; i += 2) {
      const x = lonToX(line[i]).toFixed(2);
      const y = latToY(line[i + 1]).toFixed(2);
      parts.push(head ? `M${x} ${y}` : `L${x} ${y}`);
      head = false;
    }
    if (closed) parts.push('Z');
  }
  return parts.join('');
}

/**
 * createOutline builds the coastline layer.
 *
 * The returned `setView` is the same three numbers map.js renders markers from, and
 * it is called from the same place: one function decides where the map is looking,
 * and everything that has to agree about it is told there.
 *
 * Nothing here reads the network, and nothing here needs a tile to have failed. The
 * coastlines are under the tiles, so an operator who configures a tile server sees
 * their tiles and never notices this layer; one who does not, or whose server stops
 * answering, sees the world instead of a grey rectangle.
 */
export function createOutline(rings = null) {
  const node = document.createElementNS(SVG_NS, 'svg');
  node.setAttribute('class', 'map-outline');
  node.setAttribute('aria-hidden', 'true');
  node.setAttribute('focusable', 'false');

  const group = document.createElementNS(SVG_NS, 'g');
  node.append(group);

  try {
    const decoded = rings || decode();
    if (decoded.length !== WORLD_RINGS) {
      throw new Error(`outline file says ${WORLD_RINGS} ring(s), decoded ${decoded.length}`);
    }
    const path = document.createElementNS(SVG_NS, 'path');
    path.setAttribute('d', pathData(decoded));
    path.setAttribute('fill-rule', 'evenodd');
    path.setAttribute('vector-effect', 'non-scaling-stroke');
    group.append(path);
  } catch (error) {
    // A map without coastlines is still a map: the graticule, the markers and the
    // projection all still work. A map that throws while being built is a blank
    // screen, which is the failure this whole module exists to avoid.
    console.error('probeglass: could not draw the coastlines', error);
    node.classList.add('is-broken');
  }

  return {
    node,
    /** setView places the layer for one view, in the map's own coordinates. */
    setView(z, left, top) {
      const scale = 2 ** (z - BASE_ZOOM);
      group.setAttribute('transform', `translate(${-left.toFixed(2)} ${-top.toFixed(2)}) scale(${scale})`);
    },
  };
}
