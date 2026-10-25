package panel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// jsonList is a small list of records kept in a JSON file, for settings that
// change rarely (API tokens, webhooks). An empty path keeps it in memory.
type jsonList[T any] struct {
	path  string
	mu    sync.Mutex
	items []T
}

func openJSONList[T any](dir, name string) (*jsonList[T], error) {
	l := &jsonList[T]{}
	if dir == "" {
		return l, nil
	}
	l.path = filepath.Join(dir, name)
	data, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	return l, json.Unmarshal(data, &l.items)
}

func (l *jsonList[T]) all() []T {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]T{}, l.items...) // never nil, so the API returns [] rather than null
}

// change applies fn to the list under the lock and saves the result.
func (l *jsonList[T]) change(fn func([]T) []T) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	items := fn(append([]T(nil), l.items...))
	if l.path != "" {
		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return err
		}
		tmp := l.path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, l.path); err != nil {
			return err
		}
	}
	l.items = items
	return nil
}
