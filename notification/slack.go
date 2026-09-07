package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/slack-go/slack"
)

// slackMessage is the block payload a templated Slack message may carry
// instead of plain text.
type slackMessage struct {
	Blocks slack.Blocks `json:"blocks"`
}

// IsSlackBlocksJSON reports whether a message is a JSON object with a top-level
// blocks array, which is what distinguishes a Block Kit message from text that
// merely happens to be JSON.
func IsSlackBlocksJSON(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &raw); err != nil {
		return false
	}
	blocks, ok := raw["blocks"]
	if !ok {
		return false
	}
	var array []json.RawMessage
	return json.Unmarshal(blocks, &array) == nil
}

// SendSlack posts to a channel with a bot token. It is separate from the webhook
// senders because Slack's API is the only one that takes structured blocks, and
// flattening them to text would throw away the formatting a template built.
func SendSlack(ctx context.Context, apiToken, channel string, message Message) error {
	if apiToken == "" {
		return errors.New("slack connection requires a bot token")
	}
	if channel == "" {
		return errors.New("slack connection requires a channel")
	}

	api := slack.New(apiToken)

	var options []slack.MsgOption
	if message.Title != "" {
		options = append(options, slack.MsgOptionText(message.Title, false))
	}
	if message.Body != "" {
		if IsSlackBlocksJSON(message.Body) {
			var blocks slackMessage
			if err := json.Unmarshal([]byte(message.Body), &blocks); err != nil {
				return fmt.Errorf("decode slack blocks: %w", err)
			}
			options = append(options, slack.MsgOptionBlocks(blocks.Blocks.BlockSet...))
		} else {
			options = append(options, slack.MsgOptionText(message.Body, false))
		}
	}

	_, _, err := api.PostMessageContext(ctx, channel, options...)
	if err == nil {
		return nil
	}

	// A missing channel is nearly always the bot not being invited to it, which
	// the raw error does not say.
	var slackErr slack.SlackErrorResponse
	if errors.As(err, &slackErr) && slackErr.Err == "channel_not_found" {
		return fmt.Errorf(
			"slack channel %q not found: check the channel exists and the bot has been invited to it", channel)
	}
	return fmt.Errorf("slack: %w", err)
}
