package connection

import (
	"fmt"
	"strconv"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/fs"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons/collections"
)

// +kubebuilder:object:generate=true
type S3Connection struct {
	AWSConnection `json:",inline"`
	Bucket        string `yaml:"bucket,omitempty" json:"bucket,omitempty"`
	// glob path to restrict matches to a subset
	ObjectPath string `yaml:"objectPath,omitempty" json:"objectPath,omitempty"`
	// Use path style path: http://s3.amazonaws.com/BUCKET/KEY instead of http://BUCKET.s3.amazonaws.com/KEY
	UsePathStyle bool `yaml:"usePathStyle,omitempty" json:"usePathStyle,omitempty"`
}

func (t *S3Connection) GetProperties() map[string]string {
	return collections.MergeMap(
		t.AWSConnection.GetProperties(),
		map[string]string{
			"bucket":       t.Bucket,
			"objectPath":   t.ObjectPath,
			"usePathStyle": strconv.FormatBool(t.UsePathStyle),
		})
}

// Populate populates an AWSConnection with credentials and other information.
// If a connection name is specified, it'll be used to populate the endpoint, accessKey and secretKey.
func (t *S3Connection) Populate(ctx ConnectionContext) error {
	if err := t.AWSConnection.Populate(ctx); err != nil {
		return err
	}

	if t.ConnectionName != "" {
		connection, err := ctx.HydrateConnectionByURL(t.ConnectionName)
		if err != nil {
			return fmt.Errorf("could not parse EC2 access key: %v", err)
		}

		if region, ok := connection.Properties["bucket"]; ok {
			t.Bucket = region
		}

		if objectPath, ok := connection.Properties["objectPath"]; ok {
			t.ObjectPath = objectPath
		}

		if usePathStyle, ok := connection.Properties["usePathStyle"]; ok {
			if val, err := strconv.ParseBool(usePathStyle); err == nil {
				t.UsePathStyle = val
			}
		}

	}

	return nil
}

func (c S3Connection) ToModel() models.Connection {
	conn := c.AWSConnection.ToModel()
	conn.Type = models.ConnectionTypeS3
	if c.Bucket != "" {
		conn.Properties["bucket"] = c.Bucket
	}
	if c.ObjectPath != "" {
		conn.Properties["objectPath"] = c.ObjectPath
	}
	if c.UsePathStyle {
		conn.Properties["usePathStyle"] = strconv.FormatBool(c.UsePathStyle)
	}
	return conn
}

// Filesystem returns a filesystem interface for S3 operations
func (c *S3Connection) Filesystem(ctx context.Context) (fs.FilesystemRW, error) {
	if err := c.Populate(ctx); err != nil {
		return nil, err
	}
	cfg, err := c.Client(ctx)
	if err != nil {
		return nil, err
	}
	return fs.NewS3FS(ctx, c.Bucket, cfg), nil
}
