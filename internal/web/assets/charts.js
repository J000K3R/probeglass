// Hand-rolled SVG charts.
//
// There is no charting library: the dashboard has to work offline, and a
// dependency would need a build step. The rules the charts follow are the
// boring ones that matter — a labelled axis with units, colour that carries
// meaning rather than decoration, gaps drawn as gaps instead of as zeroes, and
// a <title> on every mark so hovering explains it.
//
// Colour comes from CSS classes, never from literals, so switching theme or
// accent re-colours the charts without re-rendering them.

/** Escape text that goes into markup. Values are numbers, but labels are not. */
export function esc(value) {
  return String(value ?? '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

/** Latency in milliseconds, rendered with a unit that suits the magnitude. */
export function fmtLatency(ms) {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return '—';
  if (ms === 0) return '0 ms';
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)} s`;
}

/** A percentage with the precision its own value deserves. */
export function fmtPct(pct) {
  if (pct === null || pct === undefined || Number.isNaN(pct)) return '—';
  if (pct >= 100) return '100%';
  if (pct >= 99.9) return `${pct.toFixed(2)}%`;
  return `${pct.toFixed(1)}%`;
}

/** A duration in milliseconds, in the largest two units that fit. */
export function fmtDuration(ms) {
  if (!ms || ms < 0) return '—';
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

/** Byte sizes for the storage panel. */
export function fmtBytes(bytes) {
  if (!bytes) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

/** Relative time such as "just now" or "12 min ago". */
export function fmtAgo(iso, now = Date.now()) {
  if (!iso) return '—';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '—';
  const diff = Math.max(0, now - then);
  if (diff < 5000) return 'just now';
  if (diff < 90_000) return `${Math.round(diff / 1000)}s ago`;
  if (diff < 3_600_000) return `${Math.round(diff / 60_000)} min ago`;
  if (diff < 86_400_000) return `${Math.round(diff / 3_600_000)} h ago`;
  return `${Math.round(diff / 86_400_000)} d ago`;
}

/** Clock time in the browser's own timezone. */
export function fmtClock(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

/** Date and time, compact. */
export function fmtDateTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString(undefined, {
    day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit',
  });
}

/** Axis label that adapts to the length of the window it describes. */
function fmtTick(iso, spanMs) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  if (spanMs <= 6 * 3_600_000) return fmtClock(iso);
  if (spanMs <= 3 * 86_400_000) {
    return d.toLocaleString(undefined, { weekday: 'short', hour: '2-digit' });
  }
  return d.toLocaleDateString(undefined, { day: '2-digit', month: 'short' });
}

/** The uptime class a bar should wear. Thresholds are conventional: a homelab
 *  that drops below 99% has a real problem, below 95% is an outage. */
export function uptimeClass(pct) {
  if (pct === null || pct === undefined) return 'u-unknown';
  if (pct >= 99.5) return 'u-ok';
  if (pct >= 95) return 'u-warn';
  if (pct >= 90) return 'u-bad';
  return 'u-crit';
}

/** Round a maximum up to a value a human would have chosen. */
function niceMax(value) {
  if (!(value > 0)) return 10;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  for (const step of [1, 1.5, 2, 2.5, 3, 4, 5, 7.5, 10]) {
    if (value <= step * magnitude) return step * magnitude;
  }
  return 10 * magnitude;
}

const svg = (width, height, body, extra = '') => `<svg class="chart" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" ${extra}>${body}</svg>`;

// ---------------------------------------------------------------------------
// sparkline — the miniature trace on a dashboard card
// ---------------------------------------------------------------------------

/**
 * Draw a latency sparkline. Values of 0 mean "the probe failed", which is a
 * break in the line rather than a dive to zero, plus a tick on the baseline so
 * the failure is still visible.
 *
 * `stretch` lets the drawing fill whatever box it is put in — the width and
 * height here are then a shape rather than a size. It is for the header tiles,
 * where the curve is as wide as the tile and the tile's width is not known until
 * the page is laid out. Everywhere else the numbers in the options are the size
 * on screen, and the aspect ratio is kept.
 *
 * `fit` draws the curve against its own range instead of against zero, which is
 * what a trend line is normally drawn against and what a sparkline is for. The
 * default — zero at the bottom, the largest value at the top — is the right one
 * where the values occasionally spike, because then the spike is the story and
 * it belongs at the top. It is the wrong one for a figure that moves inside a
 * narrow band, which is what an hour of the whole installation looks like: a day
 * whose hourly averages run from 84 to 101 ms, drawn against zero, is a straight
 * line six pixels below the top of the tile, and a straight line says nothing.
 * Fitted, the same day has a shape and the number above it still says what it
 * says. The cost is honest to name: a fitted curve exaggerates, and the flattest
 * possible day fills the tile as much as the worst one. Nothing here is read as a
 * measurement — the measurement is the figure above it, and this is the shape of
 * the day that produced it.
 */
export function sparkline(values, { width = 220, height = 34, stretch = false, fit = false } = {}) {
  const extra = stretch ? 'preserveAspectRatio="none"' : '';
  if (!values || values.length === 0) {
    return svg(width, height, `<line class="spark-empty" x1="0" y1="${height / 2}" x2="${width}" y2="${height / 2}"/>`, extra);
  }
  const pad = 3;
  const usable = height - pad * 2;
  const measured = values.filter((v) => v > 0);
  const top = Math.max(...measured, 1);
  const floor = fit && measured.length ? Math.min(...measured) : 0;
  const stepX = values.length > 1 ? width / (values.length - 1) : width;
  // A flat run has no shape to draw. Fitted, top and floor are the same number
  // and there is no ratio to take; the line goes in the middle, where a line that
  // is neither rising nor falling belongs.
  const y = top === floor
    ? () => pad + usable / 2
    : (v) => pad + usable - ((v - floor) / (top - floor)) * usable;

  const segments = [];
  let current = [];
  const failures = [];
  values.forEach((v, i) => {
    if (v > 0) {
      current.push(`${(i * stepX).toFixed(1)},${y(v).toFixed(1)}`);
    } else {
      if (current.length > 1) segments.push(current);
      current = [];
      failures.push((i * stepX).toFixed(1));
    }
  });
  if (current.length > 1) segments.push(current);

  let body = '';
  for (const seg of segments) {
    body += `<polyline class="spark-line" points="${seg.join(' ')}"/>`;
  }
  for (const x of failures) {
    body += `<line class="spark-fail" x1="${x}" y1="${height - pad}" x2="${x}" y2="${height}"/>`;
  }
  if (values.length === 1 && values[0] > 0) {
    body += `<circle class="spark-dot" cx="1" cy="${y(values[0]).toFixed(1)}" r="1.6"/>`;
  }
  return svg(width, height, body, extra);
}

// ---------------------------------------------------------------------------
// uptime strip — one bar per time bucket, the fastest way to read history
// ---------------------------------------------------------------------------

/**
 * Render a strip of buckets whose colour encodes uptime. Buckets with no data
 * are drawn as hollow slots so "no probe" never reads as "down".
 */
export function uptimeStrip(points, { width = 640, height = 34 } = {}) {
  const n = points.length;
  if (n === 0) {
    return `<div class="strip-empty muted">—</div>`;
  }
  const gap = n > 120 ? 0 : 1;
  const barWidth = Math.max(1, (width - gap * (n - 1)) / n);
  const span = n > 1
    ? new Date(points[n - 1].ts).getTime() - new Date(points[0].ts).getTime()
    : 0;

  let body = '';
  points.forEach((p, i) => {
    const x = (i * (barWidth + gap)).toFixed(2);
    const label = p.total > 0
      ? `${esc(fmtDateTime(p.ts))} · ${esc(fmtPct(p.uptime_pct))} · ${p.ok}/${p.total} probes`
      : `${esc(fmtDateTime(p.ts))} · no data`;
    const cls = p.total > 0 ? uptimeClass(p.uptime_pct) : 'u-empty';
    body += `<rect class="bar ${cls}" x="${x}" y="0" width="${barWidth.toFixed(2)}" height="${height}" rx="${barWidth > 3 ? 1.5 : 0}"><title>${label}</title></rect>`;
  });

  return `<div class="strip" data-span="${span}">${svg(width, height, body, 'role="img" aria-label="Uptime history" preserveAspectRatio="none"')}</div>`;
}

// ---------------------------------------------------------------------------
// latency chart — average line with a min/max band
// ---------------------------------------------------------------------------

/**
 * Draw response time over a window. The band shows the spread between the
 * fastest and slowest probe in each bucket, which answers "is it slow, or is it
 * erratic?" — a question a single average line cannot answer.
 */
export function latencyChart(points, { width = 720, height = 190, emptyText = 'No data' } = {}) {
  const padLeft = 52;
  const padRight = 10;
  const padTop = 12;
  const padBottom = 26;
  const plotW = Math.max(10, width - padLeft - padRight);
  const plotH = Math.max(10, height - padTop - padBottom);

  const withData = points.filter((p) => p.total > 0 && p.avg_ms > 0);
  if (withData.length === 0) {
    return `<div class="chart-empty muted">${esc(emptyText)}</div>`;
  }

  const top = niceMax(Math.max(...withData.map((p) => Math.max(p.max_ms, p.avg_ms))));
  const span = points.length > 1
    ? new Date(points[points.length - 1].ts).getTime() - new Date(points[0].ts).getTime()
    : 0;
  const stepX = points.length > 1 ? plotW / (points.length - 1) : plotW;
  const y = (v) => padTop + plotH - (Math.min(v, top) / top) * plotH;

  // Grid: three lines, labelled, with the unit on the top one.
  let body = '';
  for (const frac of [1, 0.5, 0]) {
    const gy = padTop + plotH - frac * plotH;
    body += `<line class="grid" x1="${padLeft}" y1="${gy.toFixed(1)}" x2="${padLeft + plotW}" y2="${gy.toFixed(1)}"/>`;
    body += `<text class="axis" x="${padLeft - 8}" y="${(gy + 3.5).toFixed(1)}" text-anchor="end">${esc(fmtLatency(top * frac))}</text>`;
  }

  // Segments: a bucket with no data breaks both the line and the band.
  const segments = [];
  let current = [];
  points.forEach((p) => {
    if (p.total > 0 && p.avg_ms > 0) {
      current.push(p);
    } else if (current.length) {
      segments.push(current);
      current = [];
    }
  });
  if (current.length) segments.push(current);

  // Index each bucket once so the x lookup stays O(1); a 90-day window has up
  // to 500 buckets and indexOf per point would make this quadratic.
  const index = new Map(points.map((p, i) => [p, i]));
  const xOf = (p) => padLeft + index.get(p) * stepX;

  for (const seg of segments) {
    if (seg.length > 1) {
      const upper = seg.map((p) => `${xOf(p).toFixed(1)},${y(p.max_ms || p.avg_ms).toFixed(1)}`);
      const lower = seg.slice().reverse().map((p) => `${xOf(p).toFixed(1)},${y(p.min_ms || p.avg_ms).toFixed(1)}`);
      body += `<polygon class="band" points="${upper.concat(lower).join(' ')}"/>`;
    }
    const line = seg.map((p) => `${xOf(p).toFixed(1)},${y(p.avg_ms).toFixed(1)}`);
    body += `<polyline class="line" points="${line.join(' ')}"/>`;
    if (seg.length === 1) {
      body += `<circle class="dot" cx="${xOf(seg[0]).toFixed(1)}" cy="${y(seg[0].avg_ms).toFixed(1)}" r="2.5"/>`;
    }
  }

  // Hover targets: one per bucket, wide enough to hit with a mouse.
  points.forEach((p, i) => {
    const x = padLeft + i * stepX - stepX / 2;
    if (p.total === 0) return;
    const label = `${esc(fmtDateTime(p.ts))} · avg ${esc(fmtLatency(p.avg_ms))} · min ${esc(fmtLatency(p.min_ms))} · max ${esc(fmtLatency(p.max_ms))} · ${esc(fmtPct(p.uptime_pct))} of ${p.total}`;
    body += `<rect class="hit" x="${x.toFixed(1)}" y="${padTop}" width="${stepX.toFixed(1)}" height="${plotH}"><title>${label}</title></rect>`;
  });

  // X axis: first, middle and last tick only.
  if (points.length > 1) {
    for (const i of [0, Math.floor(points.length / 2), points.length - 1]) {
      const p = points[i];
      const anchor = i === 0 ? 'start' : (i === points.length - 1 ? 'end' : 'middle');
      body += `<text class="axis" x="${(padLeft + i * stepX).toFixed(1)}" y="${height - 8}" text-anchor="${anchor}">${esc(fmtTick(p.ts, span))}</text>`;
    }
  }

  return svg(width, height, body, 'role="img" aria-label="Response time over time"');
}

// ---------------------------------------------------------------------------
// heartbeat — one mark per check, for the wall
// ---------------------------------------------------------------------------

/**
 * Draw the run of checks a sensor has made, oldest first.
 *
 * This is deliberately not the sparkline. A sparkline's zero means "the probe
 * failed" *and* "the probe answered instantly", and that ambiguity is fine for a
 * latency trace — it is drawn as a break either way. It is not fine for an
 * up/down strip: a working sensor that answered in under a millisecond would be
 * painted red. So the server sends the outcome of each check as its own list of
 * booleans, and this draws exactly that, one mark each, with no arithmetic in
 * between.
 *
 * The marks stretch to the width they are given rather than sitting in a fixed
 * 240 pixels of it, so a narrow tile and a wide one both show the same run.
 *
 * `label(i, ok)` supplies the hover text — it comes from the caller because the
 * catalogue lives there, and this file has no dependency on it on purpose.
 */
export function heartbeat(beats, { width = 240, height = 22, empty = '—', label } = {}) {
  if (!beats || beats.length === 0) {
    return `<div class="heartbeat"><span class="heartbeat-empty">${esc(empty)}</span></div>`;
  }
  const n = beats.length;
  const slot = width / n;
  // A hairline between marks once they get numerous; below that the gaps would
  // eat the marks themselves.
  const bar = Math.max(0.8, slot - (n > 60 ? 0.4 : 2));
  let body = '';
  beats.forEach((ok, i) => {
    const h = ok ? height : height * 0.55;
    const title = label ? label(i, ok) : (ok ? 'ok' : 'fail');
    body += `<rect class="beat ${ok ? 'h-ok' : 'h-fail'}"` +
      ` x="${(i * slot).toFixed(2)}" y="${(height - h).toFixed(2)}"` +
      ` width="${bar.toFixed(2)}" height="${h.toFixed(2)}" rx="1">` +
      `<title>${esc(title)}</title></rect>`;
  });
  return svg(width, height, body, 'preserveAspectRatio="none" role="img"');
}

// ---------------------------------------------------------------------------
// donut — the status split in the dashboard header
// ---------------------------------------------------------------------------

/**
 * A ring split by status. Uses pathLength="100" so the arcs can be expressed in
 * percentages directly, which avoids trigonometry and rounding drift.
 */
export function donut(parts, { size = 92, thickness = 11 } = {}) {
  const total = parts.reduce((sum, p) => sum + p.value, 0);
  const radius = (size - thickness) / 2;
  const centre = size / 2;

  let body = `<circle class="donut-track" cx="${centre}" cy="${centre}" r="${radius}" fill="none" stroke-width="${thickness}"/>`;
  if (total > 0) {
    let offset = 0;
    for (const part of parts) {
      if (part.value <= 0) continue;
      const share = (part.value / total) * 100;
      body += `<circle class="donut-seg ${part.cls}" cx="${centre}" cy="${centre}" r="${radius}" fill="none" stroke-width="${thickness}" pathLength="100" stroke-dasharray="${share.toFixed(3)} ${(100 - share).toFixed(3)}" stroke-dashoffset="${(-offset).toFixed(3)}" transform="rotate(-90 ${centre} ${centre})"><title>${esc(part.label)}: ${part.value}</title></circle>`;
      offset += share;
    }
  }
  return svg(size, size, body, 'role="img"');
}

/** Width of the element a chart will be drawn into, clamped to sane bounds. */
export function widthOf(el, fallback = 640) {
  const w = el?.clientWidth || fallback;
  return Math.max(220, Math.round(w));
}

