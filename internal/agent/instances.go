package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

// stopTimeout is how long a server gets to shut down cleanly before it is killed.
const stopTimeout = 60 * time.Second

var validID = regexp.MustCompile(`^[a-z0-9]{8,64}$`)

// Manager tracks the instances on this machine. It outlives panel connections.
type Manager struct {
	dataDir string
	runtime Runtime

	mu        sync.Mutex
	instances map[string]*instance
	send      func(protocol.Envelope) // current panel connection, or nil
}

type instance struct {
	spec    protocol.InstanceSpec
	state   string
	err     string
	proc    Process
	stopped chan struct{} // closed when proc exits
	busy    string        // "backup" or "restore" while one runs
}

func NewManager(dataDir string, rt Runtime) *Manager {
	return &Manager{dataDir: dataDir, runtime: rt, instances: map[string]*instance{}}
}

// SetSend points events at a new panel connection (nil while disconnected) and
// replays every instance's state so the panel is up to date.
func (m *Manager) SetSend(send func(protocol.Envelope)) {
	m.mu.Lock()
	m.send = send
	var statuses []protocol.Status
	for id, in := range m.instances {
		statuses = append(statuses, protocol.Status{ID: id, State: in.state, Error: in.err})
	}
	m.mu.Unlock()
	for _, s := range statuses {
		m.emit(protocol.TypeStatus, s)
	}
}

func (m *Manager) emit(msgType string, payload any) {
	m.mu.Lock()
	send := m.send
	m.mu.Unlock()
	if send == nil {
		return
	}
	env, err := protocol.New(msgType, payload)
	if err == nil {
		send(env)
	}
}

func (m *Manager) console(id string) func(string) {
	return func(line string) { m.emit(protocol.TypeConsole, protocol.ConsoleLine{ID: id, Line: line}) }
}

// setState records and reports a state change. Callers must not hold m.mu.
func (m *Manager) setState(id, state, errMsg string) {
	m.mu.Lock()
	if in, ok := m.instances[id]; ok {
		in.state, in.err = state, errMsg
	}
	m.mu.Unlock()
	m.emit(protocol.TypeStatus, protocol.Status{ID: id, State: state, Error: errMsg})
}

func (m *Manager) dir(id string) string { return filepath.Join(m.dataDir, "instances", id) }

// get returns the instance, registering it from spec if this agent hasn't seen it.
func (m *Manager) get(spec protocol.InstanceSpec) (*instance, error) {
	if !validID.MatchString(spec.ID) {
		return nil, fmt.Errorf("invalid instance id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.instances[spec.ID]
	if !ok {
		in = &instance{state: protocol.StateNotInstalled}
		if _, err := os.Stat(m.dir(spec.ID)); err == nil {
			in.state = protocol.StateStopped
		}
		m.instances[spec.ID] = in
	}
	in.spec = spec
	return in, nil
}

func (m *Manager) Install(spec protocol.InstanceSpec) error {
	in, err := m.get(spec)
	if err != nil {
		return err
	}
	m.mu.Lock()
	busy := in.state == protocol.StateInstalling || in.proc != nil || in.busy != ""
	m.mu.Unlock()
	if busy {
		return errors.New("stop the server and wait for other jobs to finish before installing or updating")
	}
	m.setState(spec.ID, protocol.StateInstalling, "")
	go func() {
		out := m.console(spec.ID)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := m.runtime.Install(ctx, spec, m.dir(spec.ID), out); err != nil {
			out("Install failed: " + err.Error())
			m.setState(spec.ID, protocol.StateError, "install failed: "+err.Error())
			return
		}
		out("Install finished")
		m.setState(spec.ID, protocol.StateStopped, "")
	}()
	return nil
}

func (m *Manager) Start(spec protocol.InstanceSpec) error {
	in, err := m.get(spec)
	if err != nil {
		return err
	}
	m.mu.Lock()
	state, running, busy := in.state, in.proc != nil, in.busy
	m.mu.Unlock()
	switch {
	case running:
		return errors.New("already running")
	case busy == "restore":
		return errors.New("a restore is in progress")
	case state == protocol.StateInstalling:
		return errors.New("still installing")
	case state == protocol.StateNotInstalled:
		return errors.New("not installed yet")
	}
	m.setState(spec.ID, protocol.StateStarting, "")
	proc, err := m.runtime.Start(spec, m.dir(spec.ID), m.console(spec.ID))
	if err != nil {
		m.setState(spec.ID, protocol.StateError, "start failed: "+err.Error())
		return err
	}
	m.watch(spec.ID, in, proc)
	m.setState(spec.ID, protocol.StateRunning, "")
	return nil
}

// watch records proc as the instance's server and updates its state when it exits.
func (m *Manager) watch(id string, in *instance, proc Process) {
	stopped := make(chan struct{})
	m.mu.Lock()
	in.proc, in.stopped = proc, stopped
	m.mu.Unlock()
	go func() {
		err := proc.Wait()
		m.mu.Lock()
		wasStopping := in.state == protocol.StateStopping
		in.proc = nil
		m.mu.Unlock()
		close(stopped)
		if err != nil && !wasStopping {
			m.console(id)("Server exited: " + err.Error())
			m.setState(id, protocol.StateError, "server exited: "+err.Error())
			return
		}
		m.setState(id, protocol.StateStopped, "")
	}()
}

// Recover picks up servers that kept running while the agent was down.
func (m *Manager) Recover() error {
	procs, err := m.runtime.Running(m.console)
	if err != nil {
		return err
	}
	for id, proc := range procs {
		if !validID.MatchString(id) {
			continue
		}
		in := &instance{state: protocol.StateRunning}
		m.mu.Lock()
		m.instances[id] = in
		m.mu.Unlock()
		m.watch(id, in, proc)
		slog.Info("reattached to running server", "instance", id)
	}
	return nil
}

// Stop sends the template's stop command and kills the server if it hasn't
// exited after stopTimeout. It returns once the stop has begun.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	in, ok := m.instances[id]
	var proc Process
	var stopped chan struct{}
	var stopCmd string
	if ok {
		proc, stopped, stopCmd = in.proc, in.stopped, in.spec.Template.Stop
	}
	m.mu.Unlock()
	if proc == nil {
		return errors.New("not running")
	}
	m.setState(id, protocol.StateStopping, "")
	if stopCmd == "" || proc.WriteLine(stopCmd) != nil {
		go proc.Terminate()
		return nil
	}
	go func() {
		select {
		case <-stopped:
		case <-time.After(stopTimeout):
			slog.Warn("server did not stop in time, killing", "instance", id)
			m.console(id)("Server did not stop in time, killing it")
			proc.Kill()
		}
	}()
	return nil
}

func (m *Manager) Command(id, line string) error {
	m.mu.Lock()
	var proc Process
	if in, ok := m.instances[id]; ok {
		proc = in.proc
	}
	m.mu.Unlock()
	if proc == nil {
		return errors.New("not running")
	}
	return proc.WriteLine(line)
}

// Delete removes a stopped instance and all of its files.
func (m *Manager) Delete(id string) error {
	if !validID.MatchString(id) {
		return errors.New("invalid instance id")
	}
	m.mu.Lock()
	in, ok := m.instances[id]
	if ok && (in.proc != nil || in.state == protocol.StateInstalling || in.busy != "") {
		m.mu.Unlock()
		return errors.New("stop the server and wait for other jobs to finish before deleting it")
	}
	delete(m.instances, id)
	m.mu.Unlock()
	if err := os.RemoveAll(m.backupDir(id)); err != nil {
		return err
	}
	return os.RemoveAll(m.dir(id))
}

// reportResources sends CPU and memory for every running server.
func (m *Manager) reportResources() {
	m.mu.Lock()
	var ids []string
	for id, in := range m.instances {
		if in.proc != nil {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	res, err := m.runtime.Resources(ids)
	if err != nil {
		slog.Debug("reading resources", "err", err)
		return
	}
	for _, r := range res {
		m.emit(protocol.TypeResources, r)
	}
}
