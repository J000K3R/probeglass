package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// ---------------------------------------------------------------------------
// user administration
//
// Every route in this file is admin-only, which the router enforces. The rules
// that keep an installation from locking itself out — the last active
// administrator, and nobody disabling or deleting themselves — live in the store,
// so that they hold for a script calling the API directly just as much as for the
// dashboard.
// ---------------------------------------------------------------------------

// userListResponse carries the accounts. model.User already hides the password
// hash, the second-factor secret and the token epoch from its JSON form, so the
// only thing needed here is the shape the client expects.
type userListResponse struct {
	Users []model.User `json:"users"`
	Roles []model.Role `json:"roles"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not read the accounts")
		return
	}
	writeJSON(w, http.StatusOK, userListResponse{Users: users, Roles: model.Roles})
}

// userCreate is the body of POST /api/users.
type userCreate struct {
	Username string     `json:"username"`
	Password string     `json:"password"`
	Role     model.Role `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in userCreate
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := validUsername(in.Username); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if in.Role == "" {
		// A new account that can do nothing is the safe thing to hand out by
		// default; the admin promotes from there.
		in.Role = model.RoleViewer
	}
	if !in.Role.Valid() {
		writeError(w, http.StatusBadRequest, "unknown role %q", in.Role)
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}

	ctx, cancel := commitContext(r)
	defer cancel()
	u := &model.User{Username: in.Username, PasswordHash: hash, Role: in.Role}
	if err := s.store.CreateUser(ctx, u); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	s.log.Info("user account created", "username", u.Username, "role", u.Role)
	writeJSON(w, http.StatusCreated, u)
}

// userUpdate is the body of PUT /api/users/{id}. Both fields are optional, and a
// pointer is how "leave this alone" is told apart from "set it to false".
type userUpdate struct {
	Role     *model.Role `json:"role"`
	Disabled *bool       `json:"disabled"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r)
	actor := int64(0)
	if p.User != nil {
		actor = p.User.ID
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in userUpdate
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if in.Role == nil && in.Disabled == nil {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}

	ctx := r.Context()
	if in.Role != nil {
		if !in.Role.Valid() {
			writeError(w, http.StatusBadRequest, "unknown role %q", *in.Role)
			return
		}
		if err := s.store.SetUserRole(ctx, id, *in.Role); err != nil {
			s.writeUserError(w, err, "could not change the role")
			return
		}
	}
	if in.Disabled != nil {
		if err := s.store.SetUserDisabled(ctx, actor, id, *in.Disabled); err != nil {
			s.writeUserError(w, err, "could not change the account")
			return
		}
	}

	// Read back rather than echoing the request: the epoch may have moved, and the
	// client shows the row it is given.
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		s.fail(w, err, statusForStoreError(err), "could not read the account back")
		return
	}
	s.log.Info("user account changed", "username", u.Username, "role", u.Role, "disabled", u.Disabled)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	p, _ := principalOf(r)
	actor := int64(0)
	if p.User != nil {
		actor = p.User.ID
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// Read the name first, so the log line can say who was removed rather than
	// which number.
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err, statusForStoreError(err), "could not read the account")
		return
	}
	if err := s.store.DeleteUser(r.Context(), actor, id); err != nil {
		s.writeUserError(w, err, "could not remove the account")
		return
	}
	s.log.Info("user account removed", "username", u.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleResetUserPassword sets somebody else's password without knowing the old
// one. This is the "I forgot mine" path for the administrator, and it signs the
// account out everywhere, because that is what a reset is for.
func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in struct {
		New string `json:"new_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	hash, err := hashPassword(in.New)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	ctx, cancel := commitContext(r)
	defer cancel()
	if err := s.store.SetUserPassword(ctx, id, hash); err != nil {
		s.writeUserError(w, err, "could not store the password")
		return
	}
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		s.fail(w, err, statusForStoreError(err), "could not read the account back")
		return
	}
	s.log.Info("password reset by an administrator", "username", u.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeUserError maps the user-administration refusals onto status codes.
//
// The two self-lockout rules are conflicts rather than bad requests: the shape of
// the request was fine, and the answer is "not in this state of the world".
func (s *Server) writeUserError(w http.ResponseWriter, err error, msg string) {
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, http.StatusConflict,
			"this is the last active administrator; promote somebody else first")
	case errors.Is(err, store.ErrSelf):
		writeError(w, http.StatusConflict,
			"you cannot disable or delete the account you are signed in with")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, store.ErrUserExists):
		writeError(w, http.StatusConflict, "%s", err.Error())
	default:
		s.fail(w, err, http.StatusInternalServerError, "%s", msg)
	}
}
