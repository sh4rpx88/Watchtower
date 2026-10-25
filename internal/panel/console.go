package panel

import (
	"sync"
)

const consoleBacklog = 500

// consoles keeps recent output per instance and fans new lines out to viewers.
type consoles struct {
	mu   sync.Mutex
	logs map[string][]string
	subs map[string]map[chan string]struct{}
}

func newConsoles() *consoles {
	return &consoles{logs: map[string][]string{}, subs: map[string]map[chan string]struct{}{}}
}

func (c *consoles) append(id, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	log := append(c.logs[id], line)
	if len(log) > consoleBacklog {
		log = log[len(log)-consoleBacklog:]
	}
	c.logs[id] = log
	for ch := range c.subs[id] {
		select {
		case ch <- line:
		default: // a slow viewer misses lines rather than blocking the agent
		}
	}
}

// subscribe returns the backlog and a channel of new lines. Call the returned
// func to unsubscribe.
func (c *consoles) subscribe(id string) ([]string, chan string, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan string, 256)
	if c.subs[id] == nil {
		c.subs[id] = map[chan string]struct{}{}
	}
	c.subs[id][ch] = struct{}{}
	backlog := append([]string(nil), c.logs[id]...)
	return backlog, ch, func() {
		c.mu.Lock()
		delete(c.subs[id], ch)
		c.mu.Unlock()
	}
}

func (c *consoles) forget(id string) {
	c.mu.Lock()
	delete(c.logs, id)
	c.mu.Unlock()
}
