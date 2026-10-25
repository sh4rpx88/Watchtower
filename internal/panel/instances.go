package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
	"github.com/sh4rpx88/Watchtower/internal/templates"
)

// instanceView is an instance plus whether its agent is connected.
type instanceView struct {
	Instance
	AgentOnline bool `json:"agentOnline"`
}

func (s *Server) view(in Instance) instanceView {
	if tpl, ok := s.template(in.TemplateID); ok {
		in.Settings = tpl.Settings(in.Settings)
	}
	return instanceView{Instance: in, AgentOnline: s.agent(in.Agent) != nil}
}

func (s *Server) template(id string) (templates.Template, bool) {
	for _, t := range s.cfg.Templates {
		if t.ID == id {
			return t, true
		}
	}
	return templates.Template{}, false
}

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	list := s.store.list()
	out := make([]instanceView, 0, len(list))
	for _, in := range list {
		out = append(out, s.view(in))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetInstance(w http.ResponseWriter, r *http.Request) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	writeJSON(w, http.StatusOK, s.view(in))
}

func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string            `json:"name"`
		TemplateID string            `json:"templateId"`
		Agent      string            `json:"agent"`
		Settings   map[string]string `json:"settings"`
		Ports      map[string]int    `json:"ports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	tpl, ok := s.template(body.TemplateID)
	switch {
	case body.Name == "":
		writeError(w, http.StatusBadRequest, "name is required")
		return
	case !ok:
		writeError(w, http.StatusBadRequest, "unknown game")
		return
	case body.Agent == "":
		writeError(w, http.StatusBadRequest, "pick a machine")
		return
	}
	settings := tpl.Settings(body.Settings)
	if err := tpl.Validate(settings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ports := map[string]int{}
	for _, p := range tpl.Ports {
		ports[p.Name] = p.Port
		if h, ok := body.Ports[p.Name]; ok {
			if h < 1 || h > 65535 {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("port %s is out of range", p.Name))
				return
			}
			ports[p.Name] = h
		}
	}

	in := &Instance{
		ID: randomToken()[:16], Name: body.Name, TemplateID: tpl.ID, Agent: body.Agent,
		Settings: settings, Ports: ports, State: protocol.StateNotInstalled, CreatedAt: time.Now().UTC(),
	}
	err := s.store.add(in, func(existing []*Instance) error {
		return checkPorts(s, in, tpl, existing)
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s.view(*in))
}

// checkPorts rejects a new instance whose host ports clash with another
// instance on the same machine.
func checkPorts(s *Server, in *Instance, tpl templates.Template, existing []*Instance) error {
	used := map[string]string{} // "25565/tcp" -> instance name
	for _, e := range existing {
		if e.Agent != in.Agent {
			continue
		}
		et, ok := s.template(e.TemplateID)
		if !ok {
			continue
		}
		for _, p := range et.Ports {
			used[fmt.Sprintf("%d/%s", e.Ports[p.Name], p.Protocol)] = e.Name
		}
	}
	for _, p := range tpl.Ports {
		key := fmt.Sprintf("%d/%s", in.Ports[p.Name], p.Protocol)
		if other, clash := used[key]; clash {
			return fmt.Errorf("port %s is already used by %q; pick another %s port", key, other, p.Name)
		}
	}
	return nil
}

func (s *Server) spec(in Instance) (protocol.InstanceSpec, error) {
	tpl, ok := s.template(in.TemplateID)
	if !ok {
		return protocol.InstanceSpec{}, fmt.Errorf("game template %q is no longer available", in.TemplateID)
	}
	// Fill in defaults for fields added to the template after the instance was created.
	return protocol.InstanceSpec{ID: in.ID, Name: in.Name, Template: tpl, Settings: tpl.Settings(in.Settings), Ports: in.Ports}, nil
}

// handleAction runs install, start, stop, or command on an instance's agent.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	a := s.agent(in.Agent)
	if a == nil {
		writeError(w, http.StatusServiceUnavailable, errAgentOffline.Error())
		return
	}
	var err error
	switch r.PathValue("action") {
	case "install", "start":
		var spec protocol.InstanceSpec
		if spec, err = s.spec(in); err == nil {
			msgType := protocol.TypeInstall
			if r.PathValue("action") == "start" {
				msgType = protocol.TypeStart
			}
			err = a.request(r.Context(), msgType, spec)
		}
	case "stop":
		err = a.request(r.Context(), protocol.TypeStop, protocol.InstanceRef{ID: in.ID})
	case "command":
		var body struct {
			Line string `json:"line"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || strings.TrimSpace(body.Line) == "" {
			writeError(w, http.StatusBadRequest, "command is required")
			return
		}
		s.consoles.append(in.ID, "> "+body.Line)
		err = a.request(r.Context(), protocol.TypeCommand, protocol.Command{ID: in.ID, Line: body.Line})
	default:
		writeError(w, http.StatusNotFound, "unknown action")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUpdateSettings replaces an instance's settings. They apply the next
// time the server starts.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	tpl, ok := s.template(in.TemplateID)
	if !ok {
		writeError(w, http.StatusConflict, "this game's template is no longer available")
		return
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Start from the current settings so a partial update keeps the rest.
	merged := map[string]string{}
	for k, v := range in.Settings {
		merged[k] = v
	}
	for k, v := range body {
		merged[k] = v
	}
	settings := tpl.Settings(merged)
	if err := tpl.Validate(settings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.store.update(in.ID, func(in *Instance) { in.Settings = settings }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	in.Settings = settings
	writeJSON(w, http.StatusOK, s.view(in))
}

func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	in, ok := s.store.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	a := s.agent(in.Agent)
	if a == nil {
		writeError(w, http.StatusServiceUnavailable, "the machine must be online to delete its files")
		return
	}
	if err := a.request(r.Context(), protocol.TypeDelete, protocol.InstanceRef{ID: in.ID}); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.store.remove(in.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.consoles.forget(in.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsole streams an instance's console over a WebSocket: the backlog
// first, then each new line. Commands go through POST .../command.
func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.get(id); !ok {
		writeError(w, http.StatusNotFound, "no such instance")
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	backlog, lines, unsubscribe := s.consoles.subscribe(id)
	defer unsubscribe()
	for _, l := range backlog {
		if wsjson.Write(ctx, conn, l) != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-lines:
			if wsjson.Write(ctx, conn, l) != nil {
				return
			}
		}
	}
}
