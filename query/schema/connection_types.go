package schema

import "github.com/flanksource/commons-db/models"

// allConnectionTypes is the full set of connection types, kept in sync with the
// models constants so the `type` enum never drifts. A subset (the backends with
// a query provider) get a tailored if/then branch via tailoredProviders; the
// rest fall back to the generic base fields.
var allConnectionTypes = []string{
	models.ConnectionTypeAnthropic, models.ConnectionTypeAWS, models.ConnectionTypeAWSKMS,
	models.ConnectionTypeAzure, models.ConnectionTypeAzureDevops, models.ConnectionTypeAzureKeyVault,
	models.ConnectionTypeClickHouse, models.ConnectionTypeDiscord, models.ConnectionTypeDynatrace,
	models.ConnectionTypeElasticSearch, models.ConnectionTypeEmail, models.ConnectionTypeFolder,
	models.ConnectionTypeGCP, models.ConnectionTypeGCPKMS, models.ConnectionTypeGCS,
	models.ConnectionTypeGemini, models.ConnectionTypeGenericWebhook, models.ConnectionTypeGit,
	models.ConnectionTypeGithub, models.ConnectionTypeGitlab, models.ConnectionTypeGoogleChat,
	models.ConnectionTypeHTTP, models.ConnectionTypeIFTTT, models.ConnectionTypeJaeger,
	models.ConnectionTypeJMeter, models.ConnectionTypeKubernetes, models.ConnectionTypeLDAP,
	models.ConnectionTypeLoki,
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

// connectionTypeIcons maps each connection type to a runtime icon name (resolved
// by clicky-ui's fallback icon provider, e.g. @flanksource/icons), so the type
// picker renders as an icon grid (x-enum-icons). Names mirror flanksource-ui's
// connectionTypes.tsx. Kept in sync with allConnectionTypes by the drift-guard test.
var connectionTypeIcons = map[string]string{
	models.ConnectionTypeAnthropic:      "anthropic",
	models.ConnectionTypeAWS:            "aws",
	models.ConnectionTypeAWSKMS:         "aws-kms",
	models.ConnectionTypeAzure:          "azure",
	models.ConnectionTypeAzureDevops:    "azure-devops",
	models.ConnectionTypeAzureKeyVault:  "azure-key-vault",
	models.ConnectionTypeClickHouse:     "clickhouse",
	models.ConnectionTypeDiscord:        "discord",
	models.ConnectionTypeDynatrace:      "dynatrace",
	models.ConnectionTypeElasticSearch:  "elasticsearch",
	models.ConnectionTypeEmail:          "email",
	models.ConnectionTypeFolder:         "folder",
	models.ConnectionTypeGCP:            "google-cloud",
	models.ConnectionTypeGCPKMS:         "gcp-kms",
	models.ConnectionTypeGCS:            "gcs",
	models.ConnectionTypeGemini:         "gemini",
	models.ConnectionTypeGenericWebhook: "webhook",
	models.ConnectionTypeGit:            "git",
	models.ConnectionTypeGithub:         "github",
	models.ConnectionTypeGitlab:         "gitlab",
	models.ConnectionTypeGoogleChat:     "google-chat",
	models.ConnectionTypeHTTP:           "http",
	models.ConnectionTypeIFTTT:          "ifttt",
	models.ConnectionTypeJaeger:         "jaeger",
	models.ConnectionTypeJMeter:         "jmeter",
	models.ConnectionTypeKubernetes:     "kubernetes",
	models.ConnectionTypeLDAP:           "ldap",
	models.ConnectionTypeLoki:           "grafana",
	models.ConnectionTypeMatrix:         "matrix",
	models.ConnectionTypeMattermost:     "mattermost",
	models.ConnectionTypeMongo:          "mongo",
	models.ConnectionTypeMySQL:          "mysql",
	models.ConnectionTypeNtfy:           "ntfy",
	models.ConnectionTypeOllama:         "ollama",
	models.ConnectionTypeOpenAI:         "openai",
	models.ConnectionTypeOpenSearch:     "opensearch",
	models.ConnectionTypeOpsGenie:       "opsgenie",
	models.ConnectionTypePostgres:       "postgres",
	models.ConnectionTypePrometheus:     "prometheus",
	models.ConnectionTypePushbullet:     "pushbullet",
	models.ConnectionTypePushover:       "pushover",
	models.ConnectionTypeRedis:          "redis",
	models.ConnectionTypeRestic:         "restic",
	models.ConnectionTypeRocketchat:     "rocket",
	models.ConnectionTypeS3:             "aws-s3",
	models.ConnectionTypeSFTP:           "sftp",
	models.ConnectionTypeSlack:          "slack",
	models.ConnectionTypeSlackWebhook:   "slack",
	models.ConnectionTypeSMB:            "smb",
	models.ConnectionTypeSQLServer:      "sqlserver",
	models.ConnectionTypeTeams:          "teams",
	models.ConnectionTypeTelegram:       "telegram",
	models.ConnectionTypeWebhook:        "webhook",
	models.ConnectionTypeWindows:        "windows",
	models.ConnectionTypeZulipChat:      "zulip",
}
