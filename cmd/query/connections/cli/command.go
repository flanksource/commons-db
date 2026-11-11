package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/extensions"
	"github.com/flanksource/clicky/text"
	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/models"
	queryschema "github.com/flanksource/commons-db/query/schema"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type Backend interface {
	Create(context.Context, map[string]any) (*models.Connection, error)
	Update(context.Context, string, map[string]any) (*models.Connection, error)
	ResolveReference(string) (*models.Connection, error)
	Inventory(connections.ListOptions) ([]connections.ConnectionInventory, error)
	Validate(context.Context, connections.ValidateOptions) (connections.ValidationResult, error)
	Delete(string) error
}
type Options struct {
	Open    func(context.Context, string) (Backend, func() error, error)
	Sources []string
}

func New(options Options) *cobra.Command {
	root := &cobra.Command{Use: "connections", Short: "Manage layered connection files (or --source=db)", SilenceUsage: true}
	sources := options.Sources
	if len(sources) == 0 {
		sources = []string{"file", "db"}
	}
	root.PersistentFlags().String("source", "file", "Connection source: "+strings.Join(sources, ", "))
	add := &cobra.Command{Use: "add", Short: "Add a connection by type"}
	root.AddCommand(add)
	for _, typ := range queryschema.ConnectionTypes() {
		f := &ConnectionFlags{Type: typ}
		cmd := &cobra.Command{Use: typ, Short: "Add a " + typ + " connection"}
		bindCommon(cmd, f)
		_ = cmd.MarkFlagRequired("name")
		addTypeSpecificFlags(cmd, f, typ)
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			return withService(cmd, options, func(s Backend) (any, error) {
				c, err := BuildConnectionFromFlags(f)
				if err != nil {
					return nil, err
				}
				if err := validateConnectionFlags(f); err != nil {
					return nil, err
				}
				if err := applyProperties(&c, f.Properties); err != nil {
					return nil, err
				}
				if err := queryschema.ValidateConnection(c); err != nil {
					return nil, err
				}
				return s.Create(cmd.Context(), body(c))
			})
		}
		add.AddCommand(cmd)
	}
	list := &cobra.Command{Use: "list", Short: "List effective connections and source paths", Args: cobra.NoArgs}
	list.Flags().String("type", "", "Filter by connection type")
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		return withService(cmd, options, func(s Backend) (any, error) {
			typ, _ := cmd.Flags().GetString("type")
			return s.Inventory(connections.ListOptions{Type: typ})
		})
	}
	root.AddCommand(list)
	edit := &cobra.Command{Use: "edit <reference>", Short: "Edit a connection in its owning file", Args: cobra.ExactArgs(1)}
	f := &ConnectionFlags{}
	bindCommon(edit, f)
	for _, typ := range queryschema.ConnectionTypes() {
		temp := &cobra.Command{Use: typ}
		addTypeSpecificFlags(temp, f, typ)
		temp.Flags().VisitAll(func(flag *pflag.Flag) {
			if edit.Flags().Lookup(flag.Name) == nil {
				edit.Flags().AddFlag(flag)
			}
		})
	}
	*f = ConnectionFlags{}
	edit.Flags().BoolP("interactive", "i", false, "Edit the existing connection with Clicky's form")
	edit.RunE = func(cmd *cobra.Command, args []string) error {
		return withService(cmd, options, func(s Backend) (any, error) {
			c, err := s.ResolveReference(args[0])
			if err != nil {
				return nil, err
			}
			before := body(*c)
			before["properties"] = maps.Clone(c.Properties)
			if err := editModel(cmd, f, c); err != nil {
				return nil, err
			}
			interactive, _ := cmd.Flags().GetBool("interactive")
			if interactive {
				schema, err := json.Marshal(queryschema.Connection(queryschema.ConnectionOptions{AllowedTypes: []string{c.Type}}))
				if err != nil {
					return nil, err
				}
				values, err := clicky.PromptForm(cmd.Context(), clicky.PromptFormOptions{Title: "Edit connection", Schema: schema, Default: body(*c)})
				if err != nil {
					return nil, err
				}
				patch, err := connectionPatch(before, values)
				if err != nil {
					return nil, err
				}
				return s.Update(cmd.Context(), c.ID.String(), patch)
			}
			if err := queryschema.ValidateConnection(*c); err != nil {
				return nil, err
			}
			patch, err := connectionPatch(before, body(*c))
			if err != nil {
				return nil, err
			}
			return s.Update(cmd.Context(), c.ID.String(), patch)
		})
	}
	root.AddCommand(edit)
	for _, verb := range []string{"validate", "delete"} {
		cmd := &cobra.Command{Use: verb + " <reference>", Short: verb + " a saved connection", Args: cobra.ExactArgs(1)}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			return withService(cmd, options, func(s Backend) (any, error) {
				c, err := s.ResolveReference(args[0])
				if err != nil {
					return nil, err
				}
				if verb == "validate" {
					return s.Validate(cmd.Context(), connections.ValidateOptions{Reference: c.ID.String()})
				}
				return nil, s.Delete(c.ID.String())
			})
		}
		root.AddCommand(cmd)
	}
	extensions.CobraExtensions(root).Interactive()
	return root
}

func connectionPatch(before, after map[string]any) (map[string]any, error) {
	normalized := make([]map[string]any, 2)
	for i, value := range []map[string]any{before, after} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(encoded, &normalized[i]); err != nil {
			return nil, err
		}
	}
	patch := normalized[1]
	for key, value := range patch {
		if reflect.DeepEqual(value, normalized[0][key]) {
			delete(patch, key)
			continue
		}
		if key == "properties" {
			props, ok := value.(map[string]any)
			if !ok {
				continue
			}
			previous, _ := normalized[0][key].(map[string]any)
			for name, v := range props {
				if reflect.DeepEqual(v, previous[name]) {
					delete(props, name)
				}
			}
		}
	}
	return patch, nil
}

func bindCommon(cmd *cobra.Command, f *ConnectionFlags) {
	cmd.Flags().StringVar(&f.Name, "name", "", "Connection name")
	cmd.Flags().StringVar(&f.Namespace, "namespace", "", "Connection namespace")
	cmd.Flags().StringVar(&f.Layer, "layer", "", "Save to a writable storage layer: db, global, app or cwd")
	cmd.Flags().StringSliceVar(&f.Properties, "properties", nil, "Additional property key=value (repeatable)")
}

func body(c models.Connection) map[string]any {
	return map[string]any{"name": c.Name, "namespace": c.Namespace, "layer": c.Layer, "type": c.Type, "url": c.URL, "username": c.Username, "password": c.Password, "certificate": c.Certificate, "insecure_tls": c.InsecureTLS, "properties": c.Properties}
}

func withService(cmd *cobra.Command, options Options, run func(Backend) (any, error)) (err error) {
	source, _ := cmd.Flags().GetString("source")
	sources := options.Sources
	if len(sources) == 0 {
		sources = []string{"file", "db"}
	}
	if !slices.Contains(sources, source) {
		return fmt.Errorf("invalid --source %q: expected %s", source, strings.Join(sources, ", "))
	}
	service, close, err := options.Open(cmd.Context(), source)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close connections: %w", closeErr))
		}
	}()
	result, err := run(service)
	if result != nil {
		data, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return encodeErr
		}
		var safe any
		if encodeErr = json.Unmarshal(data, &safe); encodeErr != nil {
			return encodeErr
		}
		redact(safe)
		if rows, ok := safe.([]any); ok && len(rows) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "[]")
			return err
		}
		formatted, printErr := clicky.Format(safe, clicky.Flags.FormatOptions)
		if printErr != nil {
			return printErr
		}
		if _, printErr = fmt.Fprintln(cmd.OutOrStdout(), formatted); printErr != nil {
			return printErr
		}
	}
	return err
}

func editModel(cmd *cobra.Command, f *ConnectionFlags, c *models.Connection) error {
	f.Type = c.Type
	allowed := &cobra.Command{}
	bindCommon(allowed, &ConnectionFlags{})
	addTypeSpecificFlags(allowed, &ConnectionFlags{}, c.Type)
	var invalid error
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if flag.Name != "interactive" && flag.Name != "source" && allowed.Flags().Lookup(flag.Name) == nil && cmd.LocalFlags().Lookup(flag.Name) != nil {
			invalid = fmt.Errorf("--%s is not supported for %s connections", flag.Name, c.Type)
		}
	})
	if invalid != nil {
		return invalid
	}
	// Apply each supplied field through the same mapper used by add.
	baseline := &ConnectionFlags{Type: c.Type}
	old, err := BuildConnectionFromFlags(baseline)
	if err != nil {
		return err
	}
	next, err := BuildConnectionFromFlags(f)
	if err != nil {
		return err
	}
	common := map[string]*string{"name": &c.Name, "namespace": &c.Namespace, "url": &c.URL, "username": &c.Username, "password": &c.Password, "certificate": &c.Certificate}
	values := map[string]string{"name": f.Name, "namespace": f.Namespace, "url": f.URL, "username": f.Username, "password": f.Password, "certificate": f.Certificate}
	common["layer"], values["layer"] = &c.Layer, f.Layer
	for key, ptr := range common {
		if cmd.Flags().Changed(key) {
			*ptr = values[key]
		}
	}
	if cmd.Flags().Changed("insecure-tls") {
		c.InsecureTLS = f.InsecureTLS
		if c.Type == models.ConnectionTypeHTTP {
			c.SetProperty("insecure_tls", fmt.Sprint(f.InsecureTLS))
		}
	}
	c.Properties = maps.Clone(c.Properties)
	if c.Properties == nil {
		c.Properties = map[string]string{}
	}
	for key, value := range next.Properties {
		if old.Properties[key] != value {
			c.Properties[key] = value
		}
	}
	for flag, key := range propertyFlags {
		if cmd.Flags().Changed(flag) {
			c.Properties[key] = next.Properties[key]
		}
	}
	if next.Password != old.Password && !cmd.Flags().Changed("password") {
		c.Password = next.Password
	}
	if next.Username != old.Username && !cmd.Flags().Changed("username") {
		c.Username = next.Username
	}
	if next.Certificate != old.Certificate && !cmd.Flags().Changed("certificate") {
		c.Certificate = next.Certificate
	}
	if cmd.Flags().Changed("use-path-style") {
		c.Properties["usePathStyle"] = fmt.Sprint(f.UsePathStyle)
	}
	if cmd.Flags().Changed("host") || cmd.Flags().Changed("database") {
		if !cmd.Flags().Changed("host") {
			f.Host = c.Properties["host"]
		}
		if !cmd.Flags().Changed("database") {
			f.Database = c.Properties["database"]
		}
		mapped, err := BuildConnectionFromFlags(f)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("url") {
			c.URL = mapped.URL
		}
	}
	return applyProperties(c, f.Properties)
}

var propertyFlags = map[string]string{
	"region": "region", "profile": "profile", "key-id": "keyID", "bucket": "bucket", "use-path-style": "usePathStyle", "tenant-id": "tenant",
	"host": "host", "database": "database", "replica-set": "replica_set", "bot-name": "BotName", "color": "Color", "icon": "Icon", "thread-ts": "ThreadTS", "title": "Title",
	"port": "port", "from-address": "from", "from-name": "fromname", "subject": "subject", "auth": "auth", "topic": "topic", "ref": "ref", "path": "path", "share": "share",
	"bearer": "bearer", "oauth-client-id": "clientID", "oauth-client-secret": "clientSecret", "oauth-token-url": "tokenURL", "oauth-scopes": "scopes", "tls-ca": "ca", "tls-cert": "cert", "tls-key": "key", "timestamp-url": "timestampUrl", "model": "model",
}

func applyProperties(c *models.Connection, properties []string) error {
	if c.Properties == nil {
		c.Properties = map[string]string{}
	}
	for _, property := range properties {
		key, value, ok := strings.Cut(property, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid property %q: expected key=value", text.NewSensitiveString(property))
		}
		c.Properties[key] = value
	}
	return nil
}

func redact(value any) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			redact(item)
		}
	case map[string]any:
		for key, item := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || slices.Contains([]string{"certificate", "key", "bearer", "apikey", "api_key", "accesskey", "access_key", "privatekey", "private_key", "pwd", "passwd"}, lower) {
				if raw, ok := item.(string); ok {
					v[key] = text.NewSensitiveString(raw)
				} else {
					v[key] = text.NewSensitiveString("")
				}
			} else if key == "url" {
				if raw, ok := item.(string); ok {
					v[key] = redactURL(raw)
				}
			} else {
				redact(item)
			}
		}
	}
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && u.Scheme != "" {
		u.User = nil
		q := u.Query()
		for key := range q {
			entry := map[string]any{key: q.Get(key)}
			redact(entry)
			if _, sensitive := entry[key].(text.SensitiveString); sensitive {
				q.Set(key, "redacted")
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	parts := strings.Split(raw, ";")
	for i, part := range parts {
		key, _, ok := strings.Cut(part, "=")
		if ok {
			entry := map[string]any{strings.TrimSpace(key): "value"}
			redact(entry)
			if _, sensitive := entry[strings.TrimSpace(key)].(text.SensitiveString); sensitive {
				parts[i] = key + "=redacted"
			}
		}
	}
	return strings.Join(parts, ";")
}
