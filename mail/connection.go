// Package mail sends SMTP mail. It is the one notification channel that is not
// a webhook, and the only one that can carry an attachment.
package mail

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

// Encryption selects how the SMTP connection is secured.
type Encryption string

const (
	// EncryptionAuto upgrades with STARTTLS, the same as explicit TLS.
	EncryptionAuto Encryption = "Auto"
	// EncryptionNone sends in the clear.
	EncryptionNone Encryption = "None"
	// EncryptionExplicitTLS connects in the clear then issues STARTTLS.
	EncryptionExplicitTLS Encryption = "ExplicitTLS"
	// EncryptionImplicitTLS connects with TLS from the first byte.
	EncryptionImplicitTLS Encryption = "ImplicitTLS"
)

// Auth selects how the SMTP session authenticates.
type Auth string

const (
	AuthNone   Auth = "None"
	AuthPlain  Auth = "Plain"
	AuthOAuth2 Auth = "OAuth2"
)

// SMTPConnection is a mail server. It is derived from a models.Connection of
// type email, or parsed from an smtp:// URL.
type SMTPConnection struct {
	Host        string       `json:"host,omitempty" yaml:"host,omitempty"`
	Port        int          `json:"port,omitempty" yaml:"port,omitempty"`
	Username    types.EnvVar `json:"username,omitempty" yaml:"username,omitempty"`
	Password    types.EnvVar `json:"password,omitempty" yaml:"password,omitempty"`
	FromAddress string       `json:"fromAddress,omitempty" yaml:"fromAddress,omitempty"`
	FromName    string       `json:"fromName,omitempty" yaml:"fromName,omitempty"`
	Encryption  Encryption   `json:"encryption,omitempty" yaml:"encryption,omitempty"`
	Auth        Auth         `json:"auth,omitempty" yaml:"auth,omitempty"`
	InsecureTLS bool         `json:"insecureTLS,omitempty" yaml:"insecureTLS,omitempty"`

	// To is the default recipient list, used when a message names none.
	To []string `json:"to,omitempty" yaml:"to,omitempty"`
}

// FromConnection projects a stored connection onto an SMTP connection. The
// connection must already be hydrated: this reads resolved values, it does not
// resolve secrets itself.
func FromConnection(conn *models.Connection) (SMTPConnection, error) {
	if conn == nil {
		return SMTPConnection{}, fmt.Errorf("connection is required")
	}
	if conn.Type != models.ConnectionTypeEmail {
		return SMTPConnection{}, fmt.Errorf("connection %q is type %q, expected %q",
			conn.Name, conn.Type, models.ConnectionTypeEmail)
	}

	smtp := SMTPConnection{
		Username: types.EnvVar{ValueStatic: conn.Username},
		Password: types.EnvVar{ValueStatic: conn.Password},
	}
	// The URL carries host and port; the properties carry everything SMTP needs
	// that a generic connection has no column for.
	if conn.URL != "" {
		if err := smtp.parseHostPort(conn.URL); err != nil {
			return SMTPConnection{}, err
		}
	}
	smtp.applyProperties(conn.Properties)
	smtp.applyDefaults()
	return smtp, nil
}

// FromURL parses an smtp:// URL, including the query parameters the shoutrrr
// URL form used, so URLs already written against the old form keep working.
func FromURL(raw string) (SMTPConnection, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return SMTPConnection{}, fmt.Errorf("parse SMTP url: %w", err)
	}

	smtp := SMTPConnection{Host: parsed.Hostname()}
	if port := parsed.Port(); port != "" {
		if smtp.Port, err = strconv.Atoi(port); err != nil {
			return SMTPConnection{}, fmt.Errorf("invalid SMTP port %q: %w", port, err)
		}
	}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		smtp.Username = types.EnvVar{ValueStatic: parsed.User.Username()}
		smtp.Password = types.EnvVar{ValueStatic: password}
	}

	query := parsed.Query()
	smtp.FromAddress = firstNonEmpty(query, "FromAddress", "fromAddress", "from")
	smtp.FromName = firstNonEmpty(query, "FromName", "fromName")
	if to := firstNonEmpty(query, "ToAddresses", "ToAddress", "to"); to != "" {
		smtp.To = splitList(to)
	}
	if encryption := firstNonEmpty(query, "Encryption", "encryption"); encryption != "" {
		smtp.Encryption = Encryption(encryption)
	}
	if auth := firstNonEmpty(query, "Auth", "auth"); auth != "" {
		smtp.Auth = Auth(auth)
	}
	smtp.applyDefaults()
	return smtp, nil
}

func (c *SMTPConnection) parseHostPort(raw string) error {
	if !strings.Contains(raw, "://") {
		raw = "smtp://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse SMTP url %q: %w", raw, err)
	}
	c.Host = parsed.Hostname()
	if port := parsed.Port(); port != "" {
		if c.Port, err = strconv.Atoi(port); err != nil {
			return fmt.Errorf("invalid SMTP port %q: %w", port, err)
		}
	}
	return nil
}

func (c *SMTPConnection) applyProperties(properties map[string]string) {
	if properties == nil {
		return
	}
	if host := properties["host"]; host != "" {
		c.Host = host
	}
	if port := properties["port"]; port != "" {
		if parsed, err := strconv.Atoi(port); err == nil {
			c.Port = parsed
		}
	}
	if from := properties["fromAddress"]; from != "" {
		c.FromAddress = from
	}
	if name := properties["fromName"]; name != "" {
		c.FromName = name
	}
	if to := properties["to"]; to != "" {
		c.To = splitList(to)
	}
	if encryption := properties["encryption"]; encryption != "" {
		c.Encryption = Encryption(encryption)
	}
	if auth := properties["auth"]; auth != "" {
		c.Auth = Auth(auth)
	}
	if properties["insecureTLS"] == "true" {
		c.InsecureTLS = true
	}
}

// applyDefaults fills in the conventional SMTP submission port and the auth
// mode implied by having credentials at all.
func (c *SMTPConnection) applyDefaults() {
	if c.Port == 0 {
		c.Port = 587
	}
	if c.Encryption == "" {
		c.Encryption = EncryptionAuto
	}
	if c.Auth == "" {
		if c.Username.ValueStatic != "" || c.Password.ValueStatic != "" {
			c.Auth = AuthPlain
		} else {
			c.Auth = AuthNone
		}
	}
	if c.FromAddress == "" {
		c.FromAddress = c.Username.ValueStatic
	}
}

func firstNonEmpty(query url.Values, keys ...string) string {
	for _, key := range keys {
		if value := query.Get(key); value != "" {
			return value
		}
	}
	return ""
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
