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
// Earth's 1:110m land layer by cmd/worldgen. The provenance is in the header there.
// This file decodes it once and turns it into one SVG path.
//
// Two things about the drawing are worth knowing:
//
//   * The path is in the map's own world coordinates at zoom 4 — the same Web
//     Mercator arithmetic map.js uses. Zooming scales the group rather than
//     rebuilding the path, so a wheel click is a transform on 5000 points and not a
//     5000-point string rebuilt; `vector-effect: non-scaling-stroke` keeps the
//     coastline one pixel wide at every level.
//
//   * It is deliberately coarse. 1:110m is a world map: the shape of a continent is
//     right, the shape of a bay is a straight line. Somebody who needs the second
//     one enters a tile URL in Settings, and that field is there for exactly that.

import { WORLD_DATA, WORLD_RINGS, WORLD_SCALE } from './world.js';

const TILE = 256;

// The zoom the path coordinates are expressed in, and its extent in world pixels.
// Four is a compromise: high enough that a round coordinate is not a visible step,
// low enough that the coordinates stay small in the encoded file.
const BASE_ZOOM = 4;
const BASE_SPAN = TILE * 2 ** BASE_ZOOM;

const SVG_NS = 'http://www.w3.org/2000/svg';

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
function lonToX(lon) {
  return ((lon + 180) / 360) * BASE_SPAN;
}

function latToY(lat) {
  const s = Math.sin((clamp(lat, -85.05112878, 85.05112878) * Math.PI) / 180);
  return (0.5 - Math.log((1 + s) / (1 - s)) / (4 * Math.PI)) * BASE_SPAN;
}

/**
 * decode reads the rings back out of WORLD_DATA.
 *
 * The format is documented in cmd/worldgen. Two details of it live here: a varint is
 * base 63 in six-bit characters closed by a chunk of 63, and the first point of a ring
 * is written as an absolute value where every other is a delta — so accumulating from
 * zero does the right thing for both, and there is no special case to forget.
 */
export function decode(data = WORLD_DATA) {
  let pos = 0;

  function value() {
    let v = 0;
    // Twelve chunks is far more than any coordinate needs and far less than a loop
    // over a corrupt file would take. A string that never closes would otherwise spin
    // forever inside an animation frame.
    for (let n = 0; n < 12; n++) {
      const c = CODE[data.charCodeAt(pos++)] ?? -1;
      if (c < 0) throw new Error('outline data is truncated or not in the expected alphabet');
      if (c === 63) return v;
      v = v * 63 + c;
    }
    throw new Error('outline data has an unterminated number');
  }

  const unzigzag = (u) => (u % 2 ? -(u - 1) / 2 - 1 : u / 2);

  const rings = [];
  const count = value();
  for (let i = 0; i < count; i++) {
    const points = value();
    const ring = new Float64Array(points * 2);
    let lon = 0;
    let lat = 0;
    for (let j = 0; j < points; j++) {
      lon += unzigzag(value());
      lat += unzigzag(value());
      ring[j * 2] = lon / WORLD_SCALE;
      ring[j * 2 + 1] = lat / WORLD_SCALE;
    }
    rings.push(ring);
  }
  return rings;
}

/** pathData turns the rings into one path string in the zoom-4 world. */
function pathData(rings) {
  const parts = [];
  for (const ring of rings) {
    let head = true;
    for (let i = 0; i < ring.length; i += 2) {
      const x = lonToX(ring[i]).toFixed(2);
      const y = latToY(ring[i + 1]).toFixed(2);
      parts.push(head ? `M${x} ${y}` : `L${x} ${y}`);
      head = false;
    }
    parts.push('Z');
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
    console.error('weatherglass: could not draw the coastlines', error);
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
