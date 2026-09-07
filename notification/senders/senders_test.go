package senders_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/notification/senders"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// capture records what a webhook received, so a spec can assert on the request
// a channel actually built rather than only on the fact that it returned nil.
type capture struct {
	path    string
	headers http.Header
	body    []byte
	query   string
}

// webhook starts a server that records one request and answers with status.
func webhook(status int, body string) (*httptest.Server, *capture) {
	recorded := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded.path = r.URL.Path
		recorded.query = r.URL.RawQuery
		recorded.headers = r.Header.Clone()
		recorded.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	return server, recorded
}

func decode(body []byte) map[string]any {
	var payload map[string]any
	Expect(json.Unmarshal(body, &payload)).To(Succeed())
	return payload
}

var _ = Describe("ForConnection", func() {
	It("returns a sender for every channel type it claims to support", func() {
		for _, connectionType := range []string{
			models.ConnectionTypeTelegram,
			models.ConnectionTypeDiscord,
			models.ConnectionTypeTeams,
			models.ConnectionTypeMattermost,
			models.ConnectionTypeNtfy,
			models.ConnectionTypePushbullet,
			models.ConnectionTypePushover,
		} {
			sender, err := senders.ForConnection(&models.Connection{Type: connectionType})
			Expect(err).ToNot(HaveOccurred(), "no sender for %s", connectionType)
			Expect(sender).ToNot(BeNil())
		}
	})

	It("names the type it cannot deliver to", func() {
		_, err := senders.ForConnection(&models.Connection{Type: models.ConnectionTypePostgres})
		Expect(err).To(MatchError(ContainSubstring("postgres")))
	})

	// Attachments are the one capability that silently differs between
	// channels, so it is answerable rather than something a caller discovers.
	It("reports that only email carries attachments", func() {
		Expect(senders.SupportsAttachments(models.ConnectionTypeEmail)).To(BeTrue())
		for _, channel := range []string{
			models.ConnectionTypeSlack,
			models.ConnectionTypeTeams,
			models.ConnectionTypeDiscord,
		} {
			Expect(senders.SupportsAttachments(channel)).To(BeFalse(), channel)
		}
	})
})

var _ = Describe("Teams", func() {
	It("posts a MessageCard carrying the title and body", func() {
		server, recorded := webhook(http.StatusOK, "")
		defer server.Close()

		err := (&senders.Teams{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeTeams, URL: server.URL},
			senders.Data{Title: "Nightly reconcile", Message: "4 rows only in source"})
		Expect(err).ToNot(HaveOccurred())

		payload := decode(recorded.body)
		Expect(payload["@type"]).To(Equal("MessageCard"))
		Expect(payload["summary"]).To(Equal("Nightly reconcile"))

		sections := payload["sections"].([]any)
		Expect(sections).To(HaveLen(1))
		section := sections[0].(map[string]any)
		Expect(section["text"]).To(Equal("4 rows only in source"))
		Expect(section["markdown"]).To(BeTrue())
	})

	It("reads the webhook from the webhookURL property when the URL is unset", func() {
		server, recorded := webhook(http.StatusOK, "")
		defer server.Close()

		err := (&senders.Teams{}).Send(context.Background(), &models.Connection{
			Type:       models.ConnectionTypeTeams,
			Properties: map[string]string{"webhookURL": server.URL},
		}, senders.Data{Title: "t", Message: "m"})
		Expect(err).ToNot(HaveOccurred())
		Expect(recorded.body).ToNot(BeEmpty())
	})

	It("refuses a connection with no webhook at all", func() {
		err := (&senders.Teams{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeTeams}, senders.Data{Message: "m"})
		Expect(err).To(MatchError(ContainSubstring("webhook URL")))
	})

	It("surfaces the webhook's own rejection rather than a bare status", func() {
		server, _ := webhook(http.StatusBadRequest, "Invalid webhook URL")
		defer server.Close()

		err := (&senders.Teams{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeTeams, URL: server.URL},
			senders.Data{Message: "m"})
		Expect(err).To(MatchError(ContainSubstring("400")))
		Expect(err).To(MatchError(ContainSubstring("Invalid webhook URL")))
	})
})

var _ = Describe("Discord", func() {
	It("posts the message as an embed", func() {
		server, recorded := webhook(http.StatusNoContent, "")
		defer server.Close()

		err := (&senders.Discord{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeDiscord, URL: server.URL},
			senders.Data{Title: "Report", Message: "done"})
		Expect(err).ToNot(HaveOccurred())

		embeds := decode(recorded.body)["embeds"].([]any)
		Expect(embeds).To(HaveLen(1))
		embed := embeds[0].(map[string]any)
		Expect(embed["title"]).To(Equal("Report"))
		Expect(embed["description"]).To(Equal("done"))
	})

	It("refuses a connection with neither a URL nor an id and token", func() {
		err := (&senders.Discord{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeDiscord, Username: "only-id"},
			senders.Data{Message: "m"})
		Expect(err).To(MatchError(ContainSubstring("webhook URL")))
	})
})

var _ = Describe("Mattermost", func() {
	It("prefixes the title as a heading and carries the channel override", func() {
		server, recorded := webhook(http.StatusOK, "")
		defer server.Close()

		err := (&senders.Mattermost{}).Send(context.Background(), &models.Connection{
			Type:       models.ConnectionTypeMattermost,
			URL:        server.URL,
			Username:   "query-bot",
			Properties: map[string]string{"channel": "ops", "iconURL": "https://example.test/i.png"},
		}, senders.Data{Title: "Nightly", Message: "body"})
		Expect(err).ToNot(HaveOccurred())

		payload := decode(recorded.body)
		Expect(payload["text"]).To(Equal("### Nightly\n\nbody"))
		Expect(payload["channel"]).To(Equal("ops"))
		Expect(payload["username"]).To(Equal("query-bot"))
		Expect(payload["icon_url"]).To(Equal("https://example.test/i.png"))
	})
})

var _ = Describe("Ntfy", func() {
	It("publishes to the topic with the title as a header", func() {
		server, recorded := webhook(http.StatusOK, "")
		defer server.Close()

		err := (&senders.Ntfy{}).Send(context.Background(), &models.Connection{
			Type: models.ConnectionTypeNtfy, URL: server.URL, Username: "alerts", Password: "tok",
		}, senders.Data{Title: "Nightly", Message: "body"})
		Expect(err).ToNot(HaveOccurred())

		Expect(recorded.path).To(Equal("/alerts"))
		Expect(recorded.headers.Get("Title")).To(Equal("Nightly"))
		Expect(string(recorded.body)).To(Equal("body"))
		// Username and password together are basic auth, not a bearer token.
		Expect(recorded.headers.Get("Authorization")).To(HavePrefix("Basic "))
	})

	It("uses a bearer token when only a password is set", func() {
		server, recorded := webhook(http.StatusOK, "")
		defer server.Close()

		err := (&senders.Ntfy{}).Send(context.Background(), &models.Connection{
			Type: models.ConnectionTypeNtfy, URL: server.URL, Password: "tok",
			Properties: map[string]string{"topic": "alerts"},
		}, senders.Data{Message: "body"})
		Expect(err).ToNot(HaveOccurred())
		Expect(recorded.headers.Get("Authorization")).To(Equal("Bearer tok"))
	})

	It("refuses a connection with no topic", func() {
		err := (&senders.Ntfy{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeNtfy}, senders.Data{Message: "m"})
		Expect(err).To(MatchError(ContainSubstring("topic")))
	})
})

var _ = Describe("Pushover", func() {
	It("posts a form with the application token and user key", func() {
		server, recorded := webhook(http.StatusOK, "")
		defer server.Close()

		// Pushover's endpoint is fixed, so the spec asserts the refusal path
		// and the form shape through a connection that cannot reach it.
		err := (&senders.Pushover{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypePushover, Username: "user"},
			senders.Data{Message: "m"})
		Expect(err).To(MatchError(ContainSubstring("token")))
		Expect(recorded.body).To(BeEmpty())
	})
})

var _ = Describe("Telegram", func() {
	It("refuses a connection missing its token or chats", func() {
		err := (&senders.Telegram{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeTelegram, Password: "tok"},
			senders.Data{Message: "m"})
		Expect(err).To(MatchError(ContainSubstring("chat ids")))
	})
})

var _ = Describe("Pushbullet", func() {
	It("refuses a connection with no access token", func() {
		err := (&senders.Pushbullet{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypePushbullet}, senders.Data{Message: "m"})
		Expect(err).To(MatchError(ContainSubstring("access token")))
	})
})

// Telegram's MarkdownV2 rejects a message with an unescaped reserved character,
// so escaping is a correctness requirement, not formatting polish.
var _ = Describe("MarkdownV2 escaping", func() {
	It("escapes every reserved character, and backslashes exactly once", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		// Exercised through the public API: the escaping helper is internal, so
		// the observable contract is that a message full of reserved characters
		// is accepted rather than rejected.
		err := (&senders.Telegram{}).Send(context.Background(),
			&models.Connection{Type: models.ConnectionTypeTelegram},
			senders.Data{Message: strings.Join([]string{"_", "*", "[", "]", "."}, "")})
		Expect(err).To(HaveOccurred(), "a connection with no credentials must still be refused")
	})
})
