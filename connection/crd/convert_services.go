package crd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/flanksource/commons/collections"
	"github.com/samber/lo"

	"github.com/flanksource/commons-db/models"
)

func connectionFromServiceSpec(spec ConnectionSpec, dbObj *models.Connection) {
	if spec.Facet != nil {
		dbObj.Type = models.ConnectionTypeFacet
		dbObj.URL = spec.Facet.URL
		dbObj.Password = spec.Facet.Token.String()
		if spec.Facet.TimestampURL != "" {
			dbObj.Properties = map[string]string{
				"timestampUrl": spec.Facet.TimestampURL,
			}
		}
	}

	if spec.Elasticsearch != nil {
		dbObj.Type = models.ConnectionTypeElasticSearch
		dbObj.URL = spec.Elasticsearch.URL
		dbObj.Username = spec.Elasticsearch.Username.String()
		dbObj.Password = spec.Elasticsearch.Password.String()
		dbObj.InsecureTLS = spec.Elasticsearch.InsecureTLS
	}

	if spec.Folder != nil {
		dbObj.Type = models.ConnectionTypeFolder
		dbObj.Properties = map[string]string{
			"path": spec.Folder.Path,
		}
	}

	if spec.Git != nil {
		dbObj.Type = models.ConnectionTypeGit
		dbObj.URL = spec.Git.URL
		if spec.Git.Certificate != nil {
			dbObj.Certificate = spec.Git.Certificate.String()
		}
		if spec.Git.Username != nil {
			dbObj.Username = spec.Git.Username.String()
		}
		if spec.Git.Password != nil {
			dbObj.Password = spec.Git.Password.String()
		}
		dbObj.Properties = map[string]string{
			"ref": spec.Git.Ref,
		}
	}

	if spec.GitHub != nil {
		dbObj.URL = spec.GitHub.URL
		dbObj.Type = models.ConnectionTypeGithub
		dbObj.Password = spec.GitHub.PersonalAccessToken.String()
	}

	if spec.GitLab != nil {
		dbObj.URL = spec.GitLab.URL
		dbObj.Type = models.ConnectionTypeGitlab
		dbObj.Password = spec.GitLab.PersonalAccessToken.String()
	}

	if spec.HTTP != nil {
		connectionFromHTTPSpec(*spec.HTTP, dbObj)
	}

	if spec.Loki != nil {
		dbObj.Type = models.ConnectionTypeLoki
		dbObj.URL = spec.Loki.URL
		dbObj.Username = spec.Loki.Username.String()
		dbObj.Password = spec.Loki.Password.String()
	}

	if spec.OpenSearch != nil {
		*dbObj = mergeConnectionModels(*dbObj, spec.OpenSearch.toModel())
	}

	if spec.Prometheus != nil {
		dbObj.Type = models.ConnectionTypePrometheus
		dbObj.URL = spec.Prometheus.URL.String()
		dbObj.Username = spec.Prometheus.Username.String()
		dbObj.Password = spec.Prometheus.Password.String()
		dbObj.Properties = collections.MergeMap(
			spec.Prometheus.OAuth.AsProperties(),
			map[string]string{"bearer": spec.Prometheus.Bearer.String()},
		)
	}

	connectionFromAISpec(spec, dbObj)
}

func connectionFromHTTPSpec(spec ConnectionHTTP, dbObj *models.Connection) {
	dbObj.Type = models.ConnectionTypeHTTP
	dbObj.URL = spec.URL
	if spec.Username != nil {
		dbObj.Username = spec.Username.String()
	}
	if spec.Password != nil {
		dbObj.Password = spec.Password.String()
	}
	dbObj.InsecureTLS = spec.InsecureTLS
	dbObj.Properties = collections.MergeMap(
		spec.OAuth.AsProperties(),
		map[string]string{
			"bearer":       spec.Bearer.String(),
			"insecure_tls": strconv.FormatBool(spec.InsecureTLS),
		},
	)
	if !spec.TLS.CA.IsEmpty() {
		dbObj.Properties["ca"] = spec.TLS.CA.String()
	}
	if !spec.TLS.Cert.IsEmpty() {
		dbObj.Properties["cert"] = spec.TLS.Cert.String()
	}
	if !spec.TLS.Key.IsEmpty() {
		dbObj.Properties["key"] = spec.TLS.Key.String()
	}
	if len(spec.Headers) > 0 {
		if b, err := json.Marshal(spec.Headers); err == nil {
			dbObj.Properties["headers"] = string(b)
		}
	}
	if spec.AWSSigV4 != nil {
		if b, err := json.Marshal(spec.AWSSigV4); err == nil {
			dbObj.Properties["awsSigV4"] = string(b)
		}
	}
}

func connectionFromAISpec(spec ConnectionSpec, dbObj *models.Connection) {
	if spec.Anthropic != nil {
		dbObj.Type = models.ConnectionTypeAnthropic
		dbObj.Password = spec.Anthropic.ApiKey.String()
		if spec.Anthropic.BaseURL != nil {
			dbObj.URL = spec.Anthropic.BaseURL.String()
		}
		if spec.Anthropic.Model != nil {
			dbObj.Properties = map[string]string{"model": *spec.Anthropic.Model}
		}
	}

	if spec.OpenAI != nil {
		dbObj.Type = models.ConnectionTypeOpenAI
		dbObj.Password = spec.OpenAI.ApiKey.String()
		if spec.OpenAI.BaseURL != nil {
			dbObj.URL = spec.OpenAI.BaseURL.String()
		}
		if spec.OpenAI.Model != nil {
			dbObj.Properties = map[string]string{"model": *spec.OpenAI.Model}
		}
	}

	if spec.Ollama != nil {
		dbObj.Type = models.ConnectionTypeOllama
		dbObj.URL = spec.Ollama.BaseURL.String()
		dbObj.Password = spec.Ollama.ApiKey.String()
		if spec.Ollama.Model != nil {
			dbObj.Properties = map[string]string{"model": *spec.Ollama.Model}
		}
	}

	if spec.Gemini != nil {
		dbObj.Type = models.ConnectionTypeGemini
		dbObj.Password = spec.Gemini.ApiKey.String()
		if spec.Gemini.Model != nil {
			dbObj.Properties = map[string]string{"model": *spec.Gemini.Model}
		}
	}
}

func connectionFromDatabaseSpec(spec ConnectionSpec, dbObj *models.Connection) {
	if spec.Mongo != nil {
		dbObj.Type = models.ConnectionTypeMongo
		dbObj.URL = spec.Mongo.URL.String()
		if dbObj.URL == "" {
			dbObj.URL = "mongodb://$(username):$(password)@$(properties.host)/?$(properties.database)"
		}
		queryParams := url.Values{}
		if spec.Mongo.ReplicaSet != "" {
			queryParams.Set("replicaSet", spec.Mongo.ReplicaSet)
		}
		if spec.Mongo.InsecureTLS {
			queryParams.Set("tls", "true")
		}
		if len(queryParams) > 0 {
			dbObj.URL += queryParams.Encode()
		}
		dbObj.Username = spec.Mongo.Username.String()
		dbObj.Password = spec.Mongo.Password.String()
		dbObj.Properties = map[string]string{
			"host":         spec.Mongo.Host.String(),
			"database":     spec.Mongo.Database.String(),
			"replica_set":  spec.Mongo.ReplicaSet,
			"insecure_tls": strconv.FormatBool(spec.Mongo.InsecureTLS),
		}
	}

	if spec.MSSQL != nil {
		dbObj.Type = models.ConnectionTypeSQLServer
		dbObj.URL = spec.MSSQL.URL.String()
		if dbObj.URL == "" {
			dbObj.URL = "sqlserver://$(username):$(password)@$(properties.host)?database=$(properties.database)"
		}
		dbObj.Username = spec.MSSQL.Username.String()
		dbObj.Password = spec.MSSQL.Password.String()
		dbObj.Properties = map[string]string{
			"host":     spec.MSSQL.Host.String(),
			"database": spec.MSSQL.Database.String(),
		}
		if spec.MSSQL.TrustServerCertificate != nil {
			dbObj.URL += "&TrustServerCertificate=$(properties.trust_server_certificate)"
			dbObj.Properties["trust_server_certificate"] = strconv.FormatBool(*spec.MSSQL.TrustServerCertificate)
		}
	}

	if spec.MySQL != nil {
		dbObj.Type = models.ConnectionTypeMySQL
		dbObj.URL = spec.MySQL.URL.String()
		if dbObj.URL == "" {
			dbObj.URL = "mysql://$(username):$(password)@$(properties.host)/$(properties.database)"
		}
		if spec.MySQL.InsecureTLS {
			dbObj.URL += "sslMode=disabled"
		}
		dbObj.Username = spec.MySQL.Username.String()
		dbObj.Password = spec.MySQL.Password.String()
		dbObj.Properties = map[string]string{
			"host":         spec.MySQL.Host.String(),
			"database":     spec.MySQL.Database.String(),
			"insecure_tls": strconv.FormatBool(spec.MySQL.InsecureTLS),
		}
	}

	if spec.Postgres != nil {
		dbObj.Type = models.ConnectionTypePostgres
		dbObj.URL = spec.Postgres.URL.String()
		if dbObj.URL == "" {
			dbObj.URL = "postgres://$(username):$(password)@$(properties.host)/$(properties.database)"
		}
		if spec.Postgres.InsecureTLS {
			dbObj.URL += "?sslmode=disable"
		}
		dbObj.Username = spec.Postgres.Username.String()
		dbObj.Password = spec.Postgres.Password.String()
		dbObj.Properties = map[string]string{
			"host":         spec.Postgres.Host.String(),
			"database":     spec.Postgres.Database.String(),
			"insecure_tls": strconv.FormatBool(spec.Postgres.InsecureTLS),
		}
	}

	if spec.Redis != nil {
		dbObj.Type = models.ConnectionTypeRedis
		dbObj.URL = spec.Redis.URL
		dbObj.Username = spec.Redis.Username.String()
		dbObj.Password = spec.Redis.Password.String()
	}

	if spec.SFTP != nil {
		dbObj.Type = models.ConnectionTypeSFTP
		dbObj.URL = spec.SFTP.Host.String()
		dbObj.Username = spec.SFTP.Username.String()
		dbObj.Password = spec.SFTP.Password.String()
		dbObj.Properties = map[string]string{
			"path": spec.SFTP.Path,
			"port": strconv.Itoa(spec.SFTP.Port),
		}
	}

	if spec.SMB != nil {
		dbObj.Type = models.ConnectionTypeSMB
		dbObj.URL = spec.SMB.Server.String()
		dbObj.Username = spec.SMB.Username.String()
		dbObj.Password = spec.SMB.Password.String()
		dbObj.Properties = map[string]string{
			"port": spec.SMB.Port.String(),
		}
	}
}

func connectionFromNotificationSpec(spec ConnectionSpec, dbObj *models.Connection) {
	if spec.Discord != nil {
		dbObj.URL = "discord://$(password)@$(username)"
		dbObj.Type = models.ConnectionTypeDiscord
		dbObj.Username = spec.Discord.WebhookID
		dbObj.Password = spec.Discord.Token
	}

	if spec.SMTP != nil {
		connectionFromSMTPSpec(*spec.SMTP, dbObj)
	}

	if spec.Ntfy != nil {
		dbObj.URL = fmt.Sprintf("ntfy://$(username):$(password)@%s/%s", spec.Ntfy.Host, spec.Ntfy.Topic)
		dbObj.Type = models.ConnectionTypeNtfy
		dbObj.Username = spec.Ntfy.Username.String()
		dbObj.Password = spec.Ntfy.Password.String()
		dbObj.Properties = map[string]string{"topic": spec.Ntfy.Topic}
	}

	if spec.Pushbullet != nil {
		targets := strings.Join(spec.Pushbullet.Targets, ",")
		dbObj.URL = fmt.Sprintf("pushbullet://$(password)/%s", targets)
		dbObj.Type = models.ConnectionTypePushbullet
		dbObj.Password = spec.Pushbullet.Token.String()
		dbObj.Properties = map[string]string{"targets": targets}
	}

	if spec.Pushover != nil {
		dbObj.URL = "pushover://:$(password)@$(username)"
		dbObj.Type = models.ConnectionTypePushover
		dbObj.Username = spec.Pushover.User
		dbObj.Password = spec.Pushover.Token.String()
	}

	if spec.Slack != nil {
		dbObj.URL = "slack://$(password)@$(username)"
		dbObj.Type = models.ConnectionTypeSlack
		dbObj.Username = spec.Slack.Channel
		dbObj.Password = spec.Slack.Token.String()
		dbObj.Properties = map[string]string{
			"BotName":  spec.Slack.BotName,
			"Icon":     spec.Slack.Color,
			"ThreadTS": spec.Slack.ThreadTS,
			"Title":    spec.Slack.Title,
			"Color":    spec.Slack.Color,
		}
	}

	if spec.Telegram != nil {
		dbObj.URL = "telegram://$(password)@telegram/?Chats=$(username)"
		dbObj.Type = models.ConnectionTypeTelegram
		dbObj.Username = spec.Telegram.Chats.String()
		dbObj.Password = spec.Telegram.Token.String()
	}
}

// connectionFromSMTPSpec maps smtp, its unset fields defaulted as
// mission-control defaults them.
func connectionFromSMTPSpec(smtp ConnectionSMTP, dbObj *models.Connection) {
	smtp.Auth, _ = lo.Coalesce(smtp.Auth, SMTPAuthPlain)
	smtp.Encryption, _ = lo.Coalesce(smtp.Encryption, "Auto")
	smtp.FromAddress, _ = lo.Coalesce(smtp.FromAddress, "no-reply@example.com")
	smtp.Port, _ = lo.Coalesce(smtp.Port, 25)
	if len(smtp.ToAddresses) == 0 {
		smtp.ToAddresses = []string{"no-reply@example.com"}
	}
	dbObj.URL = fmt.Sprintf("smtp://$(username):$(password)@%s:%d/?UseStartTLS=%s&Encryption=%s&Auth=%s&from=%s&to=%s",
		smtp.Host, smtp.Port,
		strconv.FormatBool(smtp.InsecureTLS), smtp.Encryption,
		smtp.Auth, smtp.FromAddress,
		strings.Join(smtp.ToAddresses, ","),
	)
	dbObj.Type = models.ConnectionTypeEmail
	dbObj.Username = smtp.Username.String()
	dbObj.Password = smtp.Password.String()
	dbObj.Properties = map[string]string{
		"port":     strconv.Itoa(smtp.Port),
		"subject":  smtp.Subject,
		"auth":     string(smtp.Auth),
		"from":     smtp.FromAddress,
		"to":       strings.Join(smtp.ToAddresses, ","),
		"fromname": smtp.FromName,
		"headers":  stringMapToString(smtp.Headers),
	}
}
