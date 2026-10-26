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
	TypeInstall = "instance.install" // payload: InstanceSpec
	TypeStart   = "instance.start"   // payload: InstanceSpec
	TypeStop    = "instance.stop"    // payload: InstanceRef
	TypeCommand = "instance.command" // payload: Command
	TypeDelete  = "instance.delete"  // payload: InstanceRef

	TypeFileList   = "file.list"   // payload: FileRequest; response data: []FileInfo
	TypeFileRead   = "file.read"   // payload: FileRequest; response data: FileContent
	TypeFileWrite  = "file.write"  // payload: FileContent
	TypeFileDelete = "file.delete" // payload: FileRequest
	TypeFileMkdir  = "file.mkdir"  // payload: FileRequest

	TypeBackupCreate  = "backup.create"  // payload: BackupCreate; runs in the background
	TypeBackupList    = "backup.list"    // payload: InstanceRef; response data: []BackupInfo
	TypeBackupRestore = "backup.restore" // payload: BackupRequest
	TypeBackupDelete  = "backup.delete"  // payload: BackupRequest
	TypeResponse      = "response"       // agent -> panel, payload: Response

	// Events from the agent.
	TypeStatus     = "instance.status"    // payload: Status
	TypeConsole    = "console.line"       // payload: ConsoleLine
	TypeResources  = "instance.resources" // payload: Resources
	TypeBackupDone = "backup.done"        // payload: BackupResult, when a backup finishes or fails
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
	MemoryTotal   int64 `json:"memoryTotal,omitempty"` // bytes; 0 when unknown
	DiskTotal     int64 `json:"diskTotal,omitempty"`   // bytes on the data directory's filesystem
	DiskFree      int64 `json:"diskFree,omitempty"`
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
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// FileRequest names a path inside an instance's folder.
type FileRequest struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type FileInfo struct {
	Name    string `json:"name"`
	Dir     bool   `json:"dir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"` // unix seconds
}

// FileContent carries a file's bytes (base64 in JSON).
type FileContent struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

type BackupInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"createdAt"` // unix seconds
}

// BackupCreate starts a backup. With Keep set, older backups beyond the newest
// Keep are deleted once it succeeds.
type BackupCreate struct {
	InstanceSpec
	Keep int `json:"keep,omitempty"`
}

type BackupResult struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Removed int    `json:"removed,omitempty"` // old backups deleted to keep the count
	Error   string `json:"error,omitempty"`
}

type BackupRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Status struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// Resources is a server's current CPU and memory use.
type Resources struct {
	ID          string  `json:"id"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes int64   `json:"memoryBytes"`
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
