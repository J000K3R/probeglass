// One packed zoom-band stream, drawn as one SVG path per band.
//
// Four generated files are published in the same shape — countries.js, borders.js,
// lakes.js and rivers.js — because they answer the same question: which of these
// lines or rings belongs on screen at this zoom. Natural Earth gave every one of them
// a min_zoom, the zoom its author drew it for, and the generator grouped them by it.
// The file lists those groups in WORLD_*_BANDS and packs them one after another, each
// beginning with its own count, so reading them in order costs nothing extra and a
// band nobody reaches is never read.
//
// That shape is what this module walks, once, for all four. The alternative was the
// same forty lines four times, and the day one of them is fixed is the day the other
// three are not.
//
// Two things are worth knowing about the drawing:
//
//   * Nothing is built until it is needed. borders.js holds nine bands and 97 000
//     points; a world view wants the first two, which are 13 KB of path. Building all
//     nine would spend the work of a city-street view on somebody looking at Europe.
//
//   * A line is a hairline at every zoom. The layer is one scaled group, so a stroke
//     of one unit would be a thirty-unit slab of grey at zoom nine, drawn over the
//     country it is dividing — `vector-effect: non-scaling-stroke`, the same as the
//     coastlines carry, keeps it one pixel. It is set on the path rather than in the
//     stylesheet because that is where outline.js sets it, and two layers of one map
//     that each say it their own way is how one of them ends up forgotten.

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
 * banded builds one stream into a layer the map has already put on the page.
 *
 * `closed` is the difference between a ring and a line, and it is not cosmetic:
 * pathData closes a ring so it can be filled, and leaves a line open because a border
 * that returned to its own start would draw a line straight across the country it
 * divides. `className` goes on every path so the stylesheet can colour one layer
 * differently from the one under it.
 *
 * The node arrives empty and stays usable as an empty layer: the map mounts before
 * these modules are fetched, and a map with coastlines and pins and no borders is a
 * map, not a failure.
 */
export function banded(parent, { data, bands, closed, className }) {
  const group = document.createElementNS(SVG_NS, 'g');
  parent.append(group);

  // null until the first band is wanted: the cursor is what walks the packed string,
  // and there is nothing to walk until there is a reason to.
  let read = null;
  const paths = [];

  return {
    /**
     * setView shows the bands that belong at one zoom, and places the layer.
     *
     * A band's number is the zoom it was drawn for, so a map at zoom z shows every
     * band up to z and none past it: three is the shape of Germany with the states
     * that were drawn for a world view, and eight is the same country with all of
     * them.
     */
    setView(z, left, top) {
      const scale = 2 ** (z - BASE_ZOOM);
      group.setAttribute('transform',
        `translate(${-left.toFixed(2)} ${-top.toFixed(2)}) scale(${scale})`);

      let visible = 0;
      while (visible < bands.length && bands[visible] <= z) visible++;
      if (read === null) read = cursor(data);
      while (paths.length < visible) {
        const shapes = [];
        for (let i = read.value(); i > 0; i--) shapes.push(read.line());
        const path = document.createElementNS(SVG_NS, 'path');
        path.setAttribute('d', pathData(shapes, closed));
        path.setAttribute('vector-effect', 'non-scaling-stroke');
        if (className) path.setAttribute('class', className);
        group.append(path);
        paths.push(path);
      }
      for (let i = 0; i < paths.length; i++) hide(paths[i], i >= visible);
    },
  };
}
