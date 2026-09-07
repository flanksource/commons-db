package senders

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/flanksource/commons-db/models"
)

// Telegram sends one message per chat id. Chat ids are a comma-separated list
// on the connection username; the bot token is the password.
type Telegram struct{}

func (t *Telegram) Send(ctx context.Context, conn *models.Connection, data Data) error {
	token, chats := conn.Password, conn.Username
	if token == "" || chats == "" {
		return fmt.Errorf("telegram connection requires a bot token (password) and chat ids (username)")
	}

	for _, chatID := range strings.Split(chats, ",") {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			continue
		}
		if err := t.sendOne(ctx, token, chatID, data); err != nil {
			return fmt.Errorf("telegram chat %s: %w", chatID, err)
		}
	}
	return nil
}

func (t *Telegram) sendOne(ctx context.Context, token, chatID string, data Data) error {
	text := escapeMarkdownV2(data.Message)
	if data.Title != "" {
		text = fmt.Sprintf("*%s*\n\n%s", escapeMarkdownV2(data.Title), text)
	}

	body, err := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "MarkdownV2",
	})
	if err != nil {
		return err
	}
	return post(ctx, "telegram", fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token), body, nil)
}

// markdownV2Reserved are the characters Telegram's MarkdownV2 requires escaped.
// An unescaped one is not a formatting quirk — the API rejects the whole
// message.
var markdownV2Reserved = []string{
	"_", "*", "[", "]", "(", ")", "~", "`", ">", "#",
	"+", "-", "=", "|", "{", "}", ".", "!",
}

func escapeMarkdownV2(text string) string {
	// Backslash first: escaping it after the others would double-escape every
	// backslash the previous replacements just added.
	text = strings.ReplaceAll(text, "\\", "\\\\")
	for _, character := range markdownV2Reserved {
		text = strings.ReplaceAll(text, character, "\\"+character)
	}
	return text
}

// Ntfy publishes to a topic, on ntfy.sh unless the connection names a host.
type Ntfy struct{}

func (n *Ntfy) Send(ctx context.Context, conn *models.Connection, data Data) error {
	host := conn.URL
	if host == "" {
		host = "https://ntfy.sh"
	}
	host = strings.TrimRight(host, "/")

	topic := conn.Username
	if topic == "" && conn.Properties != nil {
		topic = conn.Properties["topic"]
	}
	if topic == "" {
		return fmt.Errorf("ntfy connection requires a topic")
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, host+"/"+topic, strings.NewReader(data.Message))
	if err != nil {
		return err
	}
	if data.Title != "" {
		request.Header.Set("Title", data.Title)
	}
	switch {
	case conn.Username != "" && conn.Password != "":
		request.SetBasicAuth(conn.Username, conn.Password)
	case conn.Password != "":
		request.Header.Set("Authorization", "Bearer "+conn.Password)
	}
	return do("ntfy", request)
}

// Pushbullet pushes a note using the connection's access token.
type Pushbullet struct{}

func (p *Pushbullet) Send(ctx context.Context, conn *models.Connection, data Data) error {
	token := conn.Password
	if token == "" {
		return fmt.Errorf("pushbullet connection requires an access token (password)")
	}

	body, err := json.Marshal(map[string]string{
		"type":  "note",
		"title": data.Title,
		"body":  data.Message,
	})
	if err != nil {
		return err
	}
	return post(ctx, "pushbullet", "https://api.pushbullet.com/v2/pushes", body,
		map[string]string{"Access-Token": token})
}

// Pushover posts a form-encoded message for one user key.
type Pushover struct{}

func (p *Pushover) Send(ctx context.Context, conn *models.Connection, data Data) error {
	token, user := conn.Password, conn.Username
	if token == "" || user == "" {
		return fmt.Errorf("pushover connection requires an application token (password) and user key (username)")
	}

	form := url.Values{
		"token":   {token},
		"user":    {user},
		"title":   {data.Title},
		"message": {data.Message},
		"html":    {"1"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.pushover.net/1/messages.json", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do("pushover", request)
}
