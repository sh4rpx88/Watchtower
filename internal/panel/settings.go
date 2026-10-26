package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PanelSettings are panel-wide options admins set in the UI.
type PanelSettings struct {
	// PublicAddress is the host name or IP players outside your network use,
	// shown with each server's port. LANAddress is the same for your home
	// network.
	PublicAddress string `json:"publicAddress"`
	LANAddress    string `json:"lanAddress"`
}

func (s *Server) panelSettings() PanelSettings {
	if list := s.settings.all(); len(list) > 0 {
		return list[0]
	}
	return PanelSettings{}
}

// timeZone describes the zone schedules run in, e.g. "America/Chicago (CDT, UTC-05:00)".
func timeZone() string {
	name, _ := time.Now().Zone()
	offset := time.Now().Format("-07:00")
	if loc := time.Local.String(); loc != "Local" {
		return fmt.Sprintf("%s (%s, UTC%s)", loc, name, offset)
	}
	return fmt.Sprintf("%s (UTC%s)", name, offset)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.panelSettings(), "timeZone": timeZone()})
}

// cleanAddress accepts a host name or IP with nothing else in it.
func cleanAddress(v string) (string, error) {
	v = strings.TrimSpace(v)
	if strings.ContainsAny(v, " /\\\"'<>\r\n\t") || len(v) > 253 {
		return "", fmt.Errorf("%q isn't a host name or IP address", v)
	}
	return v, nil
}

func (s *Server) handleUpdateSettingsPanel(w http.ResponseWriter, r *http.Request) {
	var body PanelSettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var err error
	if body.PublicAddress, err = cleanAddress(body.PublicAddress); err == nil {
		body.LANAddress, err = cleanAddress(body.LANAddress)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.settings.change(func([]PanelSettings) []PanelSettings { return []PanelSettings{body} }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleUpdateInstance renames a server or sets the address players use.
// Fields left out of the body stay as they are.
func (s *Server) handleUpdateInstance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name           *string `json:"name"`
		ConnectAddress *string `json:"connectAddress"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != nil {
		if *body.Name = strings.TrimSpace(*body.Name); *body.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
	}
	if body.ConnectAddress != nil {
		// Allow host:port here, since tunnels give their own port.
		v := strings.TrimSpace(*body.ConnectAddress)
		if strings.ContainsAny(v, " /\\\"'<>\r\n\t") || len(v) > 260 {
			writeError(w, http.StatusBadRequest, "the address should look like play.example.com or play.example.com:25565")
			return
		}
		body.ConnectAddress = &v
	}
	found, err := s.store.update(r.PathValue("id"), func(in *Instance) {
		if body.Name != nil {
			in.Name = *body.Name
		}
		if body.ConnectAddress != nil {
			in.ConnectAddress = *body.ConnectAddress
		}
	})
	switch {
	case !found:
		writeError(w, http.StatusNotFound, "no such instance")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		in, _ := s.store.get(r.PathValue("id"))
		writeJSON(w, http.StatusOK, s.view(in))
	}
}
