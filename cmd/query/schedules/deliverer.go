package schedules

import (
	"fmt"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/mail"
	"github.com/flanksource/commons-db/notification"
)

// deliverer sends a schedule's report to its channels.
type deliverer struct {
	sender *notification.Sender

	// baseURL is where stored artifacts are readable. Channels that cannot
	// carry a file get a link built from it instead.
	baseURL string
}

// DelivererOptions configure delivery.
type DelivererOptions struct {
	// BaseURL is the externally reachable root of this server, used to build
	// the artifact links non-SMTP channels receive.
	BaseURL string
}

// NewDeliverer returns the default report deliverer.
func NewDeliverer(options DelivererOptions) Deliverer {
	return &deliverer{
		sender:  notification.NewSender(),
		baseURL: strings.TrimRight(options.BaseURL, "/"),
	}
}

// Deliver sends the report to every configured channel, returning one result
// per channel.
//
// A failure on one channel does not stop the others: the point of configuring
// three destinations is that losing one still delivers to two. Every outcome is
// reported so the run records which channels actually received it.
func (d *deliverer) Deliver(
	ctx dbcontext.Context,
	schedule Schedule,
	artifact Artifact,
) ([]DeliveryResult, error) {
	results := make([]DeliveryResult, 0, len(schedule.Deliver))
	for _, target := range schedule.Deliver {
		results = append(results, d.deliverOne(ctx, schedule, target, artifact))
	}
	return results, nil
}

func (d *deliverer) deliverOne(
	ctx dbcontext.Context,
	schedule Schedule,
	target DeliverySpec,
	artifact Artifact,
) DeliveryResult {
	result := DeliveryResult{Connection: target.Connection, At: time.Now()}

	message := notification.Message{
		Title:      d.title(schedule, target),
		Body:       d.body(schedule, target, artifact),
		Properties: target.Properties,
	}
	if target.Attach {
		message.Attachments = []mail.Attachment{{
			Filename:    artifact.Filename,
			ContentType: artifact.ContentType,
			Content:     artifact.Content,
		}}
	}

	sent, err := d.sender.Send(ctx, target.Connection, message)
	result.Channel = sent.Channel
	result.Attached = sent.Attached
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Sent = true
	return result
}

func (d *deliverer) title(schedule Schedule, target DeliverySpec) string {
	if target.Title != "" {
		return target.Title
	}
	if schedule.Report != nil && schedule.Report.Title != "" {
		return schedule.Report.Title
	}
	return schedule.Name
}

// body builds the message. A channel that cannot carry the report gets a link
// to it, so the message is never a notification about a file the reader has no
// way to reach.
func (d *deliverer) body(schedule Schedule, target DeliverySpec, artifact Artifact) string {
	var body strings.Builder
	if target.Message != "" {
		body.WriteString(target.Message)
	} else {
		fmt.Fprintf(&body, "The %s report has run.", schedule.Name)
	}
	if target.Attach || artifact.Path == "" {
		return body.String()
	}
	if link := d.link(artifact); link != "" {
		fmt.Fprintf(&body, "\n\n%s", link)
	}
	return body.String()
}

func (d *deliverer) link(artifact Artifact) string {
	if d.baseURL == "" {
		// Without a reachable base URL a path would be a link to nowhere, so
		// name the stored file instead of inventing a URL.
		return "Report stored at " + artifact.Path
	}
	return d.baseURL + "/api/v1/schedule/artifacts/" + strings.TrimLeft(artifact.Path, "/")
}
