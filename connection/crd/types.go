// Package crd is the typed mission-control Connection resource
// (mission-control.flanksource.com/v1, kind Connection) and its conversion to
// models.Connection, so a connection written for mission-control is read the
// same way wherever commons-db resolves connections.
//
// Ported from incident-commander 1c0236bb: api/v1/connection_types.go,
// api/v1/connection_smtp.go, db/connections.go (ConnectionFromCRD) and
// db/connection_crd_convert.go, with duty's connection.OpensearchConnection.
// The kopper status and controller methods are left out: nothing here
// reconciles a resource.
package crd

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/types"
)

// APIVersion and Kind identify a Connection resource.
const (
	APIVersion = "mission-control.flanksource.com/v1"
	Kind       = "Connection"
)

type ConnectionTelegram struct {
	Token types.EnvVar `json:"token"`

	// Chat ID or Channel name (using @channel-name)
	Chats types.EnvVar `json:"chats"`
}

type ConnectionSlack struct {
	Token types.EnvVar `json:"token,omitempty"`

	// Channel to send messages to in Cxxxxxxxxxx format
	Channel string `json:"channel,omitempty"`

	BotName string `json:"botName,omitempty"`

	// good, warning, danger, or any URL encoded hex color code
	Color string `json:"color,omitempty"`

	// emoji or URL
	Icon string `json:"icon,omitempty"`

	// ts value of the parent message (to send message as reply in thread)
	ThreadTS string `json:"thread_ts,omitempty"`

	// Prepended text above the message
	Title string `json:"title,omitempty"`
}

type ConnectionNtfy struct {
	Topic string `json:"topic"`

	Host     string       `json:"host"`
	Username types.EnvVar `json:"username"`
	Password types.EnvVar `json:"password"`
}

type ConnectionDiscord struct {
	Token     string `json:"token"`
	WebhookID string `json:"webhookID"`
}

type ConnectionPushbullet struct {
	Token types.EnvVar `json:"token"`

	Targets []string `json:"targets"`
}

type ConnectionPushover struct {
	Token types.EnvVar `json:"token"`
	// User key
	User string `json:"user"`
}

type ConnectionPostgres struct {
	// URL is the connection url.
	URL types.EnvVar `json:"url,omitempty"`

	// <host:port>
	Host        types.EnvVar `json:"host,omitempty"`
	Username    types.EnvVar `json:"username,omitempty"`
	Password    types.EnvVar `json:"password,omitempty"`
	Database    types.EnvVar `json:"database,omitempty"`
	InsecureTLS bool         `json:"insecureTLS,omitempty"`
}

type ConnectionMySQL struct {
	// URL is the connection url.
	URL types.EnvVar `json:"url,omitempty"`

	// <host:port>
	Host        types.EnvVar `json:"host,omitempty"`
	Username    types.EnvVar `json:"username,omitempty"`
	Password    types.EnvVar `json:"password,omitempty"`
	Database    types.EnvVar `json:"database,omitempty"`
	InsecureTLS bool         `json:"insecureTLS,omitempty"`
}

type ConnectionMSSQL struct {
	// URL is the connection url.
	URL types.EnvVar `json:"url,omitempty"`

	// <host:port>
	Host                   types.EnvVar `json:"host,omitempty"`
	Username               types.EnvVar `json:"username,omitempty"`
	Password               types.EnvVar `json:"password,omitempty"`
	Database               types.EnvVar `json:"database,omitempty"`
	TrustServerCertificate *bool        `json:"trustServerCertificate,omitempty"`
}

type ConnectionMongo struct {
	// URL is the connection url.
	URL types.EnvVar `json:"url,omitempty"`

	// <host:port>
	Host        types.EnvVar `json:"host,omitempty"`
	Username    types.EnvVar `json:"username,omitempty"`
	Password    types.EnvVar `json:"password,omitempty"`
	Database    types.EnvVar `json:"database,omitempty"`
	ReplicaSet  string       `json:"replicaSet,omitempty"`
	InsecureTLS bool         `json:"insecureTLS,omitempty"`
}

type ConnectionAWSS3 struct {
	ConnectionAWS `json:",inline"`
	Bucket        string `json:"bucket"`
	// Use path style path: http://s3.amazonaws.com/BUCKET/KEY instead of http://BUCKET.s3.amazonaws.com/KEY
	UsePathStyle bool `yaml:"usePathStyle,omitempty" json:"usePathStyle,omitempty"`
}

type ConnectionAWS struct {
	// AWS Endpoint
	URL          types.EnvVar `json:"url,omitempty"`
	Region       string       `json:"region,omitempty"`
	Profile      string       `json:"profile,omitempty"`
	InsecureTLS  bool         `json:"insecureTLS,omitempty"`
	AccessKey    types.EnvVar `json:"accessKey,omitempty"`
	SecretKey    types.EnvVar `json:"secretKey,omitempty"`
	SessionToken types.EnvVar `json:"sessionToken,omitempty"`
	AssumeRole   string       `json:"assumeRole,omitempty"`
}

type ConnectionAWSKMS struct {
	ConnectionAWS `json:",inline"`

	// keyID can be an alias (eg: alias/ExampleAlias?region=us-east-1) or the ARN
	KeyID string `json:"keyID"`
}

type ConnectionAzure struct {
	ClientID     types.EnvVar `json:"clientID"`
	ClientSecret types.EnvVar `json:"clientSecret,omitempty"`
	TenantID     types.EnvVar `json:"tenantID"`
}

type ConnectionAzureKeyVault struct {
	ConnectionAzure `json:",inline"`

	// keyID is a URL to the key in the format
	// 	https://<vault-name>.vault.azure.net/keys/<key-name>
	KeyID string `json:"keyID"`
}

type ConnectionAzureDevops struct {
	URL                 string       `json:"url,omitempty"`
	Organization        string       `json:"organization"`
	PersonalAccessToken types.EnvVar `json:"personalAccessToken"`
}

type ConnectionGCP struct {
	Endpoint    types.EnvVar `json:"endpoint,omitempty"`
	Certificate types.EnvVar `json:"certificate,omitempty"`
	Project     string       `json:"project,omitempty"`
}

type ConnectionGCS struct {
	ConnectionGCP `json:",inline"`
	Bucket        string `json:"bucket"`
}

type ConnectionLoki struct {
	URL      string       `json:"url"`
	Username types.EnvVar `json:"username,omitempty"`
	Password types.EnvVar `json:"password,omitempty"`
}

type ConnectionGCPKMS struct {
	ConnectionGCP `json:",inline"`

	// keyID points to the key in the format
	// projects/MYPROJECT/locations/MYLOCATION/keyRings/MYKEYRING/cryptoKeys/MYKEY
	KeyID string `json:"keyID"`
}

type ConnectionFolder struct {
	Path string `json:"path"`
}

type ConnectionPrometheus struct {
	URL                  types.EnvVar `json:"url,omitempty"`
	types.Authentication `json:",inline"`
	Bearer               types.EnvVar         `json:"bearer,omitempty" yaml:"bearer,omitempty"`
	OAuth                types.OAuth          `json:"oauth,omitempty" yaml:"oauth,omitempty"`
	TLS                  connection.TLSConfig `json:"tls,omitempty" yaml:"tls,omitempty"`
}

type ConnectionKubernetes struct {
	Certificate types.EnvVar `json:"certificate"`
}

type ConnectionGitHub struct {
	URL                 string       `json:"url,omitempty"`
	PersonalAccessToken types.EnvVar `json:"personalAccessToken"`
}

type ConnectionGitLab struct {
	URL                 string       `json:"url,omitempty"`
	PersonalAccessToken types.EnvVar `json:"personalAccessToken"`
}

type ConnectionGit struct {
	URL         string        `json:"url"`
	Ref         string        `json:"ref"`
	Certificate *types.EnvVar `json:"certificate,omitempty"`
	Username    *types.EnvVar `json:"username,omitempty"`
	Password    *types.EnvVar `json:"password,omitempty"`
}

type ConnectionAWSSigV4 struct {
	ConnectionAWS `json:",inline"`
	Service       string `json:"service,omitempty"`
}

type ConnectionHTTP struct {
	URL         string               `json:"url"`
	InsecureTLS bool                 `json:"insecureTLS,omitempty"`
	Username    *types.EnvVar        `json:"username,omitempty"`
	Password    *types.EnvVar        `json:"password,omitempty"`
	Bearer      types.EnvVar         `json:"bearer,omitempty" yaml:"bearer,omitempty"`
	OAuth       types.OAuth          `json:"oauth,omitempty" yaml:"oauth,omitempty"`
	TLS         connection.TLSConfig `json:"tls,omitempty" yaml:"tls,omitempty"`
	Headers     []types.EnvVar       `json:"headers,omitempty"`
	AWSSigV4    *ConnectionAWSSigV4  `json:"awsSigV4,omitempty"`
}

type ConnectionSFTP struct {
	Host     types.EnvVar `json:"host"`
	Username types.EnvVar `json:"username"`
	Password types.EnvVar `json:"password"`

	// SMTP server port
	// 	Default: 22
	Port int `json:"port,omitempty"`

	Path string `json:"path"`
}

type ConnectionSMB struct {
	Server   types.EnvVar `json:"server"`
	Username types.EnvVar `json:"username"`
	Password types.EnvVar `json:"password"`

	// SMB server port
	// 	Default: 445
	Port types.EnvVar `json:"port,omitempty"`

	Share string `json:"share"`
}

type ConnectionOpenAI struct {
	Model   *string       `json:"model,omitempty"`
	BaseURL *types.EnvVar `json:"url,omitempty"`
	ApiKey  types.EnvVar  `json:"apiKey"`
}

type ConnectionOllama struct {
	Model   *string      `json:"model,omitempty"`
	BaseURL types.EnvVar `json:"url,omitempty"`
	ApiKey  types.EnvVar `json:"apiKey,omitempty"`
}

type ConnectionAnthropic struct {
	Model   *string       `json:"model,omitempty"`
	BaseURL *types.EnvVar `json:"url,omitempty"`
	ApiKey  types.EnvVar  `json:"apiKey"`
}

type ConnectionGemini struct {
	Model  *string      `json:"model,omitempty"`
	ApiKey types.EnvVar `json:"apiKey"`
}

type ConnectionFacet struct {
	URL          string       `json:"url"`
	Token        types.EnvVar `json:"token,omitempty"`
	TimestampURL string       `json:"timestampUrl,omitempty"`
}

type ConnectionElasticsearch struct {
	URL         string       `json:"url"`
	Username    types.EnvVar `json:"username,omitempty"`
	Password    types.EnvVar `json:"password,omitempty"`
	InsecureTLS bool         `json:"insecureTLS,omitempty"`
}

type ConnectionRedis struct {
	URL      string       `json:"url"`
	Username types.EnvVar `json:"username,omitempty"`
	Password types.EnvVar `json:"password,omitempty"`
}

// ConnectionOpenSearch is duty's connection.OpensearchConnection: one or
// more node URLs, basic auth and a default index.
type ConnectionOpenSearch struct {
	types.HTTPBasicAuth `json:",inline"`
	URLs                []string `json:"urls,omitempty" yaml:"urls,omitempty"`
	Index               string   `json:"index,omitempty" yaml:"index,omitempty"`
	InsecureSkipVerify  bool     `json:"insecureSkipVerify,omitempty" yaml:"insecureSkipVerify,omitempty"`
}

// ConnectionSpec defines the desired state of Connection
type ConnectionSpec struct {
	Properties types.JSONStringMap `json:"properties,omitempty"`

	AWS    *ConnectionAWS    `json:"aws,omitempty"`
	AWSKMS *ConnectionAWSKMS `json:"awskms,omitempty"`
	S3     *ConnectionAWSS3  `json:"s3,omitempty"`

	Azure         *ConnectionAzure         `json:"azure,omitempty"`
	AzureKeyVault *ConnectionAzureKeyVault `json:"azureKeyVault,omitempty"`
	AzureDevops   *ConnectionAzureDevops   `json:"azureDevops,omitempty"`

	GCP    *ConnectionGCP    `json:"gcp,omitempty"`
	GCPKMS *ConnectionGCPKMS `json:"gcpkms,omitempty"`
	GCS    *ConnectionGCS    `json:"gcs,omitempty"`

	Anthropic *ConnectionAnthropic `json:"anthropic,omitempty"`
	Ollama    *ConnectionOllama    `json:"ollama,omitempty"`
	OpenAI    *ConnectionOpenAI    `json:"openai,omitempty"`
	Gemini    *ConnectionGemini    `json:"gemini,omitempty"`

	Elasticsearch *ConnectionElasticsearch `json:"elasticsearch,omitempty"`
	Facet         *ConnectionFacet         `json:"facet,omitempty"`
	Folder        *ConnectionFolder        `json:"folder,omitempty"`
	Git           *ConnectionGit           `json:"git,omitempty"`
	GitHub        *ConnectionGitHub        `json:"github,omitempty"`
	GitLab        *ConnectionGitLab        `json:"gitlab,omitempty"`
	HTTP          *ConnectionHTTP          `json:"http,omitempty"`
	Kubernetes    *ConnectionKubernetes    `json:"kubernetes,omitempty"`
	Loki          *ConnectionLoki          `json:"loki,omitempty"`
	MSSQL         *ConnectionMSSQL         `json:"mssql,omitempty"`
	Mongo         *ConnectionMongo         `json:"mongo,omitempty"`
	MySQL         *ConnectionMySQL         `json:"mysql,omitempty"`
	OpenSearch    *ConnectionOpenSearch    `json:"opensearch,omitempty"`
	Postgres      *ConnectionPostgres      `json:"postgres,omitempty"`
	Prometheus    *ConnectionPrometheus    `json:"prometheus,omitempty"`
	Redis         *ConnectionRedis         `json:"redis,omitempty"`
	SFTP          *ConnectionSFTP          `json:"sftp,omitempty"`
	SMB           *ConnectionSMB           `json:"smb,omitempty"`

	//////////////////////////////
	// Notification Connections //
	//////////////////////////////

	Discord    *ConnectionDiscord    `json:"discord,omitempty"`
	Ntfy       *ConnectionNtfy       `json:"ntfy,omitempty"`
	Pushbullet *ConnectionPushbullet `json:"pushbullet,omitempty"`
	Pushover   *ConnectionPushover   `json:"pushover,omitempty"`
	SMTP       *ConnectionSMTP       `json:"smtp,omitempty"`
	Slack      *ConnectionSlack      `json:"slack,omitempty"`
	Telegram   *ConnectionTelegram   `json:"telegram,omitempty"`

	// DEPRECATED
	URL types.EnvVar `json:"url,omitempty"`
	// DEPRECATED
	Port types.EnvVar `json:"port,omitempty"`
	// DEPRECATED
	Type string `json:"type,omitempty"`
	// DEPRECATED
	Username types.EnvVar `json:"username,omitempty"`
	// DEPRECATED
	Password types.EnvVar `json:"password,omitempty"`
	// DEPRECATED
	Certificate types.EnvVar `json:"certificate,omitempty"`
	// DEPRECATED
	InsecureTLS bool `json:"insecure_tls,omitempty"`
}

// ConnectionStatus defines the observed state of Connection
type ConnectionStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty" yaml:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty" yaml:"conditions,omitempty"`

	// Ref is the connection string
	Ref string `json:"ref"`
}

// Connection is the Schema for the connections API
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConnectionSpec   `json:"spec,omitempty"`
	Status ConnectionStatus `json:"status,omitempty"`
}
