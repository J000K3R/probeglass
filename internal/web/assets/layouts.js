// The tree and the wall.
//
// Two more ways of looking at the sensors the dashboard already has. They are
// values of `ui.layout`, not pages of their own: a tab would say these are
// different things to look at, and they are not — they are the same sensors,
// gathered differently.
//
// The gathering is by place and then by group, because that is the shape of the
// thing being described: a site holds groups, a group holds sensors. A sensor
// with no site is not an error and not a special case — it is gathered under
// "No site", which is where most of them are on a dashboard that has never been
// told about places at all.
//
// Like the other layout modules this one only draws. Opening a sensor, pausing
// it and reordering it are the callbacks in `ctx`, which are the same functions
// the cards use, so every view behaves identically without any of this knowing
// what a probe is.

import { t } from './i18n.js';
import { esc, fmtPct, fmtLatency, fmtAgo, heartbeat } from './charts.js';
import { colourAttr, groupIcon, groupOf, iconMarkup } from './icons.js';

/**
 * worstStatus is the most serious status among a set of sensors.
 *
 * A site head has to answer "is anything wrong in here?" at a glance, and the
 * answer is the worst thing in it, not the average: one dead sensor among twenty
 * healthy ones is exactly the case the head exists to surface. The map uses the
 * same rule for the same reason, which is why this is exported rather than
 * living quietly in this file.
 *
 * Paused ranks below unknown on purpose. A paused sensor is a decision somebody
 * made; a sensor that has never answered is a question nobody has answered yet.
 */
const RANK = { down: 4, warn: 3, unknown: 2, paused: 1, up: 0 };

export function worstStatus(sensors) {
  let found = 'unknown';
  let rank = -1;
  for (const sensor of sensors) {
    const r = RANK[sensor.status] ?? RANK.unknown;
    if (r > rank) {
      rank = r;
      found = sensor.status;
    }
  }
  return rank < 0 ? 'unknown' : found;
}

/**
 * groupBySite gathers sensors into sites, and inside each site into groups.
 *
 * The sites come back in the order they were configured rather than in the order
 * sensors happen to arrive, so a tree does not reshuffle itself when a sensor
 * moves between groups. A site with nothing on it is left out — the dashboard is
 * about sensors, and an empty heading is a line of noise.
 */
export function groupBySite(sensors, sites) {
  const known = new Map();
  const buckets = new Map();
  const order = [];

  const bucketFor = (name) => {
    let bucket = buckets.get(name);
    if (!bucket) {
      bucket = { name, site: known.get(name) || null, groups: [], byGroup: new Map() };
      buckets.set(name, bucket);
      order.push(name);
    }
    return bucket;
  };

  for (const site of sites || []) known.set(site.name, site);
  for (const site of sites || []) bucketFor(site.name);

  for (const sensor of sensors) {
    const bucket = bucketFor(sensor.site || '');
    const group = sensor.group || '';
    if (!bucket.byGroup.has(group)) {
      bucket.byGroup.set(group, []);
      bucket.groups.push(group);
    }
    bucket.byGroup.get(group).push(sensor);
  }

  const out = [];
  for (const name of order) {
    const bucket = buckets.get(name);
    if (!bucket.byGroup.size) continue;
    const all = [];
    for (const group of bucket.groups) all.push(...bucket.byGroup.get(group));
    out.push({
      name: bucket.name,
      site: bucket.site,
      groups: bucket.groups.map((g) => ({ name: g, sensors: bucket.byGroup.get(g) })),
      sensors: all,
      worst: worstStatus(all),
    });
  }
  return out;
}

const CARET = '<svg class="site-caret" viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">' +
  '<path d="M4 2l4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.8" ' +
  'stroke-linecap="round" stroke-linejoin="round"/></svg>';

/** The name of a bucket: the site's, or "No site" for the sensors that have none. */
function siteLabel(bucket) {
  return bucket.name || t('sites.none');
}

/**
 * The symbol a place carries, if somebody gave it one.
 *
 * It comes off the site's own row rather than off the sensors, because it is a
 * picture of the place: the machines in the rack do not each wear it, the heading
 * they are gathered under does. A site named by a sensor but absent from the list
 * — which the daemon creates on the way past — has no row here and therefore no
 * symbol, which is why this is a function and not a field access.
 */
function siteIcon(bucket) {
  return iconMarkup(bucket.site && bucket.site.icon, { size: 16, cls: 'icon-lead' });
}

/** The kind of place, as a tag. A site named by a sensor but absent from the list has none. */
function kindTag(bucket) {
  const kind = bucket.site && bucket.site.kind;
  if (!kind) return '';
  return `<span class="kind-tag k-${esc(kind)}">${esc(t(`sites.kind.${kind}`))}</span>`;
}

/** How much is in a bucket, and how much of it is broken. */
function headCount(bucket) {
  const down = bucket.sensors.filter((s) => s.status === 'down').length;
  return down > 0
    ? t('sites.sensorCountDown', { count: bucket.sensors.length, down })
    : t('sites.sensorCount', { count: bucket.sensors.length });
}

/**
 * refreshHead brings a place's heading in line with the sensors drawn under it.
 *
 * The dashboard replaces one sensor's row while a probe is happening, and both
 * things a heading says — the worst status in the place, and how many of its
 * sensors are down — are changed by the row that just went by. A heading left
 * alone would go on saying that everything is fine above a row that has gone red.
 *
 * `section` is the element that carries the heading: a tree's place node or a
 * wall's group. Only the dot and the count inside it are touched; whether a
 * <details> is open is the reader's business and not this function's.
 */
export function refreshHead(section, sensors) {
  const dot = section.querySelector('.legend-dot');
  if (dot) dot.className = `legend-dot st-${worstStatus(sensors)}`;
  const count = section.querySelector('.site-count, .wall-count');
  if (count) count.textContent = headCount({ sensors });
}

// ---------------------------------------------------------------------------
// tree — collapsed by place, like a monitoring console
// ---------------------------------------------------------------------------

/**
 * renderTree draws the two-level tree.
 *
 * <details>/<summary> rather than a hand-rolled expander: the keyboard, the
 * screen reader and the browser's own find-in-page all already understand it,
 * and a custom one would have to be taught each of them in turn.
 *
 * The summary carries no `data-id`. Everything else in this container does, and
 * the delegated click handler opens a sensor detail for whatever was clicked —
 * on a heading that would open whichever sensor happened to be nearest.
 */
export function renderTree(sensors, sites, ctx) {
  return groupBySite(sensors, sites).map((bucket) => `
    <details class="site-node" open>
      <summary class="site-head">
        ${CARET}
        <span class="legend-dot st-${esc(bucket.worst)}" aria-hidden="true"></span>
        ${siteIcon(bucket)}
        <span class="site-name">${esc(siteLabel(bucket))}</span>
        ${kindTag(bucket)}
        <span class="site-count">${esc(headCount(bucket))}</span>
      </summary>
      <div class="site-body">
        ${bucket.groups.map((group) => {
          // What the settings say about this group, if anything: a symbol and a
          // colour, or nothing at all, which is what most groups have.
          const settings = groupOf(ctx.groups, group.name);
          return `
          <div class="group-node">
            ${group.name ? `<div class="group-label"${colourAttr(settings)}>` +
              `${groupIcon(settings, 15)}${esc(group.name)}</div>` : ''}
            ${group.sensors.map((sensor) => treeRow(sensor, ctx)).join('')}
          </div>`;
        }).join('')}
      </div>
    </details>`).join('');
}

/**
 * treeRow is one sensor as one row of the tree.
 *
 * Exported because the dashboard replaces a single row when a probe lands, and it
 * has to be the row this file would have drawn. A second copy of this markup in
 * app.js would be a copy that drifts.
 */
export function treeRow(sensor, ctx) {
  return `
    <div class="tree-row st-${esc(sensor.status)}${sensor.excuse ? ' in-excused in-' + esc(sensor.excuse) : ''}" data-id="${sensor.id}" tabindex="0">
      ${ctx.statusPill(sensor)}
      <span class="row-name">${iconMarkup(sensor.icon, { size: 15, cls: 'icon-lead' })}${esc(sensor.name)}
        <span class="row-target">${esc(sensor.target)}</span></span>
      <span class="row-spark">${ctx.sparkline(sensor.sparkline || [], { width: 120, height: 24 })}</span>
      <span class="num" title="${esc(t('summary.uptime'))}">${esc(fmtPct(sensor.uptime_24h))}</span>
      ${ctx.cardActions(sensor)}
    </div>`;
}

// ---------------------------------------------------------------------------
// wall — one tile per sensor, with its recent run on it
// ---------------------------------------------------------------------------

/**
 * renderWall draws the tiles.
 *
 * Every tile is the same size and carries the same three things in the same
 * place, so the eye learns where to look once and then reads the whole wall
 * without moving. The heartbeat is the widest element on the tile for that
 * reason: it is the part that says what happened, as opposed to what is true
 * right now.
 */
export function renderWall(sensors, sites, ctx) {
  return groupBySite(sensors, sites).map((bucket) => `
    <section class="wall-group">
      <h2 class="wall-head">
        <span class="legend-dot st-${esc(bucket.worst)}" aria-hidden="true"></span>
        ${siteIcon(bucket)}
        ${esc(siteLabel(bucket))}
        ${kindTag(bucket)}
        <span class="wall-count">${esc(headCount(bucket))}</span>
      </h2>
      <div class="wall">${bucket.sensors.map((sensor) => tile(sensor, ctx)).join('')}</div>
    </section>`).join('');
}

/** tile is one sensor as one tile of the wall. Exported for the same reason as treeRow. */
export function tile(sensor, ctx) {
  const beats = sensor.heartbeat || [];
  const down = sensor.status === 'down';
  return `
    <article class="tile st-${esc(sensor.status)}${down ? ' is-down' : ''}${sensor.excuse ? ' in-excused in-' + esc(sensor.excuse) : ''}" data-id="${sensor.id}" tabindex="0">
      <header class="tile-head">
        ${ctx.statusPill(sensor)}
        <span class="tile-name" title="${esc(sensor.target)}">${iconMarkup(sensor.icon, { size: 15, cls: 'icon-lead' })}${esc(sensor.name)}</span>
      </header>
      ${heartbeat(beats, {
        empty: t('heartbeat.none'),
        label: (i, ok) => `${t('heartbeat.check', { i: i + 1, n: beats.length })} · ` +
          t(ok ? 'heartbeat.ok' : 'heartbeat.fail'),
      })}
      <div class="tile-meta">
        <span>${esc(fmtLatency(sensor.latency_ms))}</span>
        <span>24h ${esc(fmtPct(sensor.uptime_24h))}</span>
        <span class="muted">${esc(fmtAgo(sensor.last_sensor))}</span>
      </div>
      ${ctx.cardActions(sensor)}
    </article>`;
}
