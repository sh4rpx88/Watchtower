package panel

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// User is a person who logs in to the panel. Their role decides what they
// can do; see roleLevels.
type User struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	Role         string     `json:"role"`
	PasswordHash string     `json:"passwordHash,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLogin    *time.Time `json:"lastLogin,omitempty"`
}

// Roles, from least to most access.
const (
	roleViewer   = "viewer"   // sees servers, stats and activity
	roleOperator = "operator" // also runs servers: start, stop, commands, settings, files, backups
	roleAdmin    = "admin"    // also creates and deletes servers, and manages users, tokens and webhooks
)

var roleLevels = map[string]level{roleViewer: levelRead, roleOperator: levelManage, roleAdmin: levelAdmin}

func validRole(role string) bool {
	_, ok := roleLevels[role]
	return ok
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

const minPasswordLength = 8

// session is a logged-in user's browser.
type session struct {
	userID string
	expiry time.Time
}

const sessionLength = 24 * time.Hour

// principal is who is making a request: a user or an API token.
type principal struct {
	Name   string // username, or "token <name>" for API tokens
	Level  level
	UserID string // empty for API tokens
	Role   string // a user's role, or "token"
}

type principalKey struct{}

func principalOf(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

// Passwords are stored as PBKDF2-SHA256 with a random salt.
const passwordIterations = 600_000

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func checkPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err != nil || err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is checked against when a username doesn't exist, so a failed
// login takes as long either way.
var dummyHash, _ = hashPassword("not a real password")

// ensureAdmin creates the first admin from PANEL_ADMIN_PASSWORD when there are
// no users yet.
func (s *Server) ensureAdmin() error {
	if len(s.users.all()) > 0 {
		return nil
	}
	if s.cfg.AdminPassword == "" {
		return fmt.Errorf("no users yet, so an admin password is needed to create the first one")
	}
	hash, err := hashPassword(s.cfg.AdminPassword)
	if err != nil {
		return err
	}
	u := User{ID: randomToken()[:16], Username: "admin", Role: roleAdmin, PasswordHash: hash, CreatedAt: time.Now().UTC()}
	slog.Info("created user admin with the password from PANEL_ADMIN_PASSWORD")
	return s.users.change(func(us []User) []User { return append(us, u) })
}

func (s *Server) userByID(id string) (User, bool) {
	for _, u := range s.users.all() {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

func (s *Server) userByName(name string) (User, bool) {
	for _, u := range s.users.all() {
		if strings.EqualFold(u.Username, name) {
			return u, true
		}
	}
	return User{}, false
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Username == "" {
		body.Username = "admin" // logins from before there were users
	}
	u, found := s.userByName(body.Username)
	hash := dummyHash
	if found {
		hash = u.PasswordHash
	}
	if !checkPassword(hash, body.Password) || !found {
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	token := randomToken()
	s.mu.Lock()
	s.sessions[token] = session{userID: u.ID, expiry: time.Now().Add(sessionLength)}
	s.mu.Unlock()
	now := time.Now().UTC()
	s.users.change(func(us []User) []User {
		for i := range us {
			if us[i].ID == u.ID {
				us[i].LastLogin = &now
			}
		}
		return us
	})
	s.audit(principal{Name: u.Username}, nil, "logged in")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	delete(s.sessions, bearer(r))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// endSessions logs a user out everywhere, after a password change or removal.
func (s *Server) endSessions(userID, except string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.sessions {
		if sess.userID == userID && token != except {
			delete(s.sessions, token)
		}
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	writeJSON(w, http.StatusOK, map[string]string{"name": p.Name, "role": p.Role})
}

func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	if p.UserID == "" {
		writeError(w, http.StatusForbidden, "API tokens don't have passwords")
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, ok := s.userByID(p.UserID)
	if !ok || !checkPassword(u.PasswordHash, body.Current) {
		writeError(w, http.StatusForbidden, "current password is wrong")
		return
	}
	if err := s.setPassword(u.ID, body.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.endSessions(u.ID, bearer(r))
	s.audit(p, nil, "changed their password")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setPassword(userID, password string) error {
	if len(password) < minPasswordLength {
		return fmt.Errorf("passwords need at least %d characters", minPasswordLength)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return s.users.change(func(us []User) []User {
		for i := range us {
			if us[i].ID == userID {
				us[i].PasswordHash = hash
			}
		}
		return us
	})
}

func publicUsers(us []User) []User {
	for i := range us {
		us[i].PasswordHash = ""
	}
	return us
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, publicUsers(s.users.all()))
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	switch {
	case !usernamePattern.MatchString(body.Username):
		writeError(w, http.StatusBadRequest, "usernames are 1 to 32 letters, numbers, dots, dashes or underscores")
		return
	case !validRole(body.Role):
		writeError(w, http.StatusBadRequest, "role must be viewer, operator or admin")
		return
	case len(body.Password) < minPasswordLength:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("passwords need at least %d characters", minPasswordLength))
		return
	}
	if _, taken := s.userByName(body.Username); taken {
		writeError(w, http.StatusConflict, "that username is taken")
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := User{ID: randomToken()[:16], Username: body.Username, Role: body.Role, PasswordHash: hash, CreatedAt: time.Now().UTC()}
	if err := s.users.change(func(us []User) []User { return append(us, u) }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(principalOf(r), nil, fmt.Sprintf("added user %s as %s", u.Username, u.Role))
	u.PasswordHash = ""
	writeJSON(w, http.StatusCreated, u)
}

// admins counts users with the admin role, so the last one can't be removed.
func (s *Server) admins() int {
	n := 0
	for _, u := range s.users.all() {
		if u.Role == roleAdmin {
			n++
		}
	}
	return n
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(r.PathValue("userId"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	var body struct {
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var changes []string
	if body.Role != "" && body.Role != u.Role {
		if !validRole(body.Role) {
			writeError(w, http.StatusBadRequest, "role must be viewer, operator or admin")
			return
		}
		if u.Role == roleAdmin && s.admins() == 1 {
			writeError(w, http.StatusConflict, "this is the only admin; make someone else an admin first")
			return
		}
		err := s.users.change(func(us []User) []User {
			for i := range us {
				if us[i].ID == u.ID {
					us[i].Role = body.Role
				}
			}
			return us
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		changes = append(changes, "made "+u.Username+" "+body.Role)
	}
	if body.Password != "" {
		if err := s.setPassword(u.ID, body.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.endSessions(u.ID, bearer(r))
		changes = append(changes, "reset the password for "+u.Username)
	}
	for _, c := range changes {
		s.audit(principalOf(r), nil, c)
	}
	u, _ = s.userByID(u.ID)
	writeJSON(w, http.StatusOK, publicUsers([]User{u})[0])
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userByID(r.PathValue("userId"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	if u.ID == principalOf(r).UserID {
		writeError(w, http.StatusConflict, "you can't remove yourself")
		return
	}
	if u.Role == roleAdmin && s.admins() == 1 {
		writeError(w, http.StatusConflict, "this is the only admin")
		return
	}
	err := s.users.change(func(us []User) []User {
		return slices.DeleteFunc(us, func(x User) bool { return x.ID == u.ID })
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.endSessions(u.ID, "")
	s.audit(principalOf(r), nil, "removed user "+u.Username)
	w.WriteHeader(http.StatusNoContent)
}
