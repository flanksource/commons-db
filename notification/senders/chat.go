package senders

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/commons-db/models"
)

// Discord posts through an incoming webhook, either given whole as the
// connection URL or assembled from a webhook id and token.
type Discord struct{}

type discordPayload struct {
	Content string         `json:"content,omitempty"`
	Embeds  []discordEmbed `json:"embeds,omitempty"`
}

type discordEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color,omitempty"`
}

func (d *Discord) Send(ctx context.Context, conn *models.Connection, data Data) error {
	webhook := conn.URL
	if webhook == "" {
		if conn.Username == "" || conn.Password == "" {
			return fmt.Errorf("discord connection requires a webhook URL, or a webhook id (username) and token (password)")
		}
		webhook = fmt.Sprintf("https://discord.com/api/webhooks/%s/%s", conn.Username, conn.Password)
	}

	body, err := json.Marshal(discordPayload{
		Embeds: []discordEmbed{{Title: data.Title, Description: data.Message}},
	})
	if err != nil {
		return err
	}
	return post(ctx, "discord", webhook, body, nil)
}

// Teams posts a MessageCard to an incoming webhook.
type Teams struct{}

type teamsMessageCard struct {
	Type       string         `json:"@type"`
	Context    string         `json:"@context"`
	ThemeColor string         `json:"themeColor,omitempty"`
	Summary    string         `json:"summary"`
	Sections   []teamsSection `json:"sections"`
}

type teamsSection struct {
	ActivityTitle string `json:"activityTitle,omitempty"`
	Text          string `json:"text"`
	Markdown      bool   `json:"markdown"`
}

func (t *Teams) Send(ctx context.Context, conn *models.Connection, data Data) error {
	webhook := webhookURL(conn)
	if webhook == "" {
		return fmt.Errorf("teams connection requires a webhook URL")
	}

	body, err := json.Marshal(teamsMessageCard{
		Type:       "MessageCard",
		Context:    "http://schema.org/extensions",
		ThemeColor: "0076D7",
		Summary:    data.Title,
		Sections: []teamsSection{{
			ActivityTitle: data.Title,
			Text:          data.Message,
			Markdown:      true,
		}},
	})
	if err != nil {
		return err
	}
	return post(ctx, "teams", webhook, body, nil)
}

// Mattermost posts to an incoming webhook, with optional channel, username and
// icon overrides carried on the connection's properties.
type Mattermost struct{}

type mattermostPayload struct {
	Channel  string `json:"channel,omitempty"`
	Username string `json:"username,omitempty"`
	IconURL  string `json:"icon_url,omitempty"`
	Text     string `json:"text"`
}

func (m *Mattermost) Send(ctx context.Context, conn *models.Connection, data Data) error {
	webhook := webhookURL(conn)
	if webhook == "" {
		return fmt.Errorf("mattermost connection requires a webhook URL")
	}

	payload := mattermostPayload{Text: data.Message, Username: conn.Username}
	if data.Title != "" {
		payload.Text = fmt.Sprintf("### %s\n\n%s", data.Title, data.Message)
	}
	if conn.Properties != nil {
		payload.Channel = conn.Properties["channel"]
		payload.IconURL = conn.Properties["iconURL"]
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return post(ctx, "mattermost", webhook, body, nil)
}

// webhookURL reads a webhook from the connection URL, falling back to the
// webhookURL property — the two places the connection schema allows one.
func webhookURL(conn *models.Connection) string {
	if conn.URL != "" {
		return conn.URL
	}
	if conn.Properties != nil {
		return conn.Properties["webhookURL"]
	}
	return ""
}
