// The country, province and district borders, drawn over the coastlines and the water.
//
// They come from three generated files — countries.js, borders.js and districts.js — in
// the same zoom-band shape the lakes arrive in, and mapbands.js walks all three. What is
// worth knowing here is the arrangement rather than the machinery:
//
//   * The countries go under the provinces, and are drawn brighter. The two files hold
//     different things: countries.js is the outline of each country, and borders.js is
//     the boundaries between the parts inside one — German Bundesländer, Swiss cantons,
//     American states. A province line that ran into a country line drawn at the same
//     weight would say the two mean the same, and they do not: one is where a country
//     ends and the other is where a county does.
//
//   * The districts go between them, and they are one country. districts.js is Austria's
//     political districts — the Bezirke and the statutory cities — filled rings drawn as
//     thin lines under the provinces, because a Bezirk is inside a Bundesland and the
//     line that says where the Bundesland ends is the one to keep when the two run along
//     the same river. Everywhere else on the map the district bands are empty: there is
//     no public-domain dataset of them for any other country (see districts.js, which
//     carries the licence this one exception cost).
//
//     It is also the only file here with one band. The Austrian register has no zoom in
//     it, so the generator chose 8 — deep enough that Vorarlberg's four districts and
//     Vienna's one are separately visible — and before zoom 8 the file draws nothing,
//     which is why a country view is the same picture it was before this layer existed.
//
//   * All three files are one setting. Settings → appearance has one switch, and it is
//     about the whole layer: an operator who turns the borders off is asking for a map
//     of countries with no lines across them, not for a map with the countries still
//     there and the provinces gone. map.js therefore does not import this module at all
//     when the setting is off, so countries.js is not downloaded either.
//
// The layer is an SVG of its own rather than a second path inside .map-outline: that
// element is the coastline, the map module draws a fill in it, and a border drawn with
// it would be filled like land.

import { banded } from './mapbands.js';
import { WORLD_COUNTRY_BANDS, WORLD_COUNTRY_DATA } from './countries.js';
import { WORLD_BORDER_BANDS, WORLD_BORDER_DATA } from './borders.js';
import { WORLD_DISTRICT_BANDS, WORLD_DISTRICT_DATA } from './districts.js';

/**
 * fill builds the borders inside a layer the map has already put on the page.
 *
 * The layer stays usable while empty, and it is the layer the map is mounted with: the
 * setting that decides whether to fetch this module is read before the fetch.
 */
export function fill(node) {
  const countries = banded(node, {
    data: WORLD_COUNTRY_DATA,
    bands: WORLD_COUNTRY_BANDS,
    closed: false,
    className: 'k-country',
  });
  const districts = banded(node, {
    data: WORLD_DISTRICT_DATA,
    bands: WORLD_DISTRICT_BANDS,
    closed: true,
    className: 'k-district',
  });
  const provinces = banded(node, {
    data: WORLD_BORDER_DATA,
    bands: WORLD_BORDER_BANDS,
    closed: false,
    className: 'k-province',
  });

  return {
    setView(z, left, top) {
      countries.setView(z, left, top);
      districts.setView(z, left, top);
      provinces.setView(z, left, top);
    },
  };
}
