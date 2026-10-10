// Password strength.
//
// The dashboard asks for a password twice — once at setup, once when changing
// it — and until now it said nothing about how good the answer was beyond the
// server's eight-character floor. This is a small estimator with no dependency:
// it looks at length, character variety and a short list of the passwords that
// turn up in every breach dump, and it says which one to fix.
//
// It informs and does not police. The server accepts anything eight characters
// or longer, and so does this screen: on a private network, a short password is
// the owner's call to make. The meter is there so the call is an informed one.

import { t } from './i18n.js';

// Deliberately short. The point is to catch the handful a first attempt reaches
// for, not to be a dictionary — a real list would be a dependency and a hundred
// kilobytes to solve a problem that only a warning can solve anyway.
const COMMON = new Set([
  'password', 'passw0rd', 'password1', 'password12', 'password123',
  '12345678', '123456789', '1234567890', 'qwertyui', 'qwerty123',
  'iloveyou', 'admin123', 'admin1234', 'letmein1', 'welcome1',
  'sunshine', 'princess', 'football', 'baseball', 'monkey123',
  'dragon123', 'abc12345', 'homelab', 'homelab123', 'weatherglass',
]);

const LEVELS = ['weak', 'fair', 'good', 'strong'];

/**
 * evaluate scores a password from 0 (weak) to 3 (strong).
 *
 * The two things it returns are separate on purpose: `level` drives the bar and
 * the word, `tip` names the single change that would help most. A meter that
 * only says "weak" tells a person what they already knew.
 */
export function evaluate(password) {
  const pw = password || '';
  if (!pw) return { level: 0, label: null, tip: null, empty: true };

  const classes = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((re) => re.test(pw)).length;
  // A single repeated character, or one of the famous ones, is never strong
  // however long it is.
  const common = COMMON.has(pw.toLowerCase()) || /^(.)\1+$/.test(pw);

  let score = 0;
  if (pw.length >= 8) score += 1;
  if (pw.length >= 12) score += 1;
  if (pw.length >= 16) score += 1;
  score += Math.max(0, classes - 1);

  const level = common ? 0 : Math.max(0, Math.min(3, score - 1));

  let tip = null;
  if (common) tip = 'password.tip.common';
  else if (pw.length < 12) tip = 'password.tip.longer';
  else if (classes < 3) tip = 'password.tip.mix';
  else if (level < 3) tip = 'password.tip.more';
  else tip = 'password.tip.good';

  return { level, label: `password.level.${LEVELS[level]}`, tip, empty: false };
}

/**
 * mount builds the meter inside `host` and keeps it in step with `input`.
 *
 * It returns the refresh function so the caller can drive it from the same
 * place it watches the field — the setup screen resets the form between steps
 * and needs the meter to follow.
 */
export function mount(input, host) {
  if (!input || !host) return () => {};
  host.classList.add('pw-meter');
  host.innerHTML = `
    <div class="pw-bar" aria-hidden="true"><span class="pw-fill"></span></div>
    <div class="pw-line">
      <span class="pw-word" role="status" aria-live="polite"></span>
      <span class="pw-tip"></span>
    </div>`;
  const fill = host.querySelector('.pw-fill');
  const word = host.querySelector('.pw-word');
  const tip = host.querySelector('.pw-tip');

  const update = () => {
    const { level, label, tip: tipKey, empty } = evaluate(input.value);
    host.dataset.level = empty ? 'none' : String(level);
    fill.style.width = `${empty ? 0 : (level + 1) * 25}%`;
    word.textContent = empty ? '' : t(label);
    tip.textContent = empty || !tipKey ? '' : t(tipKey);
  };

  input.addEventListener('input', update);
  update();
  return update;
}
