package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

// maxUpload matches the agent's file size cap.
const maxUpload = 2 << 20

// instanceAgent looks up the instance in the path and its connected agent,
// writing an error response and returning ok=false if either is missing.
func (s *Server) instanceAgent(w http.ResponseWriter, r *http.Request) (Instance, *agentConn, bool) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return Instance{}, nil, false
	}
	a := s.agent(in.Agent)
	if a == nil {
		writeError(w, http.StatusServiceUnavailable, errAgentOffline.Error())
		return Instance{}, nil, false
	}
	return in, a, true
}

// relay sends a request and writes the agent's data (or {"ok":true}) as the response.
func relay(w http.ResponseWriter, r *http.Request, a *agentConn, msgType string, payload any) {
	data, err := a.call(r.Context(), msgType, payload)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func fileReq(in Instance, r *http.Request) protocol.FileRequest {
	return protocol.FileRequest{ID: in.ID, Path: r.URL.Query().Get("path")}
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	if in, a, ok := s.instanceAgent(w, r); ok {
		relay(w, r, a, protocol.TypeFileList, fileReq(in, r))
	}
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	if in, a, ok := s.instanceAgent(w, r); ok {
		relay(w, r, a, protocol.TypeFileDelete, fileReq(in, r))
	}
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if in, a, ok := s.instanceAgent(w, r); ok {
		relay(w, r, a, protocol.TypeFileMkdir, fileReq(in, r))
	}
}

// handleReadFile returns a file's raw bytes. With ?download=1 the browser saves it.
func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	in, a, ok := s.instanceAgent(w, r)
	if !ok {
		return
	}
	data, err := a.call(r.Context(), protocol.TypeFileRead, fileReq(in, r))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var fc protocol.FileContent
	if err := json.Unmarshal(data, &fc); err != nil {
		writeError(w, http.StatusBadGateway, "bad reply from agent")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(fc.Path)))
	}
	w.Write(fc.Content)
}

// handleWriteFile saves the request body as the file, creating it if needed.
func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	in, a, ok := s.instanceAgent(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUpload))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "file is too big (over 2 MB)")
		return
	}
	relay(w, r, a, protocol.TypeFileWrite, protocol.FileContent{ID: in.ID, Path: r.URL.Query().Get("path"), Content: body})
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if in, a, ok := s.instanceAgent(w, r); ok {
		relay(w, r, a, protocol.TypeBackupList, protocol.InstanceRef{ID: in.ID})
	}
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	in, a, ok := s.instanceAgent(w, r)
	if !ok {
		return
	}
	spec, err := s.spec(in)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	relay(w, r, a, protocol.TypeBackupCreate, protocol.BackupCreate{InstanceSpec: spec})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	if in, a, ok := s.instanceAgent(w, r); ok {
		relay(w, r, a, protocol.TypeBackupRestore, protocol.BackupRequest{ID: in.ID, Name: r.PathValue("name")})
	}
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if in, a, ok := s.instanceAgent(w, r); ok {
		relay(w, r, a, protocol.TypeBackupDelete, protocol.BackupRequest{ID: in.ID, Name: r.PathValue("name")})
	}
}

// handleGameConfig returns the game's own settings files, read option by
// option, for templates that list them.
func (s *Server) handleGameConfig(w http.ResponseWriter, r *http.Request) {
	in, a, ok := s.instanceAgent(w, r)
	if !ok {
		return
	}
	spec, err := s.spec(in)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	relay(w, r, a, protocol.TypeGameConfigRead, protocol.GameConfigRequest{InstanceSpec: spec})
}

// handleUpdateGameConfig changes settings in one game settings file:
// {"values": {"PVP": "false", "ZombieLore.Speed": "1"}}.
func (s *Server) handleUpdateGameConfig(w http.ResponseWriter, r *http.Request) {
	in, a, ok := s.instanceAgent(w, r)
	if !ok {
		return
	}
	spec, err := s.spec(in)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	index, err := strconv.Atoi(r.PathValue("file"))
	if err != nil || index < 0 || index >= len(spec.Template.GameConfig) {
		writeError(w, http.StatusNotFound, "no such settings file")
		return
	}
	var body struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Values) == 0 {
		writeError(w, http.StatusBadRequest, "send the settings to change as {\"values\": {...}}")
		return
	}
	noteFor(r).text = fmt.Sprintf("%s, %d setting(s)", spec.Template.GameConfig[index].Label, len(body.Values))
	relay(w, r, a, protocol.TypeGameConfigWrite, protocol.GameConfigRequest{InstanceSpec: spec, File: index, Values: body.Values})
}
