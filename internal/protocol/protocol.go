// Package protocol defines the messages the panel and agents exchange over WebSocket.
package protocol

import "encoding/json"

// Message types.
const (
	TypeHello     = "hello"     // agent -> panel, first message after connecting
	TypeWelcome   = "welcome"   // panel -> agent, reply to hello
	TypeHeartbeat = "heartbeat" // agent -> panel, periodic host stats
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

// New builds an envelope with a JSON-encoded payload.
func New(msgType string, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: msgType, Payload: raw}, nil
}
