package notification

import (
	"context"
	"fmt"
	"strings"

	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons-db/models"
)

// SendWebhook posts a message as JSON to an arbitrary URL. It is the fallback
// for connection types with no sender of their own, and for the explicit
// generic_webhook and webhook types.
//
// The payload is deliberately flat — title, message, then every property — so a
// receiver can read it without knowing this repo's types.
func SendWebhook(ctx context.Context, conn *models.Connection, message Message) error {
	target := strings.TrimPrefix(conn.URL, "generic+")
	if target == "" {
		return fmt.Errorf("webhook connection %q has no URL", conn.Name)
	}

	payload := map[string]string{"title": message.Title, "message": message.Body}
	for key, value := range message.Properties {
		// Properties never override the two fields a receiver is guaranteed.
		if key == "title" || key == "message" {
			continue
		}
		payload[key] = value
	}

	client := commonshttp.NewClient()
	if conn.Username != "" || conn.Password != "" {
		client = client.Auth(conn.Username, conn.Password)
	}
	for key, value := range headerProperties(message.Properties) {
		client = client.Header(key, value)
	}

	response, err := client.R(ctx).Post(target, payload)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	if !response.IsOK() {
		body, _ := response.AsString()
		return fmt.Errorf("webhook returned %d: %s", response.StatusCode, body)
	}
	return nil
}
