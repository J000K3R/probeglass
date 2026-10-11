// The country and province borders, drawn over the coastlines.
//
// They come from borders.js: lines of coordinates in the same zoom-4 world
// outline.js draws in, grouped into the zoom bands Natural Earth itself puts them in.
// This file turns a band into an SVG path the first time the map is deep enough to
// want it, and shows or hides the paths from then on.
//
// Two things are worth knowing about the drawing:
//
//   * Nothing is built until it is needed. The file holds nine bands and 97 000
//     points; a world view wants the first two, which are 13 KB of path. Building all
//     nine would spend the work of a city-street view on somebody looking at Europe.
//     Because a band's stream begins where the one before it ended, reading them in
//     order costs nothing extra — and a band that is never reached is never read.
//
//   * The lines are not closed. pathData gets `false`, because a border that returns
//      to its own start draws a line straight across the country, and `fill: none` in
//     the stylesheet means there is nothing for that line to be the edge of.
//
//   * A border is a hairline at every zoom. The layer is one scaled group, so a stroke
//     of one unit would be a thirty-unit slab of grey at zoom nine, drawn over the
//     country it is dividing — `vector-effect: non-scaling-stroke`, the same as the
//     coastlines carry, keeps it one pixel. It is set on the path rather than in the
//     stylesheet because that is where outline.js sets it, and two layers of one map
//     that each say it their own way is how one of them ends up forgotten.
//
// The layer is an SVG of its own rather than a second path inside .map-outline: that
// element is the coastline, the map module draws a fill in it, and a border drawn with
// it would be filled like land.

import { WORLD_BORDER_BANDS, WORLD_BORDER_DATA } from './borders.js';
import { BASE_ZOOM, SVG_NS, cursor, pathData } from './outline.js';

/**
 * hide shows or hides one band.
 *
 * `hidden` is an attribute here rather than the `element.hidden` property used
 * everywhere else in this project: that property lives on HTMLElement, and an SVG
 * path is not one. Assigning it to a path would set a property nothing reads and the
 * band would stay on screen. The stylesheet's `[hidden] { display: none }` matches an
 * attribute in any namespace, so the attribute is what works.
 */
function hide(path, hidden) {
  if (hidden) path.setAttribute('hidden', '');
  else path.removeAttribute('hidden');
}

/**
 * fill builds the bands inside a layer the map has already put on the page.
 *
 * The node arrives empty and stays usable as an empty layer: the map mounts before
 * this module is fetched, and a map with coastlines and pins and no borders is a map,
 * not a failure.
 */
export function fill(node) {
  const group = document.createElementNS(SVG_NS, 'g');
  node.append(group);

  // null until the first band is wanted: the cursor is what walks the packed string,
  // and there is nothing to walk until there is a reason to.
  let read = null;
  const paths = [];

  /** ensure reads bands off the stream until `count` of them have been built. */
  function ensure(count) {
    if (read === null) read = cursor(WORLD_BORDER_DATA);
    while (paths.length < count) {
      const lines = [];
      for (let i = read.value(); i > 0; i--) lines.push(read.line());
      const path = document.createElementNS(SVG_NS, 'path');
      path.setAttribute('d', pathData(lines, false));
      path.setAttribute('vector-effect', 'non-scaling-stroke');
      group.append(path);
      paths.push(path);
    }
  }

  return {
    /**
     * setView shows the bands that belong at one zoom, and places the layer.
     *
     * A band's number is the zoom it was drawn for, so a map at zoom z shows every
     * band up to z and none past it: three is the shape of Germany with the states
     * that were drawn for a world view, and eight is the same country with all of them.
     */
    setView(z, left, top) {
      const scale = 2 ** (z - BASE_ZOOM);
      group.setAttribute('transform',
        `translate(${-left.toFixed(2)} ${-top.toFixed(2)}) scale(${scale})`);

      let visible = 0;
      while (visible < WORLD_BORDER_BANDS.length && WORLD_BORDER_BANDS[visible] <= z) visible++;
      ensure(visible);
      for (let i = 0; i < paths.length; i++) hide(paths[i], i >= visible);
    },
  };
}
