// Package protocol defines the messages the panel and agents exchange over WebSocket.
package protocol

import (
	"encoding/json"

	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// Message types.
const (
	TypeHello     = "hello"     // agent -> panel, first message after connecting
	TypeWelcome   = "welcome"   // panel -> agent, reply to hello
	TypeHeartbeat = "heartbeat" // agent -> panel, periodic host stats

	// Requests from the panel. Each carries an ID and gets a TypeResponse with the same ID.
	TypeInstall  = "instance.install" // payload: InstanceSpec
	TypeStart    = "instance.start"   // payload: InstanceSpec
	TypeStop     = "instance.stop"    // payload: InstanceRef
	TypeCommand  = "instance.command" // payload: Command
	TypeDelete   = "instance.delete"  // payload: InstanceRef
	TypeResponse = "response"         // agent -> panel, payload: Response

	// Events from the agent.
	TypeStatus  = "instance.status" // payload: Status
	TypeConsole = "console.line"    // payload: ConsoleLine
)

// Instance states.
const (
	StateNotInstalled = "not_installed"
	StateInstalling   = "installing"
	StateStopped      = "stopped"
	StateStarting     = "starting"
	StateRunning      = "running"
	StateStopping     = "stopping"
	StateError        = "error"
)

// Envelope wraps every message on the wire.
type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"` // correlates requests and replies
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Hello is sent by an agent when it connects.
type Hello struct {
	AgentName string `json:"agentName"`
	Version   string `json:"version"`
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Welcome is the panel's reply to Hello.
type Welcome struct {
	PanelVersion string `json:"panelVersion"`
}

// Heartbeat carries basic host stats.
type Heartbeat struct {
	UptimeSeconds int64 `json:"uptimeSeconds"`
	NumCPU        int   `json:"numCpu"`
}

// InstanceSpec is everything an agent needs to install or start an instance.
// The panel sends the template along, so agents keep no game definitions.
type InstanceSpec struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Template templates.Template `json:"template"`
	Settings map[string]string  `json:"settings"`
	Ports    map[string]int     `json:"ports"` // template port name -> host port
}

type InstanceRef struct {
	ID string `json:"id"`
}

type Command struct {
	ID   string `json:"id"`
	Line string `json:"line"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type Status struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type ConsoleLine struct {
	ID   string `json:"id"`
	Line string `json:"line"`
}

// New builds an envelope with a JSON-encoded payload.
func New(msgType string, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: msgType, Payload: raw}, nil
}
