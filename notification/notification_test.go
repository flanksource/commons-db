package notification_test

import (
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/notification"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNotification(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Notification Suite")
}

// Properties carry per-channel overrides on one delivery, so the un-prefixing
// rule decides whether an override reaches its channel at all.
var _ = Describe("PropertiesForChannel", func() {
	It("passes bare keys through to every channel", func() {
		resolved := notification.PropertiesForChannel(models.ConnectionTypeSlack,
			map[string]string{"channel": "#ops"})
		Expect(resolved).To(HaveKeyWithValue("channel", "#ops"))
	})

	It("un-prefixes a key for its own channel", func() {
		resolved := notification.PropertiesForChannel(models.ConnectionTypeSlack,
			map[string]string{"slack.channel": "#ops"})
		Expect(resolved).To(HaveKeyWithValue("channel", "#ops"))
	})

	It("drops a key addressed to a different channel", func() {
		resolved := notification.PropertiesForChannel(models.ConnectionTypeSlack,
			map[string]string{"teams.channel": "#other"})
		Expect(resolved).ToNot(HaveKey("channel"))
		Expect(resolved).ToNot(HaveKey("teams.channel"))
	})

	// The connection type is `email` but the historical property prefix is
	// `smtp`; accepting only one would make the other silently do nothing.
	It("accepts both email and smtp prefixes for mail", func() {
		Expect(notification.PropertiesForChannel(models.ConnectionTypeEmail,
			map[string]string{"email.subject": "A"})).To(HaveKeyWithValue("subject", "A"))
		Expect(notification.PropertiesForChannel(models.ConnectionTypeEmail,
			map[string]string{"smtp.subject": "B"})).To(HaveKeyWithValue("subject", "B"))
	})

	It("survives a nil property map", func() {
		Expect(notification.PropertiesForChannel(models.ConnectionTypeSlack, nil)).To(BeEmpty())
	})
})

var _ = Describe("Attachment support", func() {
	// This is the rule the whole delivery design rests on: only mail can carry
	// a file, and every other channel must be given a link instead. Dropping an
	// attachment silently would look exactly like a successful send.
	It("is an error, not a silent drop, to attach to a channel that cannot", func() {
		err := notification.ErrAttachmentsUnsupported{Channel: models.ConnectionTypeSlack}
		Expect(err.Error()).To(ContainSubstring("slack"))
		Expect(err.Error()).To(ContainSubstring("only email"))
		Expect(err.Error()).To(ContainSubstring("link"))
	})
})

var _ = Describe("IsSlackBlocksJSON", func() {
	It("recognises a Block Kit message", func() {
		Expect(notification.IsSlackBlocksJSON(`{"blocks":[{"type":"section"}]}`)).To(BeTrue())
	})

	It("treats plain text, and JSON that is not blocks, as text", func() {
		Expect(notification.IsSlackBlocksJSON("a plain message")).To(BeFalse())
		Expect(notification.IsSlackBlocksJSON(`{"text":"hello"}`)).To(BeFalse())
		Expect(notification.IsSlackBlocksJSON(`{"blocks":"not an array"}`)).To(BeFalse())
		Expect(notification.IsSlackBlocksJSON("")).To(BeFalse())
	})
})
