/*
 * The demo shim.
 *
 * This file is generated into the demo by `make demo` (cmd/demogen) and is the
 * only part of the demo that is not the product. Everything else — the shell, the
 * assets, the answers — is the real thing, so this file is deliberately small and
 * does four things and nothing else:
 *
 *   1. It replaces window.fetch. A GET that was recorded while the demo was built
 *      is answered with exactly the bytes the server sent then; a GET that was not
 *      recorded is refused, loudly. Nothing is invented here, and a panel that
 *      asks for something the recording does not have says so instead of drawing a
 *      blank.
 *   2. It replaces window.EventSource with a socket that never connects. The live
 *      indicator therefore reads "waiting", which is the truth: there is no
 *      installation behind this page, and no probe result will ever arrive. Handing
 *      it a fake "open" would be the one lie in the whole exercise.
 *   3. It replaces localStorage and sessionStorage with an object in memory. The
 *      dashboard does not use either (checked: no reference anywhere in app.js),
 *      but a static page must not be able to leave anything in a visitor's browser
 *      even if a later version starts to.
 *   4. It replays the clock. See below.
 *
 * The two writes the page is allowed to make — the interface preferences behind
 * the settings gear (PUT /api/settings) and the tour (POST /api/onboarding/tour) —
 * are applied to the recorded session in memory, so they behave exactly as they do
 * in the product for as long as the tab is open, and are gone on reload. That is
 * what the bar above the dashboard promises.
 *
 * Everything else that writes is refused with a 501 and a sentence, including
 * signing out, creating a sensor, and the backup download.
 */
'use strict';

(function () {
  var fixtures = window.__WG_FIXTURES__;
  if (!fixtures || !fixtures.responses) return;

  /* ------------------------------------------------------------ the clock
   *
   * The recording is a snapshot: it was taken at one moment, and its history runs
   * up to that moment. Served a week later it would still draw that same week, and
   * an indicator that says "checked 4 seconds ago" would say "checked 7 days ago" —
   * the one thing on the page that would look broken rather than dated.
   *
   * So every timestamp in the fixtures is shifted forward by the same amount: the
   * gap between when the recording was made and when this page was opened. The
   * whole dataset moves together — probe results, rollups, incident windows, the
   * window ends of every chart — so it stays as internally consistent as it was,
   * and the page always looks like what it is a picture of: a homelab being watched
   * right now.
   *
   * The data is invented either way. Nothing here makes it less invented; it only
   * keeps the invented clock running, which the bar at the top of the page says.
   */

  var RECORDED_AT = Date.parse(fixtures.generated_at);
  var SHIFT = isNaN(RECORDED_AT) ? 0 : Date.now() - RECORDED_AT;

  // Go writes time.Time as RFC 3339, with up to nine digits of a second. The
  // pattern is narrow on purpose: a string that merely contains a date — a log
  // line, a TLS message — must be left exactly as it was recorded.
  var STAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

  function shift(value) {
    if (typeof value === 'string') {
      if (SHIFT === 0 || !STAMP.test(value)) return value;
      // Safari parses an ISO string with more than three fractional digits
      // inconsistently, so the fraction is trimmed before the date is read.
      var at = Date.parse(value.replace(/(\.\d{3})\d+/, '$1'));
      return isNaN(at) ? value : new Date(at + SHIFT).toISOString();
    }
    if (Array.isArray(value)) {
      for (var i = 0; i < value.length; i++) value[i] = shift(value[i]);
      return value;
    }
    if (value && typeof value === 'object') {
      for (var key in value) {
        if (Object.prototype.hasOwnProperty.call(value, key)) value[key] = shift(value[key]);
      }
      return value;
    }
    return value;
  }

  var RESPONSES = {};
  for (var recorded in fixtures.responses) {
    if (Object.prototype.hasOwnProperty.call(fixtures.responses, recorded)) {
      RESPONSES[recorded] = shift(fixtures.responses[recorded]);
    }
  }

  /* --------------------------------------------------------- the responses */

  var NOT_HERE = 'This is a demo: it is a recording of ProbeGlass with example '
    + 'data and no installation behind it, so nothing can be changed here.';

  var NOT_RECORDED = 'This is a demo. This request was not part of the recording, '
    + 'so there is no answer to give.';

  // The session carries the interface preferences, which is where the dashboard
  // reads them from (app.js: Object.assign(ui, state.session.ui)). The settings
  // route is here too because the same preferences are in it, and reading a stale
  // copy after changing something would be worse than not having it at all.
  function session() { return RESPONSES['GET /api/session']; }
  function settings() { return RESPONSES['GET /api/settings']; }

  function mergeUI(pairs) {
    if (!pairs || typeof pairs !== 'object') return;
    for (var key in pairs) {
      if (!Object.prototype.hasOwnProperty.call(pairs, key)) continue;
      var value = String(pairs[key]);
      var s = session();
      if (s && s.ui) s.ui[key] = value;
      var g = settings();
      if (g && g.ui) g.ui[key] = value;
    }
  }

  /* ---------------------------------------------------------- the key space
   *
   * A request is named the same way cmd/demogen names it: the method, the path,
   * and the query parameters sorted by name, escaped with encodeURIComponent.
   * The generator has the matching escaper on the Go side.
   */

  function canonical(method, path, params) {
    var pairs = [];
    params.forEach(function (value, name) { pairs.push([name, value]); });
    pairs.sort(function (a, b) { return a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0; });
    var out = method + ' ' + path;
    for (var i = 0; i < pairs.length; i++) {
      out += (i === 0 ? '?' : '&')
        + encodeURIComponent(pairs[i][0]) + '=' + encodeURIComponent(pairs[i][1]);
    }
    return out;
  }

  /* ------------------------------------------------------------- the answers */

  function body(text, status, statusText) {
    return {
      ok: status >= 200 && status < 300,
      status: status,
      statusText: statusText,
      headers: new Headers({ 'Content-Type': 'application/json' }),
      url: location.href,
      text: function () { return Promise.resolve(text); },
      json: function () { return Promise.resolve(JSON.parse(text)); },
      clone: function () { return body(text, status, statusText); },
    };
  }

  function answer(value) {
    return Promise.resolve(body(JSON.stringify(value), 200, 'OK'));
  }

  function refuse(message) {
    return Promise.resolve(body(JSON.stringify({ error: message }), 501, 'Not Implemented'));
  }

  /**
   * incidentFilter applies the incident log's filter row to the recorded list.
   *
   * The filter is four comparisons against columns that are already in the
   * recorded rows, so it can be reproduced exactly — and it has to be, because one
   * of the four is a free-text box (app.js sends what somebody typed into "q"), and
   * a free-text filter cannot be enumerated into fixtures ahead of time. The
   * matching below is the one in internal/store/incidents.go: a case-insensitive
   * substring over the cause and the acknowledgement note, plus the sensor's own
   * name and target, newest first, cut to the limit.
   */
  function incidentFilter(list, params) {
    var sensor = params.get('sensor');
    var state = params.get('state');
    var open = params.get('open') === '1';
    var range = params.get('range');
    var q = (params.get('q') || '').trim().toLowerCase();

    var since = 0;
    if (range) {
      var units = { h: 3600000, d: 86400000 };
      var m = /^(\d+)([hd])$/.exec(range);
      if (m) since = Date.now() - Number(m[1]) * units[m[2]];
    }

    var out = list.filter(function (i) {
      if (sensor && String(i.sensor_id) !== String(sensor)) return false;
      if (state === 'open' || (open && !state)) { if (i.resolved_at) return false; }
      if (state === 'resolved' && !i.resolved_at) return false;
      if (since && Date.parse(i.started_at) < since) return false;
      if (q) {
        var hay = [i.cause, i.ack_note, i.sensor_name, i.sensor_target]
          .map(function (v) { return String(v || '').toLowerCase(); }).join(' ');
        if (hay.indexOf(q) === -1) return false;
      }
      return true;
    });

    var limit = Number(params.get('limit'));
    if (limit > 0) out = out.slice(0, limit);
    return out;
  }

  var realFetch = typeof window.fetch === 'function' ? window.fetch.bind(window) : null;

  window.fetch = function (input, init) {
    // Everything the dashboard fetches is a string path of its own. Anything else
    // — a Request object, a tile server, a call somebody adds later — is not this
    // shim's business and goes out over the network untouched.
    if (typeof input !== 'string') {
      return realFetch ? realFetch(input, init) : refuse(NOT_RECORDED);
    }
    var url = new URL(input, location.href);
    if (url.origin !== location.origin) {
      return realFetch ? realFetch(input, init) : refuse(NOT_RECORDED);
    }

    var method = (init && init.method ? init.method : 'GET').toUpperCase();
    var key = canonical(method, url.pathname, url.searchParams);

    if (method === 'GET') {
      if (key === 'GET /api/incidents' || key.indexOf('GET /api/incidents?') === 0) {
        var full = RESPONSES['GET /api/incidents?limit=200'] || [];
        return answer(incidentFilter(full, url.searchParams));
      }
      if (Object.prototype.hasOwnProperty.call(RESPONSES, key)) {
        return Promise.resolve(body(JSON.stringify(RESPONSES[key]), 200, 'OK'));
      }
      // A recorded search that nobody made: the search box matches on any
      // substring, and only the words the example data is made of were asked for
      // while the demo was built. An empty result is a true answer — it is what the
      // server says for a query that matches nothing.
      if (key.indexOf('GET /api/search?') === 0) return answer({ hits: [], truncated: false });
      console.warn('demo: nothing was recorded for', key);
      return refuse(NOT_RECORDED);
    }

    var payload = null;
    if (init && init.body) {
      try { payload = JSON.parse(init.body); } catch (e) { payload = null; }
    }

    if (key === 'PUT /api/settings') {
      mergeUI(payload && payload.ui);
      return answer({ ui: (session() || {}).ui || {} });
    }
    if (key === 'POST /api/onboarding/tour') {
      mergeUI({ tour_version: String((payload && payload.version) || 0) });
      return answer({ ok: true });
    }
    return refuse(NOT_HERE);
  };

  /* ---------------------------------------------------------- the live feed
   *
   * A socket that reports CONNECTING and stays there. app.js reads readyState to
   * label the indicator beside the account chip, and "waiting" is the honest label:
   * a dashboard that is not being fed by anything is exactly what this is.
   */
  function DemoEventSource(url) {
    this.url = url;
    this.readyState = DemoEventSource.CONNECTING;
    this.onopen = null;
    this.onerror = null;
    this.onmessage = null;
  }
  DemoEventSource.CONNECTING = 0;
  DemoEventSource.OPEN = 1;
  DemoEventSource.CLOSED = 2;
  DemoEventSource.prototype.addEventListener = function () {};
  DemoEventSource.prototype.removeEventListener = function () {};
  DemoEventSource.prototype.close = function () { this.readyState = DemoEventSource.CLOSED; };
  window.EventSource = DemoEventSource;

  /* ------------------------------------------------------------ the storage
   *
   * The dashboard keeps its preferences on the server, not here, so this is
   * belt and braces: a page that says it stores nothing should not have the means
   * to store anything.
   */
  function memoryStorage() {
    var data = Object.create(null);
    return {
      get length() { return Object.keys(data).length; },
      key: function (i) { return Object.keys(data)[i] ?? null; },
      getItem: function (k) { return Object.prototype.hasOwnProperty.call(data, String(k)) ? data[String(k)] : null; },
      setItem: function (k, v) { data[String(k)] = String(v); },
      removeItem: function (k) { delete data[String(k)]; },
      clear: function () { data = Object.create(null); },
    };
  }
  try {
    Object.defineProperty(window, 'localStorage', { value: memoryStorage(), writable: false });
    Object.defineProperty(window, 'sessionStorage', { value: memoryStorage(), writable: false });
  } catch (e) {
    // A browser that refuses to redefine them keeps its own; the page still works,
    // it simply could write, and the note in the bar covers that.
  }
})();
