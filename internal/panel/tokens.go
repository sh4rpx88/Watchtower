package panel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Access levels, lowest first. Users get theirs from their role, API tokens
// from their scope.
type level int

const (
	levelRead    level = iota // stats, server list, templates, activity
	levelControl              // plus start, stop, restart, install, commands, console
	levelManage               // plus settings, files, backups
	levelAdmin                // everything: create and delete servers, users, tokens, webhooks
)

// APIToken lets a bot or script use the API without the admin password.
// Only a hash of the secret is stored; the secret is shown once at creation.
type APIToken struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scope     string     `json:"scope"` // "read" or "control"
	Prefix    string     `json:"prefix"`
	Hash      string     `json:"hash,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsed,omitempty"`
}

const tokenPrefix = "wt_"

func hashToken(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// bearer is the request's session token or API token.
func bearer(r *http.Request) string {
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); token != "" {
		return token
	}
	return r.URL.Query().Get("access_token")
}

// authenticate works out who is making the request, or returns ok=false.
func (s *Server) authenticate(r *http.Request) (principal, bool) {
	token := bearer(r)
	if token == "" {
		return principal{}, false
	}
	if strings.HasPrefix(token, tokenPrefix) {
		return s.apiTokenPrincipal(token)
	}
	s.mu.Lock()
	sess, ok := s.sessions[token]
	if ok && time.Now().After(sess.expiry) {
		delete(s.sessions, token)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return principal{}, false
	}
	// Look the user up each time, so role changes apply at once.
	u, ok := s.userByID(sess.userID)
	if !ok {
		return principal{}, false
	}
	return principal{Name: u.Username, Level: roleLevels[u.Role], UserID: u.ID, Role: u.Role}, true
}

func (s *Server) apiTokenPrincipal(secret string) (principal, bool) {
	hash := hashToken(secret)
	var found *APIToken
	for _, t := range s.tokens.all() {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(hash)) == 1 {
			found = &t
			break
		}
	}
	if found == nil {
		return principal{}, false
	}
	// Record use at most once a minute to keep file writes rare.
	if found.LastUsed == nil || time.Since(*found.LastUsed) > time.Minute {
		now := time.Now().UTC()
		s.tokens.change(func(ts []APIToken) []APIToken {
			for i := range ts {
				if ts[i].ID == found.ID {
					ts[i].LastUsed = &now
				}
			}
			return ts
		})
	}
	p := principal{Name: "token " + found.Name, Level: levelRead, Role: "token"}
	if found.Scope == "control" {
		p.Level = levelControl
	}
	return p, true
}

// auth wraps a handler so it needs at least the given access level.
func (s *Server) auth(need level, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if p.Level < need {
			msg := "your role isn't allowed to do that"
			if p.UserID == "" {
				msg = "this API token isn't allowed to do that"
			}
			writeError(w, http.StatusForbidden, msg)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

func publicTokens(ts []APIToken) []APIToken {
	for i := range ts {
		ts[i].Hash = ""
	}
	return ts
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, publicTokens(s.tokens.all()))
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || !slices.Contains([]string{"read", "control"}, body.Scope) {
		writeError(w, http.StatusBadRequest, "a name and a scope of read or control are required")
		return
	}
	secret := tokenPrefix + randomToken()[:40]
	t := APIToken{ID: randomToken()[:16], Name: body.Name, Scope: body.Scope, Prefix: secret[:10],
		Hash: hashToken(secret), CreatedAt: time.Now().UTC()}
	if err := s.tokens.change(func(ts []APIToken) []APIToken { return append(ts, t) }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.Hash = ""
	noteFor(r).text = t.Name
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("tokenId")
	err := s.tokens.change(func(ts []APIToken) []APIToken {
		return slices.DeleteFunc(ts, func(t APIToken) bool {
			if t.ID == id {
				noteFor(r).text = t.Name
			}
			return t.ID == id
		})
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
