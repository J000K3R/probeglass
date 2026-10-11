// The water: the world's lakes and the finer copy of Europe's, filled, then the world's
// largest rivers and the finer copy of Europe's, as lines.
//
// It comes from four generated files — lakes.js, lakes_europe.js, rivers.js and
// rivers_europe.js — in the same zoom-band shape the borders arrive in, and mapbands.js
// walks all four. What is worth knowing here is the arrangement rather than the
// machinery:
//
//   * Water is part of the basemap, not a layer of its own. There is no switch for it:
//     the setting in Settings → appearance turns off the borders and the names, and a
//     map that could be asked to draw borders over a country with no lakes in it would
//     be a map claiming something about the ground that is not true.
//
//   * The lakes go under the rivers. A river that runs into a lake is published by
//     Natural Earth as a river plus a centreline through the lake, so a river drawn
//     first would be covered by the lake it flows into and would appear to stop at the
//     shore.
//
//   * Lakes are rings and rivers are lines, which is the `closed` flag and nothing
//     else in the drawing code. A ring has to be closed to be filled; a line that
//     closed would be a river running back up itself.
//
//   * There are two of each because Natural Earth publishes Europe twice. The world
//     files carry what is visible from a world view; the Europe ones carry the water
//     too small for that and are drawn from zoom 6, which is why the Zürichsee and the
//     Walensee arrive when somebody zooms into the Alps and not before. Their bands are
//     the files' own, so this module does not know which is which — it walks four
//     streams and the four band tables say when each of them belongs.
//
//     They are not two independent layers. The Europe files are the world files again
//     at a finer scale, and the generator drops the shapes whose name the world file
//     already draws (-minus in cmd/worldgen), so the two meet at a shore without a
//     second shoreline an inch away from it.

import { banded } from './mapbands.js';
import { WORLD_LAKE_BANDS, WORLD_LAKE_DATA } from './lakes.js';
import { WORLD_LAKE_EUROPE_BANDS, WORLD_LAKE_EUROPE_DATA } from './lakes_europe.js';
import { WORLD_RIVER_BANDS, WORLD_RIVER_DATA } from './rivers.js';
import { WORLD_RIVER_EUROPE_BANDS, WORLD_RIVER_EUROPE_DATA } from './rivers_europe.js';

/**
 * fill builds the water inside a layer the map has already put on the page.
 *
 * The layer stays usable while empty: a map with coastlines and pins and no lakes is a
 * map, and this module arrives a frame or two after the first draw.
 */
export function fill(node) {
  const lakes = banded(node, {
    data: WORLD_LAKE_DATA,
    bands: WORLD_LAKE_BANDS,
    closed: true,
    className: 'k-lake',
  });
  const lakesEurope = banded(node, {
    data: WORLD_LAKE_EUROPE_DATA,
    bands: WORLD_LAKE_EUROPE_BANDS,
    closed: true,
    className: 'k-lake',
  });
  const rivers = banded(node, {
    data: WORLD_RIVER_DATA,
    bands: WORLD_RIVER_BANDS,
    closed: false,
    className: 'k-river',
  });
  const riversEurope = banded(node, {
    data: WORLD_RIVER_EUROPE_DATA,
    bands: WORLD_RIVER_EUROPE_BANDS,
    closed: false,
    className: 'k-river',
  });

  return {
    setView(z, left, top) {
      lakes.setView(z, left, top);
      lakesEurope.setView(z, left, top);
      rivers.setView(z, left, top);
      riversEurope.setView(z, left, top);
    },
  };
}
