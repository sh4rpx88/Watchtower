package panel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Instance is one game server, as the panel records it.
type Instance struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	TemplateID string            `json:"templateId"`
	Agent      string            `json:"agent"`
	Settings   map[string]string `json:"settings"`
	Ports      map[string]int    `json:"ports"`
	State      string            `json:"state"`
	Error      string            `json:"error,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	// ConnectAddress is what players type to join, when it isn't the panel's
	// public address and the game port (for example a playit.gg address).
	ConnectAddress string `json:"connectAddress,omitempty"`
}

// store keeps instances in a JSON file. It will move to SQLite once there is
// more to store (users, schedules, backups).
type store struct {
	path      string
	mu        sync.Mutex
	instances map[string]*Instance
}

func openStore(dir string) (*store, error) {
	s := &store{instances: map[string]*Instance{}}
	if dir == "" {
		return s, nil // in memory, for tests
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s.path = filepath.Join(dir, "instances.json")
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Instance
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, in := range list {
		s.instances[in.ID] = in
	}
	return s, nil
}

// saveLocked writes the file atomically. Callers hold s.mu.
func (s *store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	list := make([]*Instance, 0, len(s.instances))
	for _, in := range s.instances {
		list = append(list, in)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *store) list() []Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Instance, 0, len(s.instances))
	for _, in := range s.instances {
		out = append(out, *in)
	}
	return out
}

func (s *store) get(id string) (Instance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return Instance{}, false
	}
	return *in, true
}

// update applies fn to the instance and saves. It reports whether the instance exists.
func (s *store) update(id string, fn func(*Instance)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return false, nil
	}
	fn(in)
	return true, s.saveLocked()
}

// add inserts the instance if check passes, under the store lock so that
// checks such as port conflicts can't race.
func (s *store) add(in *Instance, check func(existing []*Instance) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := make([]*Instance, 0, len(s.instances))
	for _, e := range s.instances {
		existing = append(existing, e)
	}
	if err := check(existing); err != nil {
		return err
	}
	s.instances[in.ID] = in
	return s.saveLocked()
}

func (s *store) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.instances, id)
	return s.saveLocked()
}
