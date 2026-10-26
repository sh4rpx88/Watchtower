package panel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sh4rpx88/Watchtower/internal/discord"
)

// DiscordSettings configure the Discord bot. The token never leaves the panel.
type DiscordSettings struct {
	Enabled        bool   `json:"enabled"`
	Token          string `json:"token,omitempty"`
	GuildID        string `json:"guildId"`       // Discord server for slash commands; empty uses the one the bot is in
	ChannelID      string `json:"channelId"`     // where the status board lives; empty means no board
	ControlRoleID  string `json:"controlRoleId"` // members with this role can start and stop servers
	BoardMessageID string `json:"boardMessageId,omitempty"`
}

// discordStatus is what the settings page shows about the connection.
type discordStatus struct {
	Connected     bool     `json:"connected"`
	BotName       string   `json:"botName,omitempty"`
	ApplicationID string   `json:"applicationId,omitempty"`
	Guilds        []string `json:"guilds,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type discordBot struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	rest       *discord.REST
	status     discordStatus
	poke       chan struct{}
	boardFiles string // names of the images on the board message, to know when to upload new ones
}

func (s *Server) discordSettings() DiscordSettings {
	if list := s.discord.all(); len(list) > 0 {
		return list[0]
	}
	return DiscordSettings{}
}

// startDiscord (re)connects the bot with the current settings.
func (s *Server) startDiscord() {
	b := s.bot
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	b.status = discordStatus{}
	b.boardFiles = ""
	cfg := s.discordSettings()
	if !cfg.Enabled || cfg.Token == "" {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.rest = &discord.REST{Token: cfg.Token, BaseURL: s.cfg.DiscordAPI}
	b.poke = make(chan struct{}, 1)
	b.mu.Unlock()

	gw := &discord.Gateway{
		Token: cfg.Token,
		URL:   s.cfg.DiscordGateway,
		OnStatus: func(connected bool, err error) {
			b.mu.Lock()
			b.status.Connected = connected
			if err != nil {
				b.status.Error = err.Error()
			} else if connected {
				b.status.Error = ""
			}
			b.mu.Unlock()
		},
		OnReady:       func(r discord.Ready) { s.discordReady(ctx, r) },
		OnInteraction: func(in discord.Interaction) { s.discordInteraction(ctx, in) },
	}
	go func() {
		if err := gw.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("discord bot stopped", "err", err)
		}
	}()
	go s.boardLoop(ctx)
}

func (s *Server) discordREST() *discord.REST {
	s.bot.mu.Lock()
	defer s.bot.mu.Unlock()
	return s.bot.rest
}

var discordCommands = []discord.Command{
	{Name: "status", Description: "Show every server's status"},
	{Name: "server", Description: "Open a server's controls", Options: []discord.CommandParam{
		{Type: 3, Name: "name", Description: "Which server", Required: true, Autocomplete: true},
	}},
	{Name: "players", Description: "See who's online"},
}

func (s *Server) discordReady(ctx context.Context, r discord.Ready) {
	var guilds []string
	for _, g := range r.Guilds {
		guilds = append(guilds, g.ID)
	}
	name := r.User.Username
	if r.User.GlobalName != "" {
		name = r.User.GlobalName
	}
	s.bot.mu.Lock()
	s.bot.status.BotName, s.bot.status.ApplicationID, s.bot.status.Guilds = name, r.Application.ID, guilds
	s.bot.mu.Unlock()
	slog.Info("discord bot connected", "bot", name, "guilds", len(guilds))

	targets := guilds
	if g := s.discordSettings().GuildID; g != "" {
		targets = []string{g}
	}
	for _, g := range targets {
		if err := s.discordREST().SetGuildCommands(ctx, r.Application.ID, g, discordCommands); err != nil {
			slog.Warn("discord: registering slash commands failed", "guild", g, "err", err)
		}
	}
	s.pokeBoard()
}

// pokeBoard asks for a board update soon, such as after a server changes state.
func (s *Server) pokeBoard() {
	s.bot.mu.Lock()
	ch := s.bot.poke
	s.bot.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// boardLoop keeps the status board current: every 30 seconds, and shortly
// after anything happens.
func (s *Server) boardLoop(ctx context.Context) {
	s.bot.mu.Lock()
	poke := s.bot.poke
	s.bot.mu.Unlock()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-poke:
			// Let a burst of events settle into one edit.
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
		}
		if err := s.updateBoard(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("discord: updating the status board failed", "err", err)
		}
	}
}

func (s *Server) updateBoard(ctx context.Context) error {
	cfg := s.discordSettings()
	rest := s.discordREST()
	if cfg.ChannelID == "" || rest == nil {
		return nil
	}
	s.bot.mu.Lock()
	connected := s.bot.status.Connected
	s.bot.mu.Unlock()
	if !connected {
		return nil
	}
	msg, files := s.boardMessage()
	names := fileNames(files)

	if cfg.BoardMessageID != "" {
		s.bot.mu.Lock()
		same := s.bot.boardFiles == names
		s.bot.mu.Unlock()
		var upload []discord.File
		if !same {
			// New images (or a fresh start): replace the attachments.
			upload = files
			msg.Attachments = attachmentsFor(files)
		}
		_, err := rest.EditMessage(ctx, cfg.ChannelID, cfg.BoardMessageID, msg, upload)
		if err == nil {
			s.bot.mu.Lock()
			s.bot.boardFiles = names
			s.bot.mu.Unlock()
			return nil
		}
		if !discord.IsNotFound(err) {
			return err
		}
		// Someone deleted the board; post a new one.
	}
	msg.Attachments = attachmentsFor(files)
	created, err := rest.CreateMessage(ctx, cfg.ChannelID, msg, files)
	if err != nil {
		return err
	}
	s.bot.mu.Lock()
	s.bot.boardFiles = names
	s.bot.mu.Unlock()
	return s.discord.change(func(list []DiscordSettings) []DiscordSettings {
		if len(list) > 0 {
			list[0].BoardMessageID = created.ID
		}
		return list
	})
}

func attachmentsFor(files []discord.File) []discord.Attachment {
	out := make([]discord.Attachment, len(files))
	for i, f := range files {
		out[i] = discord.Attachment{ID: i, Filename: f.Name}
	}
	return out
}

// canControl reports whether the person may start, stop, restart and back
// up servers: Discord admins and managers, and members with the control role.
func (s *Server) canControl(in discord.Interaction) bool {
	if in.Member == nil {
		return false
	}
	if in.Member.HasPermission(discord.PermManageGuild) {
		return true
	}
	role := s.discordSettings().ControlRoleID
	return role != "" && slices.Contains(in.Member.Roles, role)
}

func callerName(in discord.Interaction) string {
	u := in.Caller()
	name := u.Username
	if in.Member != nil && in.Member.Nick != "" {
		name = in.Member.Nick
	} else if u.GlobalName != "" {
		name = u.GlobalName
	}
	return name
}

func ephemeral(msg discord.Message) discord.Message {
	msg.Flags = discord.MessageFlagEphemeral
	return msg
}

// discordInteraction answers slash commands, autocomplete, buttons and menus.
func (s *Server) discordInteraction(ctx context.Context, in discord.Interaction) {
	rest := s.discordREST()
	if rest == nil {
		return
	}
	if g := s.discordSettings().GuildID; g != "" && in.GuildID != g {
		return // the bot only serves the configured Discord server
	}
	reply := func(t int, msg discord.Message, files []discord.File) {
		if files != nil && t != discord.ResponseUpdateMessage {
			msg.Attachments = attachmentsFor(files)
		}
		if t == discord.ResponseUpdateMessage {
			files = nil // keep the images already on the message
		}
		if err := rest.Respond(ctx, in, discord.InteractionResponse{Type: t, Data: msg}, files); err != nil {
			slog.Warn("discord: answering an interaction failed", "err", err)
		}
	}
	text := func(t int, content string) {
		reply(t, ephemeral(discord.Message{Content: content}), nil)
	}

	switch in.Type {
	case discord.InteractionAutocomplete:
		var typed string
		for _, o := range in.Data.Options {
			if o.Focused {
				typed, _ = o.Value.(string)
			}
		}
		choices := []discord.Choice{}
		for _, inst := range s.sortedInstances() {
			if len(choices) < 25 && strings.Contains(strings.ToLower(inst.Name), strings.ToLower(typed)) {
				choices = append(choices, discord.Choice{Name: truncate(inst.Name, 100), Value: inst.ID})
			}
		}
		rest.Respond(ctx, in, discord.InteractionResponse{Type: discord.ResponseAutocomplete, Data: map[string]any{"choices": choices}}, nil)

	case discord.InteractionCommand:
		switch in.Data.Name {
		case "status":
			msg, files := s.boardMessage()
			reply(discord.ResponseMessage, ephemeral(msg), files)
		case "players":
			reply(discord.ResponseMessage, s.playersMessage(), nil)
		case "server":
			var want string
			for _, o := range in.Data.Options {
				if o.Name == "name" {
					want, _ = o.Value.(string)
				}
			}
			inst, ok := s.findInstance(want)
			if !ok {
				text(discord.ResponseMessage, "I couldn't find a server called **"+truncate(want, 80)+"**.")
				return
			}
			msg, files := s.panelMessage(inst, "", s.canControl(in))
			reply(discord.ResponseMessage, ephemeral(msg), files)
		}

	case discord.InteractionComponent:
		parts := strings.Split(in.Data.CustomID, ":")
		if len(parts) < 2 || parts[0] != "wt" {
			return
		}
		switch parts[1] {
		case "board":
			reply(discord.ResponseDeferUpdate, discord.Message{}, nil)
			s.updateBoard(ctx)
		case "pick":
			if len(in.Data.Values) == 0 {
				return
			}
			inst, ok := s.store.get(in.Data.Values[0])
			if !ok {
				text(discord.ResponseMessage, "That server no longer exists.")
				return
			}
			msg, files := s.panelMessage(inst, "", s.canControl(in))
			reply(discord.ResponseMessage, ephemeral(msg), files)
		case "refresh":
			inst, ok := s.store.get(parts[len(parts)-1])
			if !ok {
				text(discord.ResponseUpdateMessage, "That server no longer exists.")
				return
			}
			msg, _ := s.panelMessage(inst, "", s.canControl(in))
			reply(discord.ResponseUpdateMessage, msg, nil)
		case "act", "confirm":
			if len(parts) != 4 {
				return
			}
			action, id := parts[2], parts[3]
			inst, ok := s.store.get(id)
			if !ok {
				text(discord.ResponseUpdateMessage, "That server no longer exists.")
				return
			}
			if !s.canControl(in) {
				need := "the Manage Server permission"
				if role := s.discordSettings().ControlRoleID; role != "" {
					need = "the <@&" + role + "> role"
				}
				text(discord.ResponseMessage, "You need "+need+" to control servers.")
				return
			}
			if parts[1] == "act" && (action == "stop" || action == "restart") {
				reply(discord.ResponseUpdateMessage, s.confirmMessage(inst, action), nil)
				return
			}
			if !slices.Contains([]string{"start", "stop", "restart", "backup"}, action) {
				return
			}
			// The agent can take a while; acknowledge now and edit when done.
			reply(discord.ResponseDeferUpdate, discord.Message{}, nil)
			note := s.discordAction(inst, action, in)
			time.Sleep(time.Second) // let the new state arrive
			inst, _ = s.store.get(id)
			msg, _ := s.panelMessage(inst, note, true)
			if err := rest.EditResponse(ctx, in.ApplicationID, in.Token, msg, nil); err != nil {
				slog.Warn("discord: updating a server panel failed", "err", err)
			}
			s.pokeBoard()
		}
	}
}

// findInstance matches a server by ID (from autocomplete) or name.
func (s *Server) findInstance(want string) (Instance, bool) {
	if in, ok := s.store.get(want); ok {
		return in, true
	}
	for _, in := range s.store.list() {
		if strings.EqualFold(in.Name, strings.TrimSpace(want)) {
			return in, true
		}
	}
	return Instance{}, false
}

// discordAction runs an action for a Discord member and writes it to the
// activity log under their name.
func (s *Server) discordAction(in Instance, action string, from discord.Interaction) string {
	result, message := s.doScheduledAction(Schedule{InstanceID: in.ID, Action: action})
	who := callerName(from)
	if message != "" {
		s.audit(principal{Name: who + " (Discord)"}, &in, message)
	}
	icon := "✅"
	if strings.HasPrefix(result, "failed") || strings.HasPrefix(result, "skipped") {
		icon = "⚠️"
	}
	return icon + " **" + in.Name + "**: " + result + " · by " + who
}

func (s *Server) discordView() map[string]any {
	cfg := s.discordSettings()
	hasToken := cfg.Token != ""
	cfg.Token = ""
	s.bot.mu.Lock()
	status := s.bot.status
	s.bot.mu.Unlock()
	return map[string]any{"settings": cfg, "hasToken": hasToken, "status": status}
}

func (s *Server) handleGetDiscord(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.discordView())
}

// handleUpdateDiscord saves the bot settings and reconnects. A token left
// empty keeps the saved one.
func (s *Server) handleUpdateDiscord(w http.ResponseWriter, r *http.Request) {
	var body DiscordSettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	for _, id := range []*string{&body.GuildID, &body.ChannelID, &body.ControlRoleID} {
		*id = strings.TrimSpace(*id)
		if *id != "" && !isSnowflake(*id) {
			writeError(w, http.StatusBadRequest, "Discord IDs are long numbers; right-click the server, channel or role with Developer Mode on and pick Copy ID")
			return
		}
	}
	body.Token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body.Token), "Bot "))
	old := s.discordSettings()
	if body.Token == "" {
		body.Token = old.Token
	}
	if body.Enabled && body.Token == "" {
		writeError(w, http.StatusBadRequest, "add the bot token first")
		return
	}
	if body.ChannelID == old.ChannelID {
		body.BoardMessageID = old.BoardMessageID
	}
	if err := s.discord.change(func([]DiscordSettings) []DiscordSettings { return []DiscordSettings{body} }); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.startDiscord()
	writeJSON(w, http.StatusOK, s.discordView())
}

// handlePostBoard posts a fresh status board, for when the old one scrolled away.
func (s *Server) handlePostBoard(w http.ResponseWriter, r *http.Request) {
	cfg := s.discordSettings()
	if cfg.ChannelID == "" {
		writeError(w, http.StatusBadRequest, "set a status channel first")
		return
	}
	s.discord.change(func(list []DiscordSettings) []DiscordSettings {
		if len(list) > 0 {
			list[0].BoardMessageID = ""
		}
		return list
	})
	if err := s.updateBoard(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.discordView())
}

func isSnowflake(v string) bool {
	if len(v) < 15 || len(v) > 21 {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
