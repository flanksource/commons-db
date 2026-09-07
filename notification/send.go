// Package notification delivers a message to a channel named by a connection.
//
// Dispatch is by connection type, not by URL scheme: Slack has a native API
// client, every other chat and push channel has a small HTTP sender, and SMTP
// and the generic webhook are reached by URL when no typed connection applies.
package notification

import (
	"fmt"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/mail"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/notification/senders"
)

// Message is what gets delivered.
type Message struct {
	Title string
	Body  string

	// Properties are per-channel options. A key may be namespaced with the
	// channel it applies to (`slack.icon`), and is un-prefixed for that channel
	// and dropped for the others.
	Properties map[string]string

	// Attachments are carried by SMTP only. Sending them anywhere else is
	// refused rather than silently dropped — see ErrAttachmentsUnsupported.
	Attachments []mail.Attachment
}

// Result records what one delivery did.
type Result struct {
	Connection string
	Channel    string
	Attached   bool
}

// ErrAttachmentsUnsupported is returned when a message carries an attachment to
// a channel that cannot deliver one. It is an error rather than a silent drop
// because a report that never arrived is indistinguishable from one that was
// never sent.
type ErrAttachmentsUnsupported struct {
	Channel string
}

func (e ErrAttachmentsUnsupported) Error() string {
	return fmt.Sprintf(
		"channel %q cannot carry attachments; only email can — send a link to the stored report instead", e.Channel)
}

// Sender delivers messages through resolved connections.
type Sender struct {
	// SystemSMTP is the connection name used for email when a delivery names
	// no connection of its own. Empty means there is no system mailer.
	SystemSMTP string
}

// NewSender creates a sender.
func NewSender() *Sender { return &Sender{} }

// Send delivers a message through the named connection.
//
// The connection is resolved and hydrated by the caller's context, so secrets
// are already materialised by the time a sender sees them.
func (s *Sender) Send(ctx dbcontext.Context, connectionName string, message Message) (Result, error) {
	conn, err := resolveConnection(ctx, connectionName)
	if err != nil {
		return Result{}, err
	}

	if len(message.Attachments) > 0 && !senders.SupportsAttachments(conn.Type) {
		return Result{}, ErrAttachmentsUnsupported{Channel: conn.Type}
	}

	message.Properties = PropertiesForChannel(conn.Type, message.Properties)
	result := Result{Connection: connectionName, Channel: conn.Type}

	switch conn.Type {
	case models.ConnectionTypeSlack:
		channel := conn.Username
		if named := message.Properties["channel"]; named != "" {
			channel = named
		}
		return result, SendSlack(ctx, conn.Password, channel, message)

	case models.ConnectionTypeEmail:
		result.Attached = len(message.Attachments) > 0
		return result, s.sendMail(conn, message)
	}

	sender, err := senders.ForConnection(conn)
	if err == nil {
		return result, sender.Send(ctx, conn, senders.Data{
			Title:       message.Title,
			Message:     message.Body,
			Properties:  message.Properties,
			Attachments: message.Attachments,
		})
	}

	// Anything left is addressed by URL rather than by a typed channel.
	if conn.URL != "" {
		return result, SendWebhook(ctx, conn, message)
	}
	return Result{}, fmt.Errorf("connection %q of type %q cannot deliver notifications",
		connectionName, conn.Type)
}

func (s *Sender) sendMail(conn *models.Connection, message Message) error {
	smtp, err := mail.FromConnection(conn)
	if err != nil {
		return err
	}

	recipients := smtp.To
	if to := message.Properties["to"]; to != "" {
		recipients = splitList(to)
	}
	if len(recipients) == 0 {
		return fmt.Errorf("email connection %q names no recipients", conn.Name)
	}

	subject := message.Title
	if named := message.Properties["subject"]; named != "" {
		subject = named
	}

	envelope := mail.New(recipients, subject, message.Body, `text/html; charset="UTF-8"`).
		SetFrom(smtp.FromName, smtp.FromAddress)
	for _, attachment := range message.Attachments {
		envelope.AddAttachment(attachment)
	}
	for key, value := range headerProperties(message.Properties) {
		envelope.SetHeader(key, value)
	}
	return envelope.Send(smtp)
}

// PropertiesForChannel resolves namespaced properties for one channel: bare
// keys pass through, and a `channel.key` key is un-prefixed for its own channel
// and dropped for every other. It is what lets a single delivery carry
// per-channel overrides without a separate block for each.
func PropertiesForChannel(channel string, properties map[string]string) map[string]string {
	if properties == nil {
		return map[string]string{}
	}
	// The connection type is `email`; the historical property prefix is `smtp`.
	// Accept both so neither spelling silently does nothing.
	prefixes := []string{channel + "."}
	if channel == models.ConnectionTypeEmail {
		prefixes = append(prefixes, "smtp.")
	}

	out := make(map[string]string, len(properties))
	for key, value := range properties {
		if !strings.Contains(key, ".") {
			out[key] = value
			continue
		}
		for _, prefix := range prefixes {
			if after, found := strings.CutPrefix(key, prefix); found {
				out[after] = value
			}
		}
	}
	return out
}

// headerProperties extracts `header.X` properties as mail headers.
func headerProperties(properties map[string]string) map[string]string {
	headers := map[string]string{}
	for key, value := range properties {
		if after, found := strings.CutPrefix(key, "header."); found {
			headers[after] = value
		}
	}
	return headers
}

func resolveConnection(ctx dbcontext.Context, name string) (*models.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("delivery connection is required")
	}
	namespace := ""
	if parts := strings.SplitN(name, "/", 2); len(parts) == 2 {
		namespace, name = parts[0], parts[1]
	}
	conn, err := ctx.GetConnection(name, namespace)
	if err != nil {
		return nil, fmt.Errorf("notification connection %q: %w", name, err)
	}
	if conn == nil {
		return nil, fmt.Errorf("notification connection %q not found", name)
	}
	return conn, nil
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
