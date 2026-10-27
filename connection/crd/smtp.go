package crd

import "github.com/flanksource/commons-db/types"

type SMTPTLS string

const (
	EncryptionNone        SMTPTLS = "None"
	EncryptionExplicitTLS SMTPTLS = "ExplicitTLS"
	EncryptionImplicitTLS SMTPTLS = "ImplicitTLS"
	EncryptionAuto        SMTPTLS = "Auto"
)

type SMTPAuth string

const (
	SMTPAuthNone   SMTPAuth = "none"
	SMTPAuthPlain  SMTPAuth = "plain"
	SMTPAuthOAuth2 SMTPAuth = "oauth2"
)

type ConnectionSMTP struct {
	Host     string       `json:"host"`
	Username types.EnvVar `json:"username,omitempty"`
	Password types.EnvVar `json:"password,omitempty"`

	// Default: false
	InsecureTLS bool `json:"insecureTLS,omitempty"`

	// Encryption Method
	// 	Default: auto
	// 	Possible values: None, ExplicitTLS, ImplicitTLS, Auto
	Encryption SMTPTLS `json:"encryption,omitempty"`

	// SMTP server port
	// 	Default: 587
	Port int `json:"port,omitempty"`

	// Email address that the mail are sent from
	FromAddress string `json:"fromAddress"`

	// Name that the mail are sent from
	FromName string `json:"fromName"`

	// List of recipient e-mails
	ToAddresses []string `json:"toAddresses,omitempty"`

	// The subject of the sent mail
	Subject string `json:"subject,omitempty"`

	// Auth - SMTP authentication method
	// Possible values: none, plain, oauth2
	Auth SMTPAuth `json:"auth,omitempty"`

	// Headers for SMTP Server
	Headers map[string]string `json:"headers,omitempty"`
}
