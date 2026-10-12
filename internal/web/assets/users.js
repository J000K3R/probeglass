// Accounts, roles and the second factor.
//
// The dashboard grew up with one password for everybody, so "who did this?" had
// no answer — an acknowledgement could only ever say "dashboard". This module is
// the screen that gives the question an answer: who may sign in, what each of them
// is allowed to do, and what stands between their password and the dashboard.
//
// The split of responsibility is the same as everywhere else in the assets: the
// markup is built here as strings, the behaviour is wired here too, and the server
// is the one that decides. Nothing in this file hides a control because of a role —
// that is `can()` in app.js and the `denied()` attribute it produces, applied from
// the caller. What is here is the dialogs, because they are only ever opened from
// the settings panel.

import { t, locale } from './i18n.js';
import { esc } from './charts.js';

// ---------------------------------------------------------------------------
// markup
// ---------------------------------------------------------------------------

/**
 * panel renders the user table.
 *
 * `me` is the signed-in account. It is used to mark that row and to grey out the
 * two things the server refuses for it — disabling and deleting yourself — before
 * the request is made, so the rule is visible rather than discovered. The server
 * refuses either way; the dashboard should not be the thing that teaches it.
 */
export function panel({ users, me }) {
  const rows = users.map((u) => {
    const self = me && u.id === me.id;
    const actions = [
      button('user-edit', u.id, t('common.edit'), ''),
      button('user-password', u.id, t('users.reset'), ''),
      button('user-toggle', u.id, u.disabled ? t('users.enable') : t('users.disable'), self ? t('users.you') : ''),
      button('user-delete', u.id, t('common.delete'), self ? t('users.you') : '', 'btn-danger'),
    ].join('');
    return `
      <tr class="user-row${u.disabled ? ' is-disabled' : ''}" data-id="${u.id}">
        <td class="user-name">
          ${esc(u.username)}${self ? ` <span class="muted small">(${esc(t('users.you'))})</span>` : ''}
          ${u.disabled ? `<span class="pill st-paused">${esc(t('users.disabled'))}</span>` : ''}
        </td>
        <td>${esc(t(`users.role.${u.role}`))}</td>
        <td>${esc(u.totp_enabled ? t('users.twoFactorOn') : t('users.twoFactorOff'))}</td>
        <td class="muted small">${u.last_login_at ? esc(new Date(u.last_login_at).toLocaleString(locale())) : esc(t('users.never'))}</td>
        <td class="row-actions">${actions}</td>
      </tr>`;
  }).join('');

  return `
    <div class="table-wrap">
      <table class="data-table users-table">
        <thead><tr>
          <th>${esc(t('users.username'))}</th>
          <th>${esc(t('users.role'))}</th>
          <th>${esc(t('users.twoFactor'))}</th>
          <th>${esc(t('users.lastLogin'))}</th>
          <th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>
    <div class="form-actions">
      <button class="btn btn-primary" data-action="user-new">${esc(t('users.new'))}</button>
    </div>`;
}

/** button builds one row action, greyed out with a reason when it is not allowed. */
function button(action, id, label, why, cls = '') {
  const title = why ? `${label} — ${why}` : label;
  return `<button class="btn btn-small${cls ? ` ${cls}` : ''}" data-action="${action}" data-id="${id}"
    title="${esc(title)}"${why ? ' disabled' : ''}>${esc(label)}</button>`;
}

/**
 * twoFactorBlock renders the second-factor section of the Access panel.
 *
 * It is written for the person looking at their own account, not for an
 * administrator looking at somebody else's: the only account anyone can enrol here
 * is their own. An administrator cannot switch a second factor on for another
 * person, because the whole point of the factor is possession of the device — a
 * secret the administrator knows is not a second factor.
 */
export function twoFactorBlock(user) {
  const on = Boolean(user && user.totp_enabled);
  const left = (user && user.recovery_codes_left) || 0;
  const actions = on
    ? `<button class="btn" data-action="totp-recovery">${esc(t('totp.renew'))}</button>
       <button class="btn btn-danger" data-action="totp-disable">${esc(t('totp.disable'))}</button>`
    : `<button class="btn btn-primary" data-action="totp-start">${esc(t('totp.enable'))}</button>`;

  return `
    <h3 class="section-title">${esc(t('totp.title'))}</h3>
    <p class="${on ? 'muted' : 'muted'}">${esc(on ? t('totp.on') : t('totp.off'))}</p>
    <p class="muted small">${esc(t('totp.why'))}</p>
    ${on && left ? `<p class="muted small">${esc(t('totp.codesLeft', { n: left }))}</p>` : ''}
    <div class="form-actions">${actions}</div>`;
}

/** userFields is the body of the create/edit dialog. */
export function userFields(roles, existing) {
  const role = existing ? existing.role : 'viewer';
  const picker = roles.map((r) => `
    <label class="mode-card">
      <input type="radio" name="role" value="${esc(r)}"${r === role ? ' checked' : ''}>
      <span class="mode-name">${esc(t(`users.role.${r}`))}</span>
      <span class="mode-body">${esc(t(`users.role.${r}Hint`))}</span>
    </label>`).join('');

  // The name is a handle people type at the sign-in screen, so it is fixed once
  // created. Everything else about the account is editable.
  const name = existing
    ? `<label class="field"><span>${esc(t('users.username'))}</span>
         <input type="text" value="${esc(existing.username)}" disabled></label>`
    : `<label class="field"><span>${esc(t('users.username'))}</span>
         <input name="username" type="text" required maxlength="64"
                autocapitalize="none" autocorrect="off" spellcheck="false"></label>`;

  const password = existing ? '' : `
    <label class="field"><span>${esc(t('users.password'))}</span>
      <input name="password" type="password" autocomplete="new-password" minlength="8" required>
      <span class="muted small">${esc(t('users.passwordHint'))}</span></label>`;

  return `
    ${name}
    ${password}
    <fieldset class="mode-pick"><legend>${esc(t('users.role'))}</legend>${picker}</fieldset>
    <p class="muted small">${esc(t('users.hint'))}</p>`;
}

/** passwordFields is the administrator's "reset this account's password" dialog. */
export function passwordFields(user) {
  return `
    <p class="muted">${esc(t('users.resetHint'))}</p>
    <label class="field"><span>${esc(t('users.password'))}</span>
      <input name="new_password" type="password" autocomplete="new-password" minlength="8" required>
      <span class="muted small">${esc(t('users.passwordHint'))}</span></label>
    <p class="muted small">${esc(t('users.username'))}: <b>${esc(user.username)}</b></p>`;
}

/** confirmFields asks for the password before a change that guards the account. */
export function confirmFields(message) {
  return `
    <p class="muted">${esc(message)}</p>
    <label class="field"><span>${esc(t('totp.passwordPrompt'))}</span>
      <input name="password" type="password" autocomplete="current-password" required></label>`;
}

/**
 * enrolment renders the scan step.
 *
 * The image is fetched from the server rather than encoded here, and the key is
 * printed underneath as well: a camera that will not focus is a common enough
 * problem that typing twenty-six characters by hand has to be possible.
 */
export function enrolment(secret) {
  return `
    <p class="muted">${esc(t('totp.scan'))}</p>
    <div class="qr-wrap">
      <img class="qr" src="/api/me/totp/qr" alt="${esc(t('totp.title'))}" width="220" height="220">
    </div>
    <p class="muted small">${esc(t('totp.orType'))}</p>
    <code class="secret-key">${esc(secret)}</code>
    <label class="field"><span>${esc(t('totp.code'))}</span>
      <input name="code" type="text" inputmode="numeric" autocomplete="one-time-code"
             maxlength="32" required spellcheck="false"></label>`;
}

/** recoveryMarkup shows the one-time codes. This is the only time they exist in the clear. */
export function recoveryMarkup(codes) {
  const list = codes.map((c) => `<li><code>${esc(c)}</code></li>`).join('');
  return `
    <p class="muted">${esc(t('totp.codesBody'))}</p>
    <ol class="recovery-codes">${list}</ol>
    <div class="form-actions">
      <button class="btn" type="button" data-action="copy-codes">${esc(t('totp.codesCopy'))}</button>
    </div>`;
}

// ---------------------------------------------------------------------------
// behaviour
// ---------------------------------------------------------------------------

/**
 * mount wires the dialogs.
 *
 * `modal` is the shared dialog controller from app.js — one form, several modes —
 * so these screens get the same focus handling and error line as everything else.
 * `afterChange` redraws the settings panel, which is the only view any of this
 * changes.
 */
export function mount({ api, toast, modal, state, afterChange }) {
  /**
   * submit performs the request behind one dialog.
   *
   * It returns whether the dialog the caller opened is finished with. Enrolling
   * and renewing both end by putting a *different* dialog on screen — the one with
   * the recovery codes — so for those two the answer is no, and the caller must
   * leave the modal alone.
   */
  async function submit(mode, form) {
    if (mode === 'user') {
      const id = Number(form.dataset.id || 0);
      const role = form.querySelector('input[name="role"]:checked')?.value || 'viewer';
      if (id) {
        await api(`/api/users/${id}`, { method: 'PUT', body: { role } });
        toast(t('users.updated'));
      } else {
        await api('/api/users', {
          method: 'POST',
          body: { username: form.elements.username.value.trim(), password: form.elements.password.value, role },
        });
        toast(t('users.created'));
      }
      await afterChange();
      return true;
    }

    if (mode === 'user-password') {
      const id = Number(form.dataset.id);
      await api(`/api/users/${id}/password`, {
        method: 'POST',
        body: { new_password: form.elements.new_password.value },
      });
      toast(t('users.resetDone'));
      await afterChange();
      return true;
    }

    if (mode === 'totp') {
      const answer = await api('/api/me/totp/confirm', {
        method: 'POST',
        body: { code: form.elements.code.value.trim() },
      });
      toast(t('totp.enabled'));
      // Straight into the one moment the codes exist. Closing the enrolment
      // dialog first and opening a second one would leave a frame in which the
      // person is looking at a dashboard with the factor on and no codes in hand.
      openRecovery(answer.recovery_codes);
      await afterChange();
      return false;
    }

    if (mode === 'totp-off') {
      await api('/api/me/totp/disable', {
        method: 'POST',
        body: { password: form.elements.password.value },
      });
      toast(t('totp.disabled'));
      await afterChange();
      return true;
    }

    if (mode === 'totp-codes') {
      const answer = await api('/api/me/totp/recovery', {
        method: 'POST',
        body: { password: form.elements.password.value },
      });
      toast(t('totp.codesRenewed'));
      openRecovery(answer.recovery_codes);
      await afterChange();
      return false;
    }

    if (mode === 'recovery') {
      // Nothing to send: the point of this dialog is that the codes are written
      // down. Its only button is the acknowledgement, and the closing is the
      // caller's.
      await afterChange();
      return true;
    }

    return true;
  }

  function openUserEditor(existing) {
    modal.open({
      title: existing ? t('users.edit') : t('users.new'),
      body: userFields(state.roles, existing),
      mode: 'user',
      id: existing ? existing.id : '',
      saveLabel: existing ? t('common.save') : t('users.new'),
    });
  }

  function openUserPassword(user) {
    modal.open({
      title: t('users.reset'),
      body: passwordFields(user),
      mode: 'user-password',
      id: user.id,
      saveLabel: t('users.reset'),
    });
  }

  function openConfirm({ title, message, mode }) {
    modal.open({
      title,
      body: confirmFields(message),
      mode,
      saveLabel: t('common.confirm'),
    });
  }

  function openRecovery(codes) {
    modal.open({
      title: t('totp.codesTitle'),
      body: recoveryMarkup(codes),
      mode: 'recovery',
      saveLabel: t('totp.codesDone'),
      // No way out but the acknowledgement: once this dialog is gone the codes
      // are gone. Dismissing it by accident would mean switching the second
      // factor off again to get back in.
      locked: true,
    });
  }

  /** start asks the server for a fresh secret and shows the scan step. */
  async function startEnrolment() {
    const { secret } = await api('/api/me/totp/start', { method: 'POST' });
    modal.open({
      title: t('totp.title'),
      body: enrolment(secret),
      mode: 'totp',
      saveLabel: t('totp.confirm'),
    });
  }

  return { submit, openUserEditor, openUserPassword, openConfirm, openRecovery, startEnrolment };
}
