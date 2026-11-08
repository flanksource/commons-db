package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/flanksource/commons-db/models"
	queryschema "github.com/flanksource/commons-db/query/schema"
	"github.com/spf13/cobra"
)

func BuildConnectionFromFlags(flags *ConnectionFlags) (models.Connection, error) {
	conn := models.Connection{
		Name:        flags.Name,
		Namespace:   flags.Namespace,
		Type:        flags.Type,
		URL:         flags.URL,
		Username:    flags.Username,
		Password:    flags.Password,
		Certificate: flags.Certificate,
		InsecureTLS: flags.InsecureTLS,
	}

	props := make(map[string]string)

	switch flags.Type {
	case models.ConnectionTypeAWS:
		conn.URL = flags.URL
		conn.Username = flags.AccessKey
		conn.Password = flags.SecretKey
		props["region"] = flags.Region
		props["profile"] = flags.Profile

	case models.ConnectionTypeAWSKMS:
		conn.URL = flags.URL
		conn.Username = flags.AccessKey
		conn.Password = flags.SecretKey
		props["keyID"] = flags.KeyID
		props["region"] = flags.Region
		props["profile"] = flags.Profile

	case models.ConnectionTypeS3:
		conn.URL = flags.URL
		conn.Username = flags.AccessKey
		conn.Password = flags.SecretKey
		props["bucket"] = flags.Bucket
		props["region"] = flags.Region
		props["profile"] = flags.Profile
		props["usePathStyle"] = fmt.Sprintf("%t", flags.UsePathStyle)

	case models.ConnectionTypeAzure:
		conn.Username = flags.ClientID
		conn.Password = flags.ClientSecret
		props["tenant"] = flags.TenantID

	case models.ConnectionTypeAzureKeyVault:
		conn.Username = flags.ClientID
		conn.Password = flags.ClientSecret
		props["tenant"] = flags.TenantID
		props["keyID"] = flags.KeyID

	case models.ConnectionTypeAzureDevops:
		conn.URL = flags.URL
		conn.Username = flags.Organization
		conn.Password = flags.PersonalAccessToken

	case models.ConnectionTypeGCP:
		conn.URL = flags.URL
		conn.Certificate = flags.Certificate

	case models.ConnectionTypeGCS:
		conn.URL = flags.URL
		conn.Certificate = flags.Certificate
		props["bucket"] = flags.Bucket

	case models.ConnectionTypeGCPKMS:
		conn.URL = flags.URL
		conn.Certificate = flags.Certificate
		props["keyID"] = flags.KeyID

	case models.ConnectionTypePostgres:
		if flags.URL == "" && flags.Host != "" {
			conn.URL = fmt.Sprintf("postgres://$(username):$(password)@%s/%s", flags.Host, flags.Database)
			if flags.InsecureTLS {
				conn.URL += "?sslmode=disable"
			}
		}
		props["host"] = flags.Host
		props["database"] = flags.Database

	case models.ConnectionTypeMySQL:
		if flags.URL == "" && flags.Host != "" {
			conn.URL = fmt.Sprintf("mysql://$(username):$(password)@%s/%s", flags.Host, flags.Database)
		}
		props["host"] = flags.Host
		props["database"] = flags.Database

	case models.ConnectionTypeSQLServer:
		if flags.URL == "" && flags.Host != "" {
			conn.URL = fmt.Sprintf("sqlserver://$(username):$(password)@%s?database=%s", flags.Host, flags.Database)
			if flags.TrustServerCertificate {
				conn.URL += "&TrustServerCertificate=true"
			}
		}
		props["host"] = flags.Host
		props["database"] = flags.Database

	case models.ConnectionTypeMongo:
		if flags.URL == "" && flags.Host != "" {
			conn.URL = fmt.Sprintf("mongodb://$(username):$(password)@%s/%s", flags.Host, flags.Database)
			if flags.ReplicaSet != "" {
				conn.URL += "?replicaSet=" + flags.ReplicaSet
			}
		}
		props["host"] = flags.Host
		props["database"] = flags.Database
		props["replica_set"] = flags.ReplicaSet

	case models.ConnectionTypeSlack:
		conn.URL = "slack://$(password)@$(username)"
		conn.Username = flags.Channel
		conn.Password = flags.Token
		props["BotName"] = flags.BotName
		props["Color"] = flags.Color
		props["Icon"] = flags.Icon
		props["ThreadTS"] = flags.ThreadTS
		props["Title"] = flags.Title

	case models.ConnectionTypeDiscord:
		conn.URL = "discord://$(password)@$(username)"
		conn.Username = flags.WebhookID
		conn.Password = flags.Token

	case models.ConnectionTypeEmail:
		port := flags.Port
		if port == 0 {
			port = 587
		}
		conn.URL = fmt.Sprintf("smtp://$(username):$(password)@%s:%d/", flags.Host, port)
		props["port"] = fmt.Sprintf("%d", port)
		props["from"] = flags.FromAddress
		props["fromname"] = flags.FromName
		props["subject"] = flags.Subject
		props["auth"] = flags.Auth

	case models.ConnectionTypeTelegram:
		conn.URL = "telegram://$(password)@telegram/?Chats=$(username)"
		conn.Username = flags.Chats
		conn.Password = flags.Token

	case models.ConnectionTypeNtfy:
		conn.URL = fmt.Sprintf("ntfy://$(username):$(password)@%s/%s", flags.Host, flags.Topic)
		props["topic"] = flags.Topic

	case models.ConnectionTypePushbullet:
		conn.URL = "pushbullet://$(password)/"
		conn.Password = flags.Token

	case models.ConnectionTypePushover:
		conn.URL = "pushover://:$(password)@$(username)"
		conn.Username = flags.User
		conn.Password = flags.Token

	case models.ConnectionTypeHTTP:
		conn.InsecureTLS = flags.InsecureTLS
		props["insecure_tls"] = fmt.Sprintf("%t", flags.InsecureTLS)
		props["bearer"] = flags.Bearer
		props["clientID"] = flags.OAuthClientID
		props["clientSecret"] = flags.OAuthClientSecret
		props["tokenURL"] = flags.OAuthTokenURL
		props["scopes"] = flags.OAuthScopes
		props["ca"] = flags.TLSCA
		props["cert"] = flags.TLSCert
		props["key"] = flags.TLSKey

	case models.ConnectionTypeGit:
		props["ref"] = flags.Ref

	case models.ConnectionTypeGithub:
		conn.Password = flags.PersonalAccessToken

	case models.ConnectionTypeGitlab:
		conn.Password = flags.PersonalAccessToken

	case models.ConnectionTypeKubernetes:
		conn.Certificate = flags.Kubeconfig

	case models.ConnectionTypeFolder:
		props["path"] = flags.Path

	case models.ConnectionTypeSFTP:
		port := flags.Port
		if port == 0 {
			port = 22
		}
		props["path"] = flags.Path
		props["port"] = fmt.Sprintf("%d", port)

	case models.ConnectionTypeSMB:
		props["port"] = fmt.Sprintf("%d", flags.Port)
		props["share"] = flags.Share

	case models.ConnectionTypePrometheus:
		props["bearer"] = flags.Bearer

	case models.ConnectionTypeSQLite, models.ConnectionTypeClickHouse, models.ConnectionTypeOpenTelemetry, models.ConnectionTypeOpenSearch, models.ConnectionTypeJaeger, models.ConnectionTypeLoki:
		// URL, username, password already set

	case models.ConnectionTypeElasticSearch, models.ConnectionTypeRedis:
		conn.URL = flags.URL

	case models.ConnectionTypeFacet:
		conn.Password = flags.Token
		if flags.TimestampURL != "" {
			props["timestampUrl"] = flags.TimestampURL
		}

	case models.ConnectionTypeOpenAI, models.ConnectionTypeAnthropic, models.ConnectionTypeOllama, models.ConnectionTypeGemini:
		conn.Password = flags.ApiKey
		if flags.Model != "" {
			props["model"] = flags.Model
		}

	default:
		if !slices.Contains(queryschema.ConnectionTypes(), flags.Type) {
			return conn, fmt.Errorf("unsupported connection type: %s", flags.Type)
		}
	}

	filteredProps := make(map[string]string)
	if slices.Contains([]string{models.ConnectionTypeHTTP, models.ConnectionTypeOpenSearch, models.ConnectionTypeElasticSearch, models.ConnectionTypePrometheus, models.ConnectionTypeLoki, models.ConnectionTypeJaeger}, flags.Type) {
		props["authType"] = "none"
		if conn.Username != "" || conn.Password != "" {
			props["authType"] = "basic"
		}
		if flags.OAuthClientID != "" {
			props["authType"] = "oauth"
		}
		if flags.TLSCert != "" {
			props["authType"] = "mtls"
		}
	}
	for k, v := range props {
		if v != "" && v != "false" && v != "0" {
			filteredProps[k] = v
		}
	}
	if len(filteredProps) > 0 {
		conn.Properties = filteredProps
	}

	return conn, nil
}

func addTypeSpecificFlags(cmd *cobra.Command, flags *ConnectionFlags, connType string) {
	switch connType {
	case models.ConnectionTypeSlack:
		cmd.Flags().StringVar(&flags.Channel, "channel", "", "Slack channel ID (required)")
		cmd.Flags().StringVar(&flags.Token, "token", "", "Slack bot token (required)")
		cmd.Flags().StringVar(&flags.BotName, "bot-name", "", "Slack bot name")
		cmd.Flags().StringVar(&flags.Color, "color", "", "Message color")
		cmd.Flags().StringVar(&flags.Icon, "icon", "", "Bot icon")
		cmd.Flags().StringVar(&flags.ThreadTS, "thread-ts", "", "Thread timestamp")
		cmd.Flags().StringVar(&flags.Title, "title", "", "Message title")

	case models.ConnectionTypeAWS:
		cmd.Flags().StringVar(&flags.URL, "url", "", "AWS endpoint URL")
		cmd.Flags().StringVar(&flags.AccessKey, "access-key", "", "AWS access key")
		cmd.Flags().StringVar(&flags.SecretKey, "secret-key", "", "AWS secret key")
		cmd.Flags().StringVar(&flags.Region, "region", "", "AWS region")
		cmd.Flags().StringVar(&flags.Profile, "profile", "", "AWS profile")

	case models.ConnectionTypeAWSKMS:
		cmd.Flags().StringVar(&flags.URL, "url", "", "AWS endpoint URL")
		cmd.Flags().StringVar(&flags.AccessKey, "access-key", "", "AWS access key")
		cmd.Flags().StringVar(&flags.SecretKey, "secret-key", "", "AWS secret key")
		cmd.Flags().StringVar(&flags.Region, "region", "", "AWS region")
		cmd.Flags().StringVar(&flags.Profile, "profile", "", "AWS profile")
		cmd.Flags().StringVar(&flags.KeyID, "key-id", "", "KMS key ID")

	case models.ConnectionTypeS3:
		cmd.Flags().StringVar(&flags.URL, "url", "", "S3 endpoint URL")
		cmd.Flags().StringVar(&flags.AccessKey, "access-key", "", "AWS access key")
		cmd.Flags().StringVar(&flags.SecretKey, "secret-key", "", "AWS secret key")
		cmd.Flags().StringVar(&flags.Region, "region", "", "AWS region")
		cmd.Flags().StringVar(&flags.Profile, "profile", "", "AWS profile")
		cmd.Flags().StringVar(&flags.Bucket, "bucket", "", "S3 bucket name")
		cmd.Flags().BoolVar(&flags.UsePathStyle, "use-path-style", false, "Use path-style S3 URLs")

	case models.ConnectionTypeAzure:
		cmd.Flags().StringVar(&flags.ClientID, "client-id", "", "Azure client ID")
		cmd.Flags().StringVar(&flags.ClientSecret, "client-secret", "", "Azure client secret")
		cmd.Flags().StringVar(&flags.TenantID, "tenant-id", "", "Azure tenant ID")

	case models.ConnectionTypeAzureKeyVault:
		cmd.Flags().StringVar(&flags.ClientID, "client-id", "", "Azure client ID")
		cmd.Flags().StringVar(&flags.ClientSecret, "client-secret", "", "Azure client secret")
		cmd.Flags().StringVar(&flags.TenantID, "tenant-id", "", "Azure tenant ID")
		cmd.Flags().StringVar(&flags.KeyID, "key-id", "", "Key Vault key ID")

	case models.ConnectionTypeAzureDevops:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Azure DevOps URL")
		cmd.Flags().StringVar(&flags.Organization, "organization", "", "Azure DevOps organization")
		cmd.Flags().StringVar(&flags.PersonalAccessToken, "personal-access-token", "", "Personal access token")

	case models.ConnectionTypeGCP:
		cmd.Flags().StringVar(&flags.URL, "url", "", "GCP endpoint URL")
		cmd.Flags().StringVar(&flags.Certificate, "certificate", "", "Service account credentials JSON")

	case models.ConnectionTypeGCS:
		cmd.Flags().StringVar(&flags.URL, "url", "", "GCS endpoint URL")
		cmd.Flags().StringVar(&flags.Certificate, "certificate", "", "Service account credentials JSON")
		cmd.Flags().StringVar(&flags.Bucket, "bucket", "", "GCS bucket name")

	case models.ConnectionTypeGCPKMS:
		cmd.Flags().StringVar(&flags.URL, "url", "", "GCP endpoint URL")
		cmd.Flags().StringVar(&flags.Certificate, "certificate", "", "Service account credentials JSON")
		cmd.Flags().StringVar(&flags.KeyID, "key-id", "", "KMS key ID")

	case models.ConnectionTypePostgres:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Postgres connection URL")
		cmd.Flags().StringVar(&flags.Host, "host", "", "Database host")
		cmd.Flags().StringVar(&flags.Database, "database", "", "Database name")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Database username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Database password")
		cmd.Flags().BoolVar(&flags.InsecureTLS, "insecure-tls", false, "Skip TLS verification")

	case models.ConnectionTypeMySQL:
		cmd.Flags().StringVar(&flags.URL, "url", "", "MySQL connection URL")
		cmd.Flags().StringVar(&flags.Host, "host", "", "Database host")
		cmd.Flags().StringVar(&flags.Database, "database", "", "Database name")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Database username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Database password")

	case models.ConnectionTypeSQLServer:
		cmd.Flags().StringVar(&flags.URL, "url", "", "SQL Server connection URL")
		cmd.Flags().StringVar(&flags.Host, "host", "", "Database host")
		cmd.Flags().StringVar(&flags.Database, "database", "", "Database name")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Database username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Database password")
		cmd.Flags().BoolVar(&flags.TrustServerCertificate, "trust-server-certificate", false, "Trust server certificate")

	case models.ConnectionTypeMongo:
		cmd.Flags().StringVar(&flags.URL, "url", "", "MongoDB connection URL")
		cmd.Flags().StringVar(&flags.Host, "host", "", "Database host")
		cmd.Flags().StringVar(&flags.Database, "database", "", "Database name")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Database username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Database password")
		cmd.Flags().StringVar(&flags.ReplicaSet, "replica-set", "", "MongoDB replica set")

	case models.ConnectionTypeDiscord:
		cmd.Flags().StringVar(&flags.WebhookID, "webhook-id", "", "Discord webhook ID")
		cmd.Flags().StringVar(&flags.Token, "token", "", "Discord webhook token")

	case models.ConnectionTypeEmail:
		cmd.Flags().StringVar(&flags.Host, "host", "", "SMTP host")
		cmd.Flags().IntVar(&flags.Port, "port", 587, "SMTP port")
		cmd.Flags().StringVar(&flags.Username, "username", "", "SMTP username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "SMTP password")
		cmd.Flags().StringVar(&flags.FromAddress, "from-address", "", "From email address")
		cmd.Flags().StringVar(&flags.FromName, "from-name", "", "From name")
		cmd.Flags().StringVar(&flags.Subject, "subject", "", "Email subject")
		cmd.Flags().StringVar(&flags.Auth, "auth", "", "SMTP auth method")
		cmd.Flags().BoolVar(&flags.InsecureTLS, "insecure-tls", false, "Skip TLS verification")

	case models.ConnectionTypeTelegram:
		cmd.Flags().StringVar(&flags.Token, "token", "", "Telegram bot token")
		cmd.Flags().StringVar(&flags.Chats, "chats", "", "Telegram chat IDs")

	case models.ConnectionTypeNtfy:
		cmd.Flags().StringVar(&flags.Host, "host", "", "Ntfy server host")
		cmd.Flags().StringVar(&flags.Topic, "topic", "", "Ntfy topic")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Ntfy username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Ntfy password")

	case models.ConnectionTypePushbullet:
		cmd.Flags().StringVar(&flags.Token, "token", "", "Pushbullet access token")

	case models.ConnectionTypePushover:
		cmd.Flags().StringVar(&flags.Token, "token", "", "Pushover API token")
		cmd.Flags().StringVar(&flags.User, "user", "", "Pushover user key")

	case models.ConnectionTypeHTTP:
		cmd.Flags().StringVar(&flags.URL, "url", "", "HTTP URL")
		cmd.Flags().StringVar(&flags.Username, "username", "", "HTTP basic auth username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "HTTP basic auth password")
		cmd.Flags().BoolVar(&flags.InsecureTLS, "insecure-tls", false, "Skip TLS verification")
		cmd.Flags().StringVar(&flags.Bearer, "bearer", "", "Bearer token")
		cmd.Flags().StringVar(&flags.OAuthClientID, "oauth-client-id", "", "OAuth client ID")
		cmd.Flags().StringVar(&flags.OAuthClientSecret, "oauth-client-secret", "", "OAuth client secret")
		cmd.Flags().StringVar(&flags.OAuthTokenURL, "oauth-token-url", "", "OAuth token URL")
		cmd.Flags().StringVar(&flags.OAuthScopes, "oauth-scopes", "", "OAuth scopes (comma-separated)")
		cmd.Flags().StringVar(&flags.TLSCA, "tls-ca", "", "PEM encoded CA certificate")
		cmd.Flags().StringVar(&flags.TLSCert, "tls-cert", "", "PEM encoded client certificate")
		cmd.Flags().StringVar(&flags.TLSKey, "tls-key", "", "PEM encoded client private key")

	case models.ConnectionTypeGit:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Git repository URL")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Git username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Git password/token")
		cmd.Flags().StringVar(&flags.Ref, "ref", "", "Git reference (branch/tag)")
		cmd.Flags().StringVar(&flags.Certificate, "certificate", "", "SSH private key")

	case models.ConnectionTypeGithub:
		cmd.Flags().StringVar(&flags.URL, "url", "", "GitHub URL")
		cmd.Flags().StringVar(&flags.PersonalAccessToken, "personal-access-token", "", "GitHub personal access token")

	case models.ConnectionTypeGitlab:
		cmd.Flags().StringVar(&flags.URL, "url", "", "GitLab URL")
		cmd.Flags().StringVar(&flags.PersonalAccessToken, "personal-access-token", "", "GitLab personal access token")

	case models.ConnectionTypeKubernetes:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Kubernetes API URL")
		cmd.Flags().StringVar(&flags.Kubeconfig, "kubeconfig", kubeconfigDefault(), "Path to kubeconfig file or raw kubeconfig content")

	case models.ConnectionTypeFolder:
		cmd.Flags().StringVar(&flags.Path, "path", "", "Folder path")

	case models.ConnectionTypeSFTP:
		cmd.Flags().StringVar(&flags.Host, "host", "", "SFTP host")
		cmd.Flags().IntVar(&flags.Port, "port", 22, "SFTP port")
		cmd.Flags().StringVar(&flags.Username, "username", "", "SFTP username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "SFTP password")
		cmd.Flags().StringVar(&flags.Path, "path", "", "SFTP path")
		cmd.Flags().StringVar(&flags.Certificate, "certificate", "", "SSH private key")

	case models.ConnectionTypeSMB:
		cmd.Flags().StringVar(&flags.Host, "host", "", "SMB host")
		cmd.Flags().IntVar(&flags.Port, "port", 445, "SMB port")
		cmd.Flags().StringVar(&flags.Username, "username", "", "SMB username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "SMB password")
		cmd.Flags().StringVar(&flags.Share, "share", "", "SMB share name")

	case models.ConnectionTypePrometheus:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Prometheus URL")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Basic auth username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Basic auth password")
		cmd.Flags().StringVar(&flags.Bearer, "bearer", "", "Bearer token")

	case models.ConnectionTypeSQLite, models.ConnectionTypeClickHouse, models.ConnectionTypeOpenTelemetry, models.ConnectionTypeOpenSearch, models.ConnectionTypeJaeger, models.ConnectionTypeLoki:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Loki URL")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Basic auth username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Basic auth password")

	case models.ConnectionTypeElasticSearch:
		cmd.Flags().StringVar(&flags.URL, "url", "", "ElasticSearch URL")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Basic auth username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Basic auth password")
		cmd.Flags().BoolVar(&flags.InsecureTLS, "insecure-tls", false, "Skip TLS verification")

	case models.ConnectionTypeRedis:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Redis address")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Redis username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Redis password")

	case models.ConnectionTypeFacet:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Facet service URL (required)")
		cmd.Flags().StringVar(&flags.Token, "token", "", "API key for facet service")
		cmd.Flags().StringVar(&flags.TimestampURL, "timestamp-url", "", "RFC 3161 timestamp authority URL")

	case models.ConnectionTypeOpenAI, models.ConnectionTypeAnthropic, models.ConnectionTypeOllama, models.ConnectionTypeGemini:
		cmd.Flags().StringVar(&flags.URL, "url", "", "API base URL")
		cmd.Flags().StringVar(&flags.ApiKey, "api-key", "", "API key")
		cmd.Flags().StringVar(&flags.Model, "model", "", "Model name")
	default:
		cmd.Flags().StringVar(&flags.URL, "url", "", "Connection URL")
		cmd.Flags().StringVar(&flags.Username, "username", "", "Connection username")
		cmd.Flags().StringVar(&flags.Password, "password", "", "Connection password")
		cmd.Flags().StringVar(&flags.Certificate, "certificate", "", "Connection certificate")
		cmd.Flags().BoolVar(&flags.InsecureTLS, "insecure-tls", false, "Skip TLS verification")
	}
}

type ConnectionFlags struct {
	Name      string
	Namespace string
	Type      string
	Test      bool
	DryRun    bool

	// Common fields
	URL         string
	Username    string
	Password    string
	Certificate string
	InsecureTLS bool

	// Kubernetes
	Kubeconfig string

	// AWS
	AccessKey    string
	SecretKey    string
	Region       string
	Profile      string
	FromProfile  string
	SessionToken string

	// AWS KMS / GCP KMS / Azure Key Vault
	KeyID string

	// S3 / GCS
	Bucket       string
	UsePathStyle bool

	// Azure
	ClientID     string
	ClientSecret string
	TenantID     string

	// Azure DevOps
	Organization        string
	PersonalAccessToken string

	// Git
	Ref string

	// Slack
	Channel  string
	Token    string
	BotName  string
	Color    string
	Icon     string
	ThreadTS string
	Title    string

	// SMTP
	Host        string
	Port        int
	FromAddress string
	FromName    string
	ToAddresses []string
	Subject     string
	Auth        string
	Encryption  string

	// Telegram / Ntfy
	Chats string
	Topic string

	// Pushbullet / Pushover
	Targets []string
	User    string

	// Discord
	WebhookID string

	// SFTP / SMB
	Path  string
	Share string

	// Mongo
	ReplicaSet string

	// MSSQL
	TrustServerCertificate bool

	// Database common
	Database string

	// HTTP / Prometheus auth
	Bearer            string
	OAuthClientID     string
	OAuthClientSecret string
	OAuthTokenURL     string
	OAuthScopes       string
	TLSCA             string
	TLSCert           string
	TLSKey            string

	// Facet
	TimestampURL string

	// AI models
	Model  string
	ApiKey string

	// Properties (generic key=value pairs)
	Properties []string
}

func kubeconfigDefault() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}

func validateConnectionFlags(flags *ConnectionFlags) error {
	switch flags.Type {
	case models.ConnectionTypeSlack:
		if flags.Token == "" {
			return fmt.Errorf("--token is required for Slack connections")
		}
		if flags.Channel == "" {
			return fmt.Errorf("--channel is required for Slack connections")
		}

	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeMongo:
		if flags.URL == "" && flags.Host == "" {
			return fmt.Errorf("--url or --host is required for database connections")
		}

	case models.ConnectionTypeAWS, models.ConnectionTypeAWSKMS, models.ConnectionTypeS3:
		// AWS can use instance profiles, so credentials are optional

	case models.ConnectionTypeAzure, models.ConnectionTypeAzureKeyVault:
		if flags.ClientID == "" || flags.ClientSecret == "" || flags.TenantID == "" {
			return fmt.Errorf("--client-id, --client-secret, and --tenant-id are required for Azure connections")
		}

	case models.ConnectionTypeAzureDevops:
		if flags.PersonalAccessToken == "" {
			return fmt.Errorf("--personal-access-token is required for Azure DevOps connections")
		}

	case models.ConnectionTypeGCP, models.ConnectionTypeGCS, models.ConnectionTypeGCPKMS:
		// GCP can use default credentials, so certificate is optional

	case models.ConnectionTypeDiscord:
		if flags.WebhookID == "" || flags.Token == "" {
			return fmt.Errorf("--webhook-id and --token are required for Discord connections")
		}

	case models.ConnectionTypeEmail:
		if flags.Host == "" {
			return fmt.Errorf("--host is required for SMTP connections")
		}

	case models.ConnectionTypeTelegram:
		if flags.Token == "" {
			return fmt.Errorf("--token is required for Telegram connections")
		}

	case models.ConnectionTypeNtfy:
		if flags.Host == "" || flags.Topic == "" {
			return fmt.Errorf("--host and --topic are required for Ntfy connections")
		}

	case models.ConnectionTypePushbullet:
		if flags.Token == "" {
			return fmt.Errorf("--token is required for Pushbullet connections")
		}

	case models.ConnectionTypePushover:
		if flags.Token == "" || flags.User == "" {
			return fmt.Errorf("--token and --user are required for Pushover connections")
		}

	case models.ConnectionTypeHTTP:
		if flags.URL == "" {
			return fmt.Errorf("--url is required for HTTP connections")
		}

	case models.ConnectionTypeGit:
		if flags.URL == "" {
			return fmt.Errorf("--url is required for Git connections")
		}

	case models.ConnectionTypeGithub, models.ConnectionTypeGitlab:
		if flags.PersonalAccessToken == "" {
			return fmt.Errorf("--personal-access-token is required for GitHub/GitLab connections")
		}

	case models.ConnectionTypeFolder:
		if flags.Path == "" {
			return fmt.Errorf("--path is required for folder connections")
		}

	case models.ConnectionTypeSFTP:
		if flags.Host == "" {
			return fmt.Errorf("--host is required for SFTP connections")
		}

	case models.ConnectionTypeSMB:
		if flags.Host == "" || flags.Share == "" {
			return fmt.Errorf("--host and --share are required for SMB connections")
		}

	case models.ConnectionTypePrometheus, models.ConnectionTypeLoki, models.ConnectionTypeElasticSearch, models.ConnectionTypeRedis:
		if flags.URL == "" {
			return fmt.Errorf("--url is required for %s connections", flags.Type)
		}

	case models.ConnectionTypeOpenAI, models.ConnectionTypeAnthropic, models.ConnectionTypeGemini:
		if flags.ApiKey == "" {
			return fmt.Errorf("--api-key is required for %s connections", flags.Type)
		}

	case models.ConnectionTypeOllama:
		if flags.URL == "" {
			return fmt.Errorf("--url is required for Ollama connections")
		}

	case models.ConnectionTypeFacet:
		if flags.URL == "" {
			return fmt.Errorf("--url is required for Facet connections")
		}
	}

	return nil
}
