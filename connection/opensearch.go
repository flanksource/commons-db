package connection

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

type OpenSearchConnection struct {
	ConnectionName      string `json:"connection,omitempty" yaml:"connection,omitempty"`
	types.HTTPBasicAuth `json:",inline" yaml:",inline"`
	URLs                []string `json:"urls,omitempty" yaml:"urls,omitempty"`
	Index               string   `json:"index,omitempty" yaml:"index,omitempty"`
	InsecureSkipVerify  bool     `json:"insecureSkipVerify,omitempty" yaml:"insecureSkipVerify,omitempty"`
}

func (c *OpenSearchConnection) Hydrate(ctx ConnectionContext) error {
	if c.ConnectionName != "" {
		conn, err := ctx.HydrateConnectionByURL(c.ConnectionName)
		if err != nil {
			return err
		}
		if conn == nil {
			return fmt.Errorf("connection[%s] not found", c.ConnectionName)
		}
		if conn.Type != models.ConnectionTypeOpenSearch && conn.Type != models.ConnectionTypeElasticSearch {
			return fmt.Errorf("connection[%s] has type %s, expected opensearch or elasticsearch", c.ConnectionName, conn.Type)
		}
		if len(c.URLs) == 0 {
			if raw := conn.Properties["urls"]; raw != "" {
				c.URLs = strings.Split(raw, ",")
			} else if conn.URL != "" {
				c.URLs = []string{conn.URL}
			}
		}
		if c.Index == "" {
			c.Index = conn.Properties["index"]
		}
		if c.Username.IsEmpty() {
			if err := c.Username.Scan(conn.Username); err != nil {
				return err
			}
		}
		if c.Password.IsEmpty() {
			if err := c.Password.Scan(conn.Password); err != nil {
				return err
			}
		}
		if raw := conn.Properties["insecure_tls"]; raw != "" {
			insecure, err := strconv.ParseBool(raw)
			if err != nil {
				return fmt.Errorf("invalid insecure_tls: %w", err)
			}
			c.InsecureSkipVerify = c.InsecureSkipVerify || insecure
		} else {
			c.InsecureSkipVerify = c.InsecureSkipVerify || conn.InsecureTLS
		}
	}
	if len(c.URLs) == 0 {
		return fmt.Errorf("opensearch connection urls cannot be empty")
	}
	for _, url := range c.URLs {
		if strings.TrimSpace(url) == "" {
			return fmt.Errorf("opensearch connection urls cannot contain empty values")
		}
	}
	var err error
	c.Username.ValueStatic, err = ctx.GetEnvValueFromCache(c.Username, ctx.GetNamespace())
	if err != nil {
		return err
	}
	c.Password.ValueStatic, err = ctx.GetEnvValueFromCache(c.Password, ctx.GetNamespace())
	return err
}

func setupOpenSearchConnection(ctx context.Context, conn *OpenSearchConnection, cmd *exec.Cmd) error {
	if err := conn.Hydrate(ctx); err != nil {
		return fmt.Errorf("hydrate opensearch connection: %w", err)
	}
	urls, err := json.Marshal(conn.URLs)
	if err != nil {
		return err
	}
	cmd.Env = append(cmd.Env, "OPENSEARCH_URLS="+string(urls), "OPENSEARCH_USERNAME="+conn.Username.ValueStatic, "OPENSEARCH_PASSWORD="+conn.Password.ValueStatic, "OPENSEARCH_INDEX="+conn.Index, "OPENSEARCH_INSECURE_SKIP_VERIFY="+strconv.FormatBool(conn.InsecureSkipVerify))
	return nil
}
