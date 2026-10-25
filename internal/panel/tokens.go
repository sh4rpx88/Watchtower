package panel

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Access levels, lowest first. A logged-in admin session has every level.
type level int

const (
	levelRead    level = iota // stats, server list, templates
	levelControl              // plus start, stop, restart, install, commands, console
	levelAdmin                // everything: create, delete, settings, files, backups, tokens, webhooks
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

// authLevel returns the access the request's token grants, or ok=false.
func (s *Server) authLevel(r *http.Request) (level, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.URL.Query().Get("access_token")
	}
	if token == "" {
		return 0, false
	}
	if strings.HasPrefix(token, tokenPrefix) {
		return s.apiTokenLevel(token)
	}
	s.mu.Lock()
	expiry, ok := s.sessions[token]
	s.mu.Unlock()
	if !ok || time.Now().After(expiry) {
		return 0, false
	}
	return levelAdmin, true
}

func (s *Server) apiTokenLevel(secret string) (level, bool) {
	hash := hashToken(secret)
	var found *APIToken
	for _, t := range s.tokens.all() {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(hash)) == 1 {
			found = &t
			break
		}
	}
	if found == nil {
		return 0, false
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
	if found.Scope == "control" {
		return levelControl, true
	}
	return levelRead, true
}

// auth wraps a handler so it needs at least the given access level.
func (s *Server) auth(need level, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		have, ok := s.authLevel(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if have < need {
			writeError(w, http.StatusForbidden, "this API token isn't allowed to do that")
			return
		}
		next(w, r)
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
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("tokenId")
	err := s.tokens.change(func(ts []APIToken) []APIToken {
		return slices.DeleteFunc(ts, func(t APIToken) bool { return t.ID == id })
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
