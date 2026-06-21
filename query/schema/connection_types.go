package schema

import "github.com/flanksource/commons-db/models"

// connField describes one field of a connection type. When Property is set the
// field is stored under the connection's `properties` map; otherwise Base names
// a top-level Connection column (url, username, password, certificate, ...).
type connField struct {
	Base        string
	Property    string
	Label       string
	Type        string
	Format      string
	Enum        []string
	Required    bool
	Description string
}

// connTypeSpec is the field set contributed by a connection type's if/then branch.
type connTypeSpec struct {
	Type   string
	Title  string
	Fields []connField
}

// allConnectionTypes is the full set of connection types, kept in sync with the
// models constants so the `type` enum never drifts.
var allConnectionTypes = []string{
	models.ConnectionTypeAnthropic, models.ConnectionTypeAWS, models.ConnectionTypeAWSKMS,
	models.ConnectionTypeAzure, models.ConnectionTypeAzureDevops, models.ConnectionTypeAzureKeyVault,
	models.ConnectionTypeClickHouse, models.ConnectionTypeDiscord, models.ConnectionTypeDynatrace,
	models.ConnectionTypeElasticSearch, models.ConnectionTypeEmail, models.ConnectionTypeFolder,
	models.ConnectionTypeGCP, models.ConnectionTypeGCPKMS, models.ConnectionTypeGCS,
	models.ConnectionTypeGemini, models.ConnectionTypeGenericWebhook, models.ConnectionTypeGit,
	models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeGoogleChat,
	models.ConnectionTypeHTTP, models.ConnectionTypeIFTTT, models.ConnectionTypeJMeter,
	models.ConnectionTypeKubernetes, models.ConnectionTypeLDAP, models.ConnectionTypeLoki,
	models.ConnectionTypeMatrix, models.ConnectionTypeMattermost, models.ConnectionTypeMongo,
	models.ConnectionTypeMySQL, models.ConnectionTypeNtfy, models.ConnectionTypeOllama,
	models.ConnectionTypeOpenAI, models.ConnectionTypeOpenSearch, models.ConnectionTypeOpsGenie,
	models.ConnectionTypePostgres, models.ConnectionTypePrometheus, models.ConnectionTypePushbullet,
	models.ConnectionTypePushover, models.ConnectionTypeRedis, models.ConnectionTypeRestic,
	models.ConnectionTypeRocketchat, models.ConnectionTypeS3, models.ConnectionTypeSFTP,
	models.ConnectionTypeSlack, models.ConnectionTypeSlackWebhook, models.ConnectionTypeSMB,
	models.ConnectionTypeSQLServer, models.ConnectionTypeTeams, models.ConnectionTypeTelegram,
	models.ConnectionTypeWebhook, models.ConnectionTypeWindows, models.ConnectionTypeZulipChat,
}

func connectionTypeEnum() []string { return allConnectionTypes }

// field constructors keep the registry terse.
func urlField(desc string) connField {
	return connField{Base: "url", Label: "URL", Required: true, Description: desc}
}
func userField() connField { return connField{Base: "username", Label: "Username"} }
func passField(l string) connField {
	return connField{Base: "password", Label: l, Format: "password"}
}
func certField(l, desc string) connField {
	return connField{Base: "certificate", Label: l, Description: desc}
}
func propField(key, label, desc string) connField {
	return connField{Property: key, Label: label, Description: desc}
}

// sqlSpec is the shared shape for SQL backends: a required DSN plus optional creds.
func sqlSpec(typ, title, dsn string) connTypeSpec {
	return connTypeSpec{Type: typ, Title: title, Fields: []connField{
		urlField(dsn), userField(), passField("Password"),
	}}
}

// httpSpec is the shape for HTTP-style backends: a required base URL plus optional creds.
func httpSpec(typ, title, urlDesc string) connTypeSpec {
	return connTypeSpec{Type: typ, Title: title, Fields: []connField{
		urlField(urlDesc), userField(), passField("Password"),
	}}
}

// apiKeySpec is the shape for LLM/API backends: a token in `password`, optional URL.
func apiKeySpec(typ, title string) connTypeSpec {
	return connTypeSpec{Type: typ, Title: title, Fields: []connField{
		{Base: "url", Label: "API Base URL"},
		passField("API Key"),
	}}
}

// connectionTypes contributes per-type fields for the if/then branches. Types not
// listed here still appear in the `type` enum and fall back to the generic base
// fields (url/username/password/properties).
var connectionTypes = []connTypeSpec{
	sqlSpec(models.ConnectionTypePostgres, "PostgreSQL", "postgres://user:pass@host:5432/db?sslmode=disable"),
	sqlSpec(models.ConnectionTypeMySQL, "MySQL", "user:pass@tcp(host:3306)/db"),
	sqlSpec(models.ConnectionTypeSQLServer, "SQL Server", "sqlserver://user:pass@host:1433?database=db"),
	sqlSpec(models.ConnectionTypeClickHouse, "ClickHouse", "clickhouse://user:pass@host:9000/db"),
	sqlSpec(models.ConnectionTypeMongo, "MongoDB", "mongodb://user:pass@host:27017/db"),
	sqlSpec(models.ConnectionTypeRedis, "Redis", "redis://user:pass@host:6379/0"),
	httpSpec(models.ConnectionTypeHTTP, "HTTP", "Base URL of the HTTP endpoint"),
	httpSpec(models.ConnectionTypePrometheus, "Prometheus", "Prometheus base URL (e.g. http://prometheus:9090)"),
	httpSpec(models.ConnectionTypeLoki, "Loki", "Loki base URL (e.g. http://loki:3100)"),
	httpSpec(models.ConnectionTypeOpenSearch, "OpenSearch", "OpenSearch base URL"),
	httpSpec(models.ConnectionTypeElasticSearch, "Elasticsearch", "Elasticsearch base URL"),
	httpSpec(models.ConnectionTypeDynatrace, "Dynatrace", "Dynatrace environment URL"),
	apiKeySpec(models.ConnectionTypeOpenAI, "OpenAI"),
	apiKeySpec(models.ConnectionTypeAnthropic, "Anthropic"),
	apiKeySpec(models.ConnectionTypeGemini, "Gemini"),
	apiKeySpec(models.ConnectionTypeOllama, "Ollama"),
	{Type: models.ConnectionTypeAWS, Title: "AWS", Fields: []connField{
		{Base: "username", Label: "Access Key ID"},
		{Base: "password", Label: "Secret Access Key", Format: "password"},
		propField("region", "Region", "Default AWS region (e.g. us-east-1)"),
		propField("profile", "Profile", "Named AWS profile"),
	}},
	{Type: models.ConnectionTypeS3, Title: "S3", Fields: []connField{
		{Base: "url", Label: "Endpoint", Description: "S3 endpoint (blank for AWS)"},
		{Base: "username", Label: "Access Key ID"},
		{Base: "password", Label: "Secret Access Key", Format: "password"},
		propField("bucket", "Bucket", "S3 bucket name"),
		propField("region", "Region", "AWS region"),
	}},
	{Type: models.ConnectionTypeGCP, Title: "Google Cloud", Fields: []connField{
		{Base: "url", Label: "Endpoint"},
		certField("Service Account JSON", "GCP service-account credentials JSON"),
	}},
	{Type: models.ConnectionTypeAzure, Title: "Azure", Fields: []connField{
		{Base: "username", Label: "Client ID"},
		{Base: "password", Label: "Client Secret", Format: "password"},
		propField("tenant", "Tenant", "Azure tenant ID"),
	}},
	{Type: models.ConnectionTypeKubernetes, Title: "Kubernetes", Fields: []connField{
		certField("Kubeconfig", "Kubeconfig contents"),
	}},
	{Type: models.ConnectionTypeGit, Title: "Git", Fields: []connField{
		urlField("Git repository URL"),
		userField(), passField("Password / Token"),
		certField("SSH Key", "SSH private key"),
		propField("ref", "Ref", "Branch, tag or commit"),
	}},
	{Type: models.ConnectionTypeGithub, Title: "GitHub", Fields: []connField{
		{Base: "url", Label: "API URL", Description: "Blank for github.com"},
		passField("Personal Access Token"),
	}},
	{Type: models.ConnectionTypeGitlab, Title: "GitLab", Fields: []connField{
		{Base: "url", Label: "API URL"},
		passField("Personal Access Token"),
	}},
	{Type: models.ConnectionTypeSlack, Title: "Slack", Fields: []connField{
		passField("Bot Token"),
	}},
	{Type: models.ConnectionTypeFolder, Title: "Folder", Fields: []connField{
		{Base: "url", Label: "Path", Required: true, Description: "Filesystem path"},
	}},
	{Type: models.ConnectionTypeSFTP, Title: "SFTP", Fields: []connField{
		urlField("sftp://host:22/path"), userField(), passField("Password"),
	}},
	{Type: models.ConnectionTypeSMB, Title: "SMB", Fields: []connField{
		urlField("smb://host/share"), userField(), passField("Password"),
	}},
}
