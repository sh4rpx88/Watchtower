package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/discord"
	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

// The bot's messages follow the panel's look: dark embeds edged in the
// state's color, the Watchtower banner and raven icon, game art as
// thumbnails, and meter bars for players, CPU and memory.

const (
	colorCrimson = 0x8f1d24
	colorRunning = 0x3fb950
	colorBusy    = 0xd29922
	colorStopped = 0x4b4547
	colorError   = 0xd8333c
)

func stateLook(in Instance, st Stats) (emoji, label string, color int) {
	switch in.State {
	case protocol.StateRunning:
		if !st.Ready && st.UptimeSeconds < 600 {
			return "🟡", "Starting", colorBusy
		}
		return "🟢", "Running", colorRunning
	case protocol.StateStarting:
		return "🟡", "Starting", colorBusy
	case protocol.StateStopping:
		return "🟠", "Stopping", colorBusy
	case protocol.StateInstalling:
		return "🔵", "Installing", colorBusy
	case protocol.StateError:
		return "🔴", "Error", colorError
	case protocol.StateNotInstalled:
		return "⚪", "Not installed", colorStopped
	default:
		return "⚫", "Stopped", colorStopped
	}
}

// bar draws a ten-cell meter.
func bar(frac float64) string {
	n := int(frac*10 + 0.5)
	n = max(0, min(10, n))
	return strings.Repeat("▰", n) + strings.Repeat("▱", 10-n)
}

func shortDuration(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", d/(24*time.Hour), d%(24*time.Hour)/time.Hour)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", d/time.Hour, d%time.Hour/time.Minute)
	default:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
}

func gigabytes(b int64) string { return fmt.Sprintf("%.1f GB", float64(b)/(1<<30)) }

var memorySetting = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*([mg])b?$`)

// memoryLimit reads a memory setting like "6G" or "8g".
func memoryLimit(v string) int64 {
	m := memorySetting.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	if strings.EqualFold(m[2], "g") {
		return int64(n * (1 << 30))
	}
	return int64(n * (1 << 20))
}

// joinAddress is what players type to connect, as the panel shows it.
func (s *Server) joinAddress(in Instance) string {
	if in.ConnectAddress != "" {
		return in.ConnectAddress
	}
	ps := s.panelSettings()
	if ps.PublicAddress == "" {
		return ""
	}
	port, ok := in.Ports["game"]
	if !ok {
		for _, p := range in.Ports {
			port = p
			break
		}
	}
	if port == 0 {
		return ps.PublicAddress
	}
	return fmt.Sprintf("%s:%d", ps.PublicAddress, port)
}

// art returns an image from the web UI's art folder as an upload, or nil.
func (s *Server) art(name string) *discord.File {
	if s.cfg.WebDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.WebDir, name))
	if err != nil {
		return nil
	}
	return &discord.File{Name: filepath.Base(name), Data: data}
}

// files collects uploads for a message and dedupes them by name.
type files struct{ list []discord.File }

func (f *files) ref(file *discord.File) string {
	if file == nil {
		return ""
	}
	for _, have := range f.list {
		if have.Name == file.Name {
			return "attachment://" + file.Name
		}
	}
	f.list = append(f.list, *file)
	return "attachment://" + file.Name
}

func fileNames(list []discord.File) string {
	var n []string
	for _, file := range list {
		n = append(n, file.Name)
	}
	return strings.Join(n, ",")
}

func image(url string) *discord.EmbedImage {
	if url == "" {
		return nil
	}
	return &discord.EmbedImage{URL: url}
}

func (s *Server) gameArt(f *files, templateID string) string {
	if art := s.art("art/" + templateID + ".jpg"); art != nil {
		return f.ref(art)
	}
	return f.ref(s.art("art/keep.jpg"))
}

func (s *Server) sortedInstances() []Instance {
	list := s.store.list()
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	return list
}

// serverEmbed is one server's card. With detail it adds ports, for the
// private panel, and with console the latest console lines too.
func (s *Server) serverEmbed(f *files, in Instance, detail, console bool) discord.Embed {
	st := s.stats(in)
	emoji, label, color := stateLook(in, st)
	machine := in.Agent
	if s.agent(in.Agent) == nil {
		machine += " (offline)"
	}
	status := emoji + " **" + label + "**"
	if st.UptimeSeconds > 0 {
		status += " · up " + shortDuration(st.UptimeSeconds)
	}
	if in.Error != "" && in.State == protocol.StateError {
		status += "\n" + truncate(in.Error, 200)
	}
	e := discord.Embed{
		Title:       in.Name,
		Description: fmt.Sprintf("%s on %s\n%s", st.Game, machine, status),
		Color:       color,
		Thumbnail:   image(s.gameArt(f, in.TemplateID)),
	}

	players := fmt.Sprint(st.PlayerCount)
	frac := 0.0
	if st.MaxPlayers > 0 {
		players += fmt.Sprintf(" / %d", st.MaxPlayers)
		frac = float64(st.PlayerCount) / float64(st.MaxPlayers)
	}
	live := in.State == protocol.StateRunning
	cpu, mem := "—", "—"
	cpuFrac, memFrac := 0.0, 0.0
	tpl, _ := s.template(in.TemplateID)
	limit := memoryLimit(tpl.Settings(in.Settings)["memory"])
	if live {
		cpu = fmt.Sprintf("%.0f%%", st.CPUPercent)
		cpuFrac = st.CPUPercent / 100
		mem = gigabytes(st.MemoryBytes)
		if limit > 0 {
			mem += " / " + gigabytes(limit)
			memFrac = float64(st.MemoryBytes) / float64(limit)
		}
	}
	e.Fields = []discord.EmbedField{
		{Name: "Players", Value: players + "\n" + bar(frac), Inline: true},
		{Name: "CPU", Value: cpu + "\n" + bar(min(cpuFrac, 1)), Inline: true},
		{Name: "Memory", Value: mem + "\n" + bar(memFrac), Inline: true},
	}
	if addr := s.joinAddress(in); addr != "" {
		e.Fields = append(e.Fields, discord.EmbedField{Name: "Connect", Value: "`" + addr + "`", Inline: true})
	}
	if len(st.Players) > 0 {
		sort.Strings(st.Players)
		e.Fields = append(e.Fields, discord.EmbedField{Name: "Online now", Value: truncate(strings.Join(st.Players, ", "), 1000), Inline: true})
	}
	if detail {
		var ports []string
		for name, port := range in.Ports {
			ports = append(ports, fmt.Sprintf("%d (%s)", port, name))
		}
		sort.Strings(ports)
		if len(ports) > 0 {
			e.Fields = append(e.Fields, discord.EmbedField{Name: "Ports", Value: strings.Join(ports, ", "), Inline: true})
		}
		if lines := s.consoles.tail(in.ID, 8); console && len(lines) > 0 {
			text := formatting.ReplaceAllString(strings.Join(lines, "\n"), "")
			if len(text) > 900 {
				text = "…" + text[len(text)-899:] // keep the newest lines
			}
			e.Fields = append(e.Fields, discord.EmbedField{Name: "Recent console", Value: "```\n" + strings.ReplaceAll(text, "```", "'''") + "\n```"})
		}
		e.Footer = &discord.EmbedFooter{Text: "Watchtower · container watchtower-" + in.ID, IconURL: f.ref(s.art("icons/icon-192.png"))}
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// boardMessage is the live status board: a header with the banner and a
// summary, a card per server, and a menu to open a server's controls.
func (s *Server) boardMessage() (discord.Message, []discord.File) {
	f := &files{}
	list := s.sortedInstances()
	running, players := 0, 0
	for _, in := range list {
		if in.State == protocol.StateRunning {
			running++
			players += s.stats(in).PlayerCount
		}
	}
	header := discord.Embed{
		Author:      &discord.EmbedAuthor{Name: "WATCHTOWER", IconURL: f.ref(s.art("icons/icon-192.png"))},
		Title:       "Server status",
		Description: fmt.Sprintf("**%d** of **%d** servers running · **%d** %s online", running, len(list), players, plural(players, "player", "players")),
		Color:       colorCrimson,
		Image:       image(f.ref(s.art("art/banner.jpg"))),
	}
	embeds := []discord.Embed{header}
	const maxCards = 9 // Discord allows 10 embeds per message
	for i, in := range list {
		if i == maxCards {
			header.Description += fmt.Sprintf("\n%d more servers aren't shown; use `/server` to open one.", len(list)-maxCards)
			embeds[0] = header
			break
		}
		embeds = append(embeds, s.serverEmbed(f, in, false, false))
	}
	last := &embeds[len(embeds)-1]
	last.Footer = &discord.EmbedFooter{Text: "Servers · Control · Monitor · Always online"}
	last.Timestamp = time.Now().UTC().Format(time.RFC3339)
	if len(list) == 0 {
		header.Description = "No servers yet. Add one in the Watchtower panel."
		embeds[0] = header
	}

	msg := discord.Message{Embeds: embeds}
	if picker := s.serverPicker(list); picker != nil {
		msg.Components = append(msg.Components, discord.Row(*picker))
	}
	msg.Components = append(msg.Components, discord.Row(
		discord.Component{Type: discord.ComponentButton, Style: discord.ButtonSecondary, Label: "Refresh", Emoji: &discord.Emoji{Name: "🔄"}, CustomID: "wt:board"},
	))
	return msg, f.list
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Server) serverPicker(list []Instance) *discord.Component {
	if len(list) == 0 {
		return nil
	}
	var opts []discord.SelectOption
	for i, in := range list {
		if i == 25 {
			break
		}
		st := s.stats(in)
		emoji, label, _ := stateLook(in, st)
		desc := st.Game + " · " + label
		if in.State == protocol.StateRunning && st.MaxPlayers > 0 {
			desc += fmt.Sprintf(" · %d/%d players", st.PlayerCount, st.MaxPlayers)
		}
		opts = append(opts, discord.SelectOption{Label: truncate(in.Name, 100), Value: in.ID, Description: truncate(desc, 100), Emoji: &discord.Emoji{Name: emoji}})
	}
	return &discord.Component{Type: discord.ComponentSelect, CustomID: "wt:pick", Placeholder: "Open a server's controls…", Options: opts}
}

// panelMessage is one server's private control panel. note, if set, reports
// what just happened. People who can control servers also see the console.
func (s *Server) panelMessage(in Instance, note string, console bool) (discord.Message, []discord.File) {
	f := &files{}
	e := s.serverEmbed(f, in, true, console)
	running := in.State == protocol.StateRunning || in.State == protocol.StateStarting
	stopped := in.State == protocol.StateStopped || in.State == protocol.StateError
	btn := func(style int, emoji, label, id string, disabled bool) discord.Component {
		return discord.Component{Type: discord.ComponentButton, Style: style, Emoji: &discord.Emoji{Name: emoji}, Label: label, CustomID: id + ":" + in.ID, Disabled: disabled}
	}
	msg := discord.Message{
		Content: note,
		Embeds:  []discord.Embed{e},
		Components: []discord.Component{discord.Row(
			btn(discord.ButtonSuccess, "▶️", "Start", "wt:act:start", !stopped),
			btn(discord.ButtonDanger, "⏹️", "Stop", "wt:act:stop", !running),
			btn(discord.ButtonPrimary, "🔁", "Restart", "wt:act:restart", in.State != protocol.StateRunning),
			btn(discord.ButtonSecondary, "📦", "Backup", "wt:act:backup", in.State == protocol.StateNotInstalled || in.State == protocol.StateInstalling),
			btn(discord.ButtonSecondary, "🔄", "Refresh", "wt:refresh", false),
		)},
	}
	return msg, f.list
}

// confirmMessage asks before stopping or restarting a server.
func (s *Server) confirmMessage(in Instance, action string) discord.Message {
	st := s.stats(in)
	who := "Nobody is online."
	if st.PlayerCount > 0 {
		who = fmt.Sprintf("**%d** %s online will be disconnected: %s.", st.PlayerCount, plural(st.PlayerCount, "player", "players"), truncate(strings.Join(st.Players, ", "), 500))
	}
	verb := map[string]string{"stop": "Stop", "restart": "Restart"}[action]
	e := discord.Embed{
		Title:       verb + " " + in.Name + "?",
		Description: who,
		Color:       colorBusy,
	}
	if action == "restart" {
		e.Description += "\nThe server comes back up on its own once it has stopped."
	}
	style := discord.ButtonDanger
	if action == "restart" {
		style = discord.ButtonPrimary
	}
	return discord.Message{
		Embeds: []discord.Embed{e},
		Components: []discord.Component{discord.Row(
			discord.Component{Type: discord.ComponentButton, Style: style, Label: "Yes, " + strings.ToLower(verb), CustomID: "wt:confirm:" + action + ":" + in.ID},
			discord.Component{Type: discord.ComponentButton, Style: discord.ButtonSecondary, Label: "Cancel", CustomID: "wt:refresh:" + in.ID},
		)},
	}
}

// playersMessage lists who is online on each running server.
func (s *Server) playersMessage() discord.Message {
	e := discord.Embed{Author: &discord.EmbedAuthor{Name: "WATCHTOWER"}, Title: "Who's online", Color: colorCrimson}
	total := 0
	for _, in := range s.sortedInstances() {
		if in.State != protocol.StateRunning {
			continue
		}
		st := s.stats(in)
		total += st.PlayerCount
		value := "Nobody right now"
		if len(st.Players) > 0 {
			sort.Strings(st.Players)
			value = truncate(strings.Join(st.Players, ", "), 1000)
		}
		name := fmt.Sprintf("%s · %d", in.Name, st.PlayerCount)
		if st.MaxPlayers > 0 {
			name += fmt.Sprintf("/%d", st.MaxPlayers)
		}
		e.Fields = append(e.Fields, discord.EmbedField{Name: name, Value: value})
	}
	if len(e.Fields) == 0 {
		e.Description = "No servers are running."
	} else {
		e.Description = fmt.Sprintf("**%d** %s online", total, plural(total, "player", "players"))
	}
	return discord.Message{Embeds: []discord.Embed{e}, Flags: discord.MessageFlagEphemeral}
}
