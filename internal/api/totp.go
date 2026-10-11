package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"

	"github.com/j000k3r/probeglass/internal/store"
	"github.com/j000k3r/probeglass/internal/totp"
)

// recoveryCodeCount is how many one-time codes are issued when the second factor
// is enrolled or renewed. Eight is the number every other product uses, which
// means it is the number people expect, and it is enough that losing one to a
// mislaid printout is not a crisis.
const recoveryCodeCount = 8

// ---------------------------------------------------------------------------
// the signed-in user's own second factor
//
// These routes belong to whoever is signed in and act only on their own account.
// They sit at viewer level in the router for exactly that reason: a viewer may not
// change anything about the installation, but their own credentials are theirs.
// ---------------------------------------------------------------------------

// selfUser returns the signed-in account, or writes the refusal and reports false.
func (s *Server) selfUser(w http.ResponseWriter, r *http.Request) (principal, bool) {
	p, ok := principalOf(r)
	if !ok || p.User == nil {
		writeError(w, http.StatusBadRequest,
			"the API key has no account of its own")
		return principal{}, false
	}
	return p, true
}

// handleTOTPStart answers POST /api/me/totp/start.
//
// It stores the new secret straight away, but leaves totp_enabled false. That is
// what makes an enrolment interruptible: the secret exists, nothing is required
// yet, and a second start simply replaces it. Only a code computed from it can
// turn the factor on.
func (s *Server) handleTOTPStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	if p.User.TOTPEnabled {
		writeError(w, http.StatusConflict,
			"two-factor authentication is already switched on for this account")
		return
	}

	secret, err := totp.GenerateSecret()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not generate a secret")
		return
	}
	// Any half-finished enrolment is discarded: there is only ever one secret per
	// account, so a stale tab cannot leave a second one lying around that would
	// still be accepted later.
	if err := s.store.SetUserTOTP(r.Context(), p.User.ID, secret, false, 0); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not store the secret")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
		"uri":    totp.URI(p.User.Username, secret),
	})
}

// handleTOTPQR answers GET /api/me/totp/qr with the enrolment QR code as a PNG.
//
// The image is served rather than drawn in the browser because the browser has no
// QR encoder and there is no CDN to borrow one from. It is same-origin, so the
// content policy needs no exception. Nothing is cached: the image contains the
// shared secret, and a secret in a browser cache outlives the tab.
func (s *Server) handleTOTPQR(w http.ResponseWriter, r *http.Request) {
	p, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	if p.User.TOTPSecret == "" || p.User.TOTPEnabled {
		writeError(w, http.StatusConflict, "there is no enrolment in progress")
		return
	}
	code, err := qr.Encode(totp.URI(p.User.Username, p.User.TOTPSecret), qr.M)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not encode the QR image")
		return
	}
	// Eight image pixels per module: the symbol comes out a few hundred pixels
	// across, which is sharper than anything the dashboard displays it at and still
	// a couple of kilobytes. Scale zero would produce a zero-sized image, so this
	// is required, not cosmetic.
	code.Scale = 8

	png := code.PNG()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(png); err != nil {
		s.log.Debug("write qr image", "error", err)
	}
}

// handleTOTPConfirm answers POST /api/me/totp/confirm. It is the step that turns
// the factor on, and the only time the recovery codes are ever shown.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	p, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if p.User.TOTPEnabled {
		writeError(w, http.StatusConflict,
			"two-factor authentication is already switched on for this account")
		return
	}
	if p.User.TOTPSecret == "" {
		writeError(w, http.StatusConflict, "there is no enrolment in progress")
		return
	}

	step, ok := totp.Verify(p.User.TOTPSecret, in.Code, time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest,
			"that code does not match; check the clock on the device and try the next one")
		return
	}

	ctx, cancel := commitContext(r)
	defer cancel()
	if err := s.store.SetUserTOTP(ctx, p.User.ID, p.User.TOTPSecret, true, step); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not switch on the second factor")
		return
	}
	codes, err := s.storeRecoveryCodes(ctx, p.User.ID)
	if err != nil {
		// The factor is on; the codes are the fallback for losing the device, so
		// failing to create them is worth saying plainly rather than reporting a
		// successful enrolment that has no way back.
		s.fail(w, err, http.StatusInternalServerError,
			"the second factor is on, but the recovery codes could not be created; "+
				"request new ones from the same panel")
		return
	}
	s.log.Info("two-factor authentication switched on", "username", p.User.Username)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// handleTOTPRecovery answers POST /api/me/totp/recovery: a fresh set of one-time
// codes, which retires every code that has not been used yet.
func (s *Server) handleTOTPRecovery(w http.ResponseWriter, r *http.Request) {
	p, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	if !p.User.TOTPEnabled {
		writeError(w, http.StatusConflict,
			"two-factor authentication is not switched on for this account")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	// Re-asking for the password is the point: these codes are a way around the
	// second factor, so a borrowed browser that is merely signed in must not be
	// able to mint a set of them.
	if bcrypt.CompareHashAndPassword([]byte(p.User.PasswordHash), []byte(in.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	ctx, cancel := commitContext(r)
	defer cancel()
	codes, err := s.storeRecoveryCodes(ctx, p.User.ID)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not create the recovery codes")
		return
	}
	s.log.Info("recovery codes renewed", "username", p.User.Username)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// handleTOTPDisable answers POST /api/me/totp/disable. It takes the password, for
// the same reason as above, and drops the secret and every recovery code with it.
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	p, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(p.User.PasswordHash), []byte(in.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	ctx, cancel := commitContext(r)
	defer cancel()
	if err := s.store.SetUserTOTP(ctx, p.User.ID, "", false, 0); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not switch off the second factor")
		return
	}
	if err := s.store.DeleteRecoveryCodes(ctx, p.User.ID); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not remove the recovery codes")
		return
	}
	s.log.Info("two-factor authentication switched off", "username", p.User.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// storeRecoveryCodes generates a fresh set, stores the hashes and returns the codes
// themselves — which is the only moment anything holds them in the clear.
func (s *Server) storeRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes, err := store.NewRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceRecoveryCodes(ctx, userID, codes); err != nil {
		return nil, err
	}
	return codes, nil
}
