package crd

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/flanksource/commons/collections"
	"github.com/google/uuid"
	"github.com/samber/lo"

	"github.com/flanksource/commons-db/models"
)

// ToModel is the models.Connection obj describes, as mission-control persists
// it: the typed spec mapped onto type, url, credentials and properties, with
// the spec's own properties merged over them. obj must pass Validate. The
// caller sets Source.
func ToModel(obj *Connection) (models.Connection, error) {
	if err := Validate(obj); err != nil {
		return models.Connection{}, err
	}
	dbObj := models.Connection{
		Name:        obj.Name,
		Namespace:   obj.Namespace,
		Type:        obj.Spec.Type,
		URL:         obj.Spec.URL.String(),
		Username:    obj.Spec.Username.String(),
		Password:    obj.Spec.Password.String(),
		Certificate: obj.Spec.Certificate.String(),
		InsecureTLS: obj.Spec.InsecureTLS,
	}

	if obj.GetUID() != "" {
		uid, err := uuid.Parse(string(obj.GetUID()))
		if err != nil {
			return dbObj, fmt.Errorf("failed to parse uid: %w", err)
		}
		dbObj.ID = uid
	}

	connectionFromSpec(obj.Spec, &dbObj)

	if len(obj.Spec.Properties) != 0 {
		dbObj.Properties = collections.MergeMap(obj.Spec.Properties, dbObj.Properties)
	}

	return dbObj, nil
}

// Variants are the typed connections spec sets, by their JSON field name.
func Variants(spec ConnectionSpec) []string {
	var variants []string
	value, specType := reflect.ValueOf(spec), reflect.TypeOf(spec)
	for i := range specType.NumField() {
		field := specType.Field(i)
		if field.Type.Kind() != reflect.Pointer || value.Field(i).IsNil() {
			continue
		}
		variants = append(variants, strings.Split(field.Tag.Get("json"), ",")[0])
	}
	return variants
}

// Validate refuses a connection that names no kind of connection, neither a
// typed variant nor the deprecated spec.type, and one that sets two variants.
func Validate(obj *Connection) error {
	if obj.Kind != "" && obj.Kind != Kind {
		return fmt.Errorf("kind is %q, not %s", obj.Kind, Kind)
	}
	if obj.APIVersion != "" && obj.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion is %q, not %s", obj.APIVersion, APIVersion)
	}
	switch variants := Variants(obj.Spec); {
	case len(variants) > 1:
		return fmt.Errorf("spec sets %s: a connection is one kind of connection", strings.Join(variants, " and "))
	case len(variants) == 0 && obj.Spec.Type == "":
		return fmt.Errorf("spec names no kind of connection: set one of its typed fields, such as postgres or aws")
	}
	return nil
}

func mergeConnectionModels(source, override models.Connection) models.Connection {
	source.ID = lo.CoalesceOrEmpty(override.ID, source.ID)
	source.Name = lo.CoalesceOrEmpty(override.Name, source.Name)
	source.Namespace = lo.CoalesceOrEmpty(override.Namespace, source.Namespace)
	source.Source = lo.CoalesceOrEmpty(override.Source, source.Source)
	source.Type = lo.CoalesceOrEmpty(override.Type, source.Type)
	source.URL = lo.CoalesceOrEmpty(override.URL, source.URL)
	source.Username = lo.CoalesceOrEmpty(override.Username, source.Username)
	source.Password = lo.CoalesceOrEmpty(override.Password, source.Password)
	source.Certificate = lo.CoalesceOrEmpty(override.Certificate, source.Certificate)
	source.InsecureTLS = override.InsecureTLS
	source.CreatedAt = lo.CoalesceOrEmpty(override.CreatedAt, source.CreatedAt)
	source.UpdatedAt = lo.CoalesceOrEmpty(override.UpdatedAt, source.UpdatedAt)
	source.CreatedBy = lo.CoalesceOrEmpty(override.CreatedBy, source.CreatedBy)
	source.Properties = collections.MergeMap(source.Properties, override.Properties)
	return source
}

func (c ConnectionOpenSearch) toModel() models.Connection {
	return models.Connection{
		Type:     models.ConnectionTypeOpenSearch,
		URL:      lo.FirstOrEmpty(c.URLs),
		Username: c.Username.String(),
		Password: c.Password.String(),
		Properties: map[string]string{
			"urls":         strings.Join(c.URLs, ","),
			"index":        c.Index,
			"insecure_tls": strconv.FormatBool(c.InsecureSkipVerify),
		},
	}
}

func stringMapToString(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func connectionFromSpec(spec ConnectionSpec, dbObj *models.Connection) {
	connectionFromCloudSpec(spec, dbObj)
	connectionFromServiceSpec(spec, dbObj)
	connectionFromDatabaseSpec(spec, dbObj)
	connectionFromNotificationSpec(spec, dbObj)
}

func connectionFromCloudSpec(spec ConnectionSpec, dbObj *models.Connection) {
	if spec.AWS != nil {
		dbObj.Type = models.ConnectionTypeAWS
		dbObj.URL = spec.AWS.URL.String()
		dbObj.Username = spec.AWS.AccessKey.String()
		dbObj.Password = spec.AWS.SecretKey.String()
		dbObj.InsecureTLS = spec.AWS.InsecureTLS
		dbObj.Properties = map[string]string{
			"region":       spec.AWS.Region,
			"profile":      spec.AWS.Profile,
			"sessionToken": spec.AWS.SessionToken.String(),
			"assumeRole":   spec.AWS.AssumeRole,
		}
	}

	if spec.AWSKMS != nil {
		dbObj.Type = models.ConnectionTypeAWSKMS
		dbObj.URL = spec.AWSKMS.URL.String()
		dbObj.Username = spec.AWSKMS.AccessKey.String()
		dbObj.Password = spec.AWSKMS.SecretKey.String()
		dbObj.InsecureTLS = spec.AWSKMS.InsecureTLS
		dbObj.Properties = map[string]string{
			"keyID":        spec.AWSKMS.KeyID,
			"region":       spec.AWSKMS.Region,
			"profile":      spec.AWSKMS.Profile,
			"sessionToken": spec.AWSKMS.SessionToken.String(),
			"assumeRole":   spec.AWSKMS.AssumeRole,
		}
	}

	if spec.S3 != nil {
		dbObj.Type = models.ConnectionTypeS3
		dbObj.URL = spec.S3.URL.String()
		dbObj.Username = spec.S3.AccessKey.String()
		dbObj.Password = spec.S3.SecretKey.String()
		dbObj.InsecureTLS = spec.S3.InsecureTLS
		dbObj.Properties = map[string]string{
			"bucket":       spec.S3.Bucket,
			"region":       spec.S3.Region,
			"profile":      spec.S3.Profile,
			"usePathStyle": strconv.FormatBool(spec.S3.UsePathStyle),
			"sessionToken": spec.S3.SessionToken.String(),
			"assumeRole":   spec.S3.AssumeRole,
		}
	}

	if spec.Azure != nil {
		dbObj.Type = models.ConnectionTypeAzure
		dbObj.Username = spec.Azure.ClientID.String()
		dbObj.Password = spec.Azure.ClientSecret.String()
		dbObj.Properties = map[string]string{
			"tenant": spec.Azure.TenantID.String(),
		}
	}

	if spec.AzureKeyVault != nil {
		dbObj.Type = models.ConnectionTypeAzureKeyVault
		dbObj.Username = spec.AzureKeyVault.ClientID.String()
		dbObj.Password = spec.AzureKeyVault.ClientSecret.String()
		dbObj.Properties = map[string]string{
			"tenant": spec.AzureKeyVault.TenantID.String(),
			"keyID":  spec.AzureKeyVault.KeyID,
		}
	}

	if spec.GCP != nil {
		dbObj.Type = models.ConnectionTypeGCP
		dbObj.Certificate = spec.GCP.Certificate.String()
		dbObj.URL = spec.GCP.Endpoint.String()
		dbObj.Properties = map[string]string{
			"project": spec.GCP.Project,
		}
	}

	if spec.GCS != nil {
		dbObj.Type = models.ConnectionTypeGCS
		dbObj.Certificate = spec.GCS.Certificate.String()
		dbObj.URL = spec.GCS.Endpoint.String()
		dbObj.Properties = map[string]string{
			"bucket":  spec.GCS.Bucket,
			"project": spec.GCS.Project,
		}
	}

	if spec.GCPKMS != nil {
		dbObj.Type = models.ConnectionTypeGCPKMS
		dbObj.Certificate = spec.GCPKMS.Certificate.String()
		dbObj.URL = spec.GCPKMS.Endpoint.String()
		dbObj.Properties = map[string]string{
			"keyID":   spec.GCPKMS.KeyID,
			"project": spec.GCPKMS.Project,
		}
	}

	if spec.AzureDevops != nil {
		dbObj.URL = spec.AzureDevops.URL
		dbObj.Type = models.ConnectionTypeAzureDevops
		dbObj.Username = spec.AzureDevops.Organization
		dbObj.Password = spec.AzureDevops.PersonalAccessToken.String()
	}

	if spec.Kubernetes != nil {
		dbObj.Type = models.ConnectionTypeKubernetes
		dbObj.Certificate = spec.Kubernetes.Certificate.String()
	}
}
