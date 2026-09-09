package mail

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/properties"
)

// Attachment is a file carried by a message. SMTP is the only channel that
// accepts one.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// Mail is one message under construction.
type Mail struct {
	to          []string
	from        string
	fromName    string
	subject     string
	body        string
	contentType string
	headers     map[string]string
	attachments []Attachment
	host        string
	port        int
	user        string
	password    string
}

// New starts a message. contentType is the body's, e.g.
// `text/html; charset="UTF-8"`.
func New(to []string, subject, body, contentType string) *Mail {
	return &Mail{
		to:          to,
		subject:     subject,
		body:        body,
		contentType: contentType,
		headers:     map[string]string{},
	}
}

func (m *Mail) SetFrom(name, email string) *Mail {
	m.fromName, m.from = name, email
	return m
}

func (m *Mail) SetHeader(key, value string) *Mail {
	m.headers[key] = value
	return m
}

func (m *Mail) SetCredentials(host string, port int, user, password string) *Mail {
	m.host, m.port, m.user, m.password = host, port, user, password
	return m
}

func (m *Mail) AddAttachment(attachment Attachment) *Mail {
	m.attachments = append(m.attachments, attachment)
	return m
}

func (m *Mail) buildMessage() ([]byte, error) {
	var buffer bytes.Buffer

	var header mail.Header
	header.SetDate(time.Now())
	header.SetSubject(m.subject)
	header.SetAddressList("From", []*mail.Address{{Name: m.fromName, Address: m.from}})

	recipients := make([]*mail.Address, len(m.to))
	for i, address := range m.to {
		recipients[i] = &mail.Address{Address: address}
	}
	header.SetAddressList("To", recipients)
	for key, value := range m.headers {
		header.Set(key, value)
	}

	writer, err := mail.CreateWriter(&buffer, header)
	if err != nil {
		return nil, err
	}

	var inline mail.InlineHeader
	inline.Set("Content-Type", m.contentType)
	body, err := writer.CreateSingleInline(inline)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(body, m.body); err != nil {
		return nil, err
	}
	if err := body.Close(); err != nil {
		return nil, err
	}

	for _, attachment := range m.attachments {
		if err := writeAttachment(writer, attachment); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeAttachment(writer *mail.Writer, attachment Attachment) error {
	var header mail.AttachmentHeader
	header.SetFilename(attachment.Filename)
	if attachment.ContentType != "" {
		header.Set("Content-Type", attachment.ContentType)
	}

	part, err := writer.CreateAttachment(header)
	if err != nil {
		return fmt.Errorf("create attachment %s: %w", attachment.Filename, err)
	}
	if _, err := part.Write(attachment.Content); err != nil {
		return fmt.Errorf("write attachment %s: %w", attachment.Filename, err)
	}
	if err := part.Close(); err != nil {
		return fmt.Errorf("close attachment %s: %w", attachment.Filename, err)
	}
	return nil
}

// Send delivers the message over conn.
func (m *Mail) Send(conn SMTPConnection) error {
	m.applyDefaults(conn)
	if m.host == "" {
		return fmt.Errorf("no SMTP host configured")
	}
	if len(m.to) == 0 {
		return fmt.Errorf("no recipients")
	}

	message, err := m.buildMessage()
	if err != nil {
		return err
	}

	client, err := m.dial(conn)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Errorf("failed to close SMTP client: %v", err)
		}
	}()

	if err := m.authenticate(client, conn); err != nil {
		return err
	}
	if properties.On(false, "smtp.debug") {
		client.DebugWriter = os.Stderr
	}
	return client.SendMail(m.from, m.to, bytes.NewReader(message))
}

func (m *Mail) dial(conn SMTPConnection) (*smtp.Client, error) {
	address := net.JoinHostPort(m.host, strconv.Itoa(m.port))
	tlsConfig := &tls.Config{ServerName: m.host, InsecureSkipVerify: conn.InsecureTLS} //nolint:gosec // opt-in

	switch conn.Encryption {
	case EncryptionImplicitTLS:
		return smtp.DialTLS(address, tlsConfig)
	case EncryptionExplicitTLS, EncryptionAuto:
		return smtp.DialStartTLS(address, tlsConfig)
	default:
		return smtp.Dial(address)
	}
}

func (m *Mail) authenticate(client *smtp.Client, conn SMTPConnection) error {
	switch conn.Auth {
	case AuthOAuth2:
		session := sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
			Username: m.user, Token: m.password, Host: m.host, Port: m.port,
		})
		if err := client.Auth(session); err != nil {
			return fmt.Errorf("authenticate with oauth bearer: %w", err)
		}
	case AuthPlain:
		// The identity parameter authenticates as one user while acting as
		// another, which is not something this ever needs.
		if err := client.Auth(sasl.NewPlainClient("", m.user, m.password)); err != nil {
			return fmt.Errorf("authenticate: %w", err)
		}
	case AuthNone:
	}
	return nil
}

// applyDefaults fills anything the caller did not set from the connection, then
// from the environment. Explicit credentials always win.
func (m *Mail) applyDefaults(conn SMTPConnection) {
	if m.host == "" {
		m.host, m.port = conn.Host, conn.Port
		m.user, m.password = conn.Username.ValueStatic, conn.Password.ValueStatic

		if m.host == "" {
			m.host = os.Getenv("SMTP_HOST")
			m.user = os.Getenv("SMTP_USER")
			m.password = os.Getenv("SMTP_PASSWORD")
			m.port, _ = strconv.Atoi(os.Getenv("SMTP_PORT"))
		}
		if m.port == 0 {
			m.port = 587
		}
	}
	if m.from == "" {
		m.from, m.fromName = conn.FromAddress, conn.FromName
	}
	if len(m.to) == 0 {
		m.to = conn.To
	}
}
