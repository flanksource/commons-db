// Package senders delivers a notification to one channel, chosen by the type of
// the connection addressing it.
//
// Each sender speaks its channel's own HTTP API directly. There is no router
// abstraction and no third-party dispatch library: a channel is a URL, a payload
// shape and a status code, and naming them explicitly is both smaller and easier
// to debug than a layer that hides them.
package senders

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/mail"
	"github.com/flanksource/commons-db/models"
)

// Sender delivers one message to one channel.
type Sender interface {
	Send(ctx context.Context, conn *models.Connection, data Data) error
}

// Data is the message to deliver.
//
// Attachments are carried here but honoured only by SMTP — no chat channel
// accepts a file on its webhook. Callers must not silently rely on them
// elsewhere; ForConnection's documentation and the schedule spec's validation
// both say so, because a report that quietly failed to arrive is worse than one
// that refused to be configured.
type Data struct {
	Title       string
	Message     string
	Properties  map[string]string
	Attachments []mail.Attachment
}

// httpClient bounds every webhook send. Chat webhooks answer in milliseconds;
// anything near this timeout is a failure worth surfacing, not worth waiting on.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// ForConnection returns the sender for a connection's type, or an error naming
// the type when there is none.
//
// SMTP is deliberately absent: it is not a webhook, it is the only channel that
// carries attachments, and it is handled by the notification package's own SMTP
// path rather than here.
func ForConnection(conn *models.Connection) (Sender, error) {
	if conn == nil {
		return nil, fmt.Errorf("connection is required")
	}
	switch conn.Type {
	case models.ConnectionTypeTelegram:
		return &Telegram{}, nil
	case models.ConnectionTypeDiscord:
		return &Discord{}, nil
	case models.ConnectionTypeTeams:
		return &Teams{}, nil
	case models.ConnectionTypeMattermost:
		return &Mattermost{}, nil
	case models.ConnectionTypeNtfy:
		return &Ntfy{}, nil
	case models.ConnectionTypePushbullet:
		return &Pushbullet{}, nil
	case models.ConnectionTypePushover:
		return &Pushover{}, nil
	default:
		return nil, fmt.Errorf("unsupported notification service: %s", conn.Type)
	}
}

// SupportsAttachments reports whether a channel can carry a file. Only SMTP
// can, so a caller asking to attach anywhere else is refused up front.
func SupportsAttachments(connectionType string) bool {
	return connectionType == models.ConnectionTypeEmail
}

// post sends a JSON body to a webhook and turns a non-2xx into an error naming
// the channel and echoing the response, which is the only thing that ever says
// why a webhook rejected a message.
func post(ctx context.Context, channel, url string, body []byte, headers map[string]string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytesReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return do(channel, request)
}
