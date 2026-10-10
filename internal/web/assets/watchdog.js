// Boot watchdog.
//
// The loading spinner is the screen with no way forward: if app.js never runs
// — a syntax error, an asset that failed to load, a server that accepted the
// TCP connection and then never answered — the user sits on a spinner forever
// and nothing on the page says why. This module loads separately from app.js
// and imports nothing, so it is the one piece of code guaranteed to run. If the
// dashboard has not signalled a finished boot by the deadline below, it
// replaces the spinner with a message that names the likely cause.
//
// The text is a literal rather than a catalogue lookup on purpose: i18n.js may
// itself be the thing that failed to load.

(function () {
  'use strict';

  // Generous: a cold start on a Raspberry Pi against a large database is slow,
  // and a false alarm is worse than waiting a few seconds longer.
  const DEADLINE_MS = 12000;

  const boot = document.getElementById('boot');
  if (!boot) return;

  window.setTimeout(function () {
    if (window.__weatherglassBooted) return;
    // boot() may have replaced the contents already without flagging success;
    // only the untouched spinner is a failure.
    if (!boot.querySelector('.spinner')) return;

    boot.textContent = '';

    const panel = document.createElement('div');
    panel.className = 'boot-error';

    const heading = document.createElement('h2');
    heading.textContent = 'The dashboard did not load';
    panel.append(heading);

    const text = document.createElement('p');
    text.className = 'form-error';
    text.textContent =
      'The server has not answered yet. Check that Weatherglass is running, then reload the page.';
    panel.append(text);

    const help = document.createElement('p');
    help.className = 'muted';
    // Naming a keystroke here would help half the readers: F12 is Chrome,
    // Firefox and Edge, but Safari has no F12 and hides its console behind a
    // preference that is off by default. So the text names the menu instead,
    // which is the only route that works in every one of them.
    help.textContent =
      'If a reload does not help, open the developer console — in Chrome, ' +
      'Firefox and Edge with F12, in Safari via Settings, Advanced, "Show ' +
      'features for web developers", then Develop, Show JavaScript Console. ' +
      'A failed request or a script error there says what went wrong.';
    panel.append(help);

    const retry = document.createElement('button');
    retry.type = 'button';
    retry.className = 'btn btn-primary';
    retry.textContent = 'Reload';
    retry.addEventListener('click', function () {
      window.location.reload();
    });
    panel.append(retry);

    boot.append(panel);
  }, DEADLINE_MS);
})();
