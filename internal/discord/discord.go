// Package discord is a small Discord bot client: a gateway connection that
// receives interactions (slash commands, buttons, menus) and a REST client
// for messages. It covers only what Watchtower's bot uses, and needs no
// public address because Discord delivers interactions over the gateway.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"time"
)

// Message and embed types, trimmed to the fields Watchtower sends.

type Embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	URL         string       `json:"url,omitempty"`
	Color       int          `json:"color,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"`
	Author      *EmbedAuthor `json:"author,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
	Thumbnail   *EmbedImage  `json:"thumbnail,omitempty"`
	Image       *EmbedImage  `json:"image,omitempty"`
	Fields      []EmbedField `json:"fields,omitempty"`
}

type EmbedAuthor struct {
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

type EmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url,omitempty"`
}

type EmbedImage struct {
	URL string `json:"url"`
}

type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// Component types and button styles.
const (
	ComponentRow    = 1
	ComponentButton = 2
	ComponentSelect = 3

	ButtonPrimary   = 1
	ButtonSecondary = 2
	ButtonSuccess   = 3
	ButtonDanger    = 4
)

type Component struct {
	Type        int            `json:"type"`
	Components  []Component    `json:"components,omitempty"` // rows
	Style       int            `json:"style,omitempty"`      // buttons
	Label       string         `json:"label,omitempty"`
	Emoji       *Emoji         `json:"emoji,omitempty"`
	CustomID    string         `json:"custom_id,omitempty"`
	Disabled    bool           `json:"disabled,omitempty"`
	Placeholder string         `json:"placeholder,omitempty"` // select menus
	Options     []SelectOption `json:"options,omitempty"`
}

type Emoji struct {
	Name string `json:"name"`
}

type SelectOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	Emoji       *Emoji `json:"emoji,omitempty"`
}

// Row puts components in an action row.
func Row(c ...Component) Component { return Component{Type: ComponentRow, Components: c} }

// MessageFlagEphemeral shows an interaction reply only to the person who used it.
const MessageFlagEphemeral = 64

type Message struct {
	ID          string       `json:"id,omitempty"`
	ChannelID   string       `json:"channel_id,omitempty"`
	Content     string       `json:"content"`
	Embeds      []Embed      `json:"embeds"`
	Components  []Component  `json:"components"`
	Flags       int          `json:"flags,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Attachment struct {
	ID       any    `json:"id"` // index for new uploads, snowflake for existing ones
	Filename string `json:"filename,omitempty"`
}

// File is an upload referenced from embeds as attachment://Name.
type File struct {
	Name string
	Data []byte
}

// Interaction types and response types.
const (
	InteractionCommand      = 2
	InteractionComponent    = 3
	InteractionAutocomplete = 4

	ResponseMessage       = 4 // reply with a new message
	ResponseDeferUpdate   = 6 // acknowledge a component, edit later
	ResponseUpdateMessage = 7 // edit the message the component is on
	ResponseAutocomplete  = 8
)

type Interaction struct {
	ID            string          `json:"id"`
	ApplicationID string          `json:"application_id"`
	Type          int             `json:"type"`
	Token         string          `json:"token"`
	GuildID       string          `json:"guild_id"`
	ChannelID     string          `json:"channel_id"`
	Member        *Member         `json:"member"`
	User          *User           `json:"user"` // in DMs
	Data          InteractionData `json:"data"`
	Message       *Message        `json:"message"`
}

type InteractionData struct {
	Name     string          `json:"name"`      // command
	Options  []CommandOption `json:"options"`   // command
	CustomID string          `json:"custom_id"` // component
	Values   []string        `json:"values"`    // select menu
}

type CommandOption struct {
	Name    string `json:"name"`
	Type    int    `json:"type"`
	Value   any    `json:"value"`
	Focused bool   `json:"focused,omitempty"`
}

type Member struct {
	User        User     `json:"user"`
	Nick        string   `json:"nick"`
	Roles       []string `json:"roles"`
	Permissions string   `json:"permissions"` // bitfield as a decimal string
}

type User struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
}

// Caller is who used an interaction.
func (i Interaction) Caller() User {
	if i.Member != nil {
		return i.Member.User
	}
	if i.User != nil {
		return *i.User
	}
	return User{}
}

// Permission bits Watchtower checks.
const (
	PermAdministrator = 1 << 3
	PermManageGuild   = 1 << 5
)

// HasPermission reports whether the member's permissions in the channel include bit.
func (m *Member) HasPermission(bit uint64) bool {
	if m == nil {
		return false
	}
	p, _ := strconv.ParseUint(m.Permissions, 10, 64)
	return p&PermAdministrator != 0 || p&bit != 0
}

type InteractionResponse struct {
	Type int `json:"type"`
	Data any `json:"data,omitempty"`
}

// Command is a slash command definition.
type Command struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Options     []CommandParam `json:"options,omitempty"`
}

type CommandParam struct {
	Type         int    `json:"type"` // 3 = string
	Name         string `json:"name"`
	Description  string `json:"description"`
	Required     bool   `json:"required,omitempty"`
	Autocomplete bool   `json:"autocomplete,omitempty"`
}

type Choice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// REST is a Discord REST client authenticated as a bot.
type REST struct {
	Token   string
	BaseURL string // defaults to the live API; tests point it elsewhere
	HTTP    *http.Client
}

// Error is a failed API call.
type Error struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("discord: %d %s (code %d)", e.Status, e.Message, e.Code)
}

// IsNotFound reports whether err is Discord saying the thing doesn't exist,
// such as a status message someone deleted.
func IsNotFound(err error) bool {
	var de *Error
	return errors.As(err, &de) && de.Status == http.StatusNotFound
}

func (r *REST) base() string {
	if r.BaseURL != "" {
		return r.BaseURL
	}
	return "https://discord.com/api/v10"
}

// Do sends a JSON request, or multipart when files are given, and decodes the
// reply into out. It waits and retries once when rate limited.
func (r *REST) Do(ctx context.Context, method, path string, body any, files []File, out any) error {
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	for attempt := 0; ; attempt++ {
		var buf bytes.Buffer
		contentType := ""
		if len(files) > 0 {
			mw := multipart.NewWriter(&buf)
			payload, err := json.Marshal(body)
			if err != nil {
				return err
			}
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="payload_json"`)
			h.Set("Content-Type", "application/json")
			pw, _ := mw.CreatePart(h)
			pw.Write(payload)
			for i, f := range files {
				fw, err := mw.CreateFormFile(fmt.Sprintf("files[%d]", i), f.Name)
				if err != nil {
					return err
				}
				fw.Write(f.Data)
			}
			mw.Close()
			contentType = mw.FormDataContentType()
		} else if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				return err
			}
			contentType = "application/json"
		}
		req, err := http.NewRequestWithContext(ctx, method, r.base()+path, &buf)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bot "+r.Token)
		req.Header.Set("User-Agent", "DiscordBot (https://github.com/sh4rpx88/Watchtower, 1)")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.Unmarshal(data, &rl)
			wait := time.Duration(rl.RetryAfter*float64(time.Second)) + 100*time.Millisecond
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if resp.StatusCode >= 300 {
			e := &Error{Status: resp.StatusCode}
			json.Unmarshal(data, e)
			if e.Message == "" {
				e.Message = http.StatusText(resp.StatusCode)
			}
			return e
		}
		if out != nil && len(data) > 0 {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

// Respond answers an interaction. It must happen within three seconds.
func (r *REST) Respond(ctx context.Context, in Interaction, resp InteractionResponse, files []File) error {
	return r.Do(ctx, "POST", "/interactions/"+in.ID+"/"+in.Token+"/callback", resp, files, nil)
}

// EditResponse changes the reply to an interaction, for work that finishes
// after a deferred response.
func (r *REST) EditResponse(ctx context.Context, applicationID, token string, msg Message, files []File) error {
	return r.Do(ctx, "PATCH", "/webhooks/"+applicationID+"/"+token+"/messages/@original", msg, files, nil)
}

func (r *REST) CreateMessage(ctx context.Context, channelID string, msg Message, files []File) (Message, error) {
	var out Message
	err := r.Do(ctx, "POST", "/channels/"+channelID+"/messages", msg, files, &out)
	return out, err
}

func (r *REST) EditMessage(ctx context.Context, channelID, messageID string, msg Message, files []File) (Message, error) {
	var out Message
	err := r.Do(ctx, "PATCH", "/channels/"+channelID+"/messages/"+messageID, msg, files, &out)
	return out, err
}

// SetGuildCommands replaces the bot's slash commands in one server. Guild
// commands show up right away, unlike global ones.
func (r *REST) SetGuildCommands(ctx context.Context, applicationID, guildID string, cmds []Command) error {
	return r.Do(ctx, "PUT", "/applications/"+applicationID+"/guilds/"+guildID+"/commands", cmds, nil, nil)
}

// MarshalJSON sends empty lists rather than null, so an edit clears embeds
// or buttons it leaves out.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if m.Embeds == nil {
		m.Embeds = []Embed{}
	}
	if m.Components == nil {
		m.Components = []Component{}
	}
	return json.Marshal(plain(m))
}
