// Package awstest starts the ministack AWS emulator for integration specs.
//
// The emulator runs in Docker, possibly on a remote host, so callers must use
// the endpoint it reports rather than assuming localhost.
package awstest

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/commons/http/middlewares/awssigv4"
	"github.com/flanksource/deps/start"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
)

const serviceName = "ministack"

// Emulator is a running ministack instance.
type Emulator struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string

	instances map[string]*start.Instance
}

// Start launches an ephemeral ministack. It is an error when Docker is
// unavailable; there is no fallback. Any instance that did start is stopped
// before the error is returned.
func Start(ctx context.Context) (*Emulator, error) {
	// ministack keeps no data directory, so the spec defines no docker volume and
	// start rejects any volume mode for it.
	instances, err := start.StartServices(ctx, []start.ServiceRequest{{Name: serviceName}})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start %s (Docker is required for AWS integration specs): %w", serviceName, err), start.StopServices(ctx, instances))
	}
	emulator, err := fromInstance(instances[serviceName])
	if err != nil {
		return nil, errors.Join(err, start.StopServices(ctx, instances))
	}
	emulator.instances = instances
	return emulator, nil
}

func fromInstance(instance *start.Instance) (*Emulator, error) {
	if instance == nil {
		return nil, fmt.Errorf("%s did not report an instance", serviceName)
	}
	conn := instance.Connection
	e := &Emulator{Endpoint: conn.Properties["endpoint"], Region: conn.Properties["region"], AccessKey: conn.Username, SecretKey: conn.Password}
	if e.Endpoint == "" || e.Region == "" || e.AccessKey == "" || e.SecretKey == "" {
		return nil, fmt.Errorf("%s connection is missing endpoint, region or credentials: %+v", serviceName, conn.Properties)
	}
	return e, nil
}

// Stop stops the emulator.
func (e *Emulator) Stop(ctx context.Context) error {
	return start.StopServices(ctx, e.instances)
}

// SessionOptions are the options for a cloudaws.Session talking to the emulator.
func (e *Emulator) SessionOptions() cloudaws.SessionOptions {
	return cloudaws.SessionOptions{
		Region:   e.Region,
		Endpoint: e.Endpoint,
		ServiceEndpoints: map[string]string{
			cloudaws.ServiceSTS: e.Endpoint, cloudaws.ServiceKMS: e.Endpoint, cloudaws.ServiceEKS: e.Endpoint,
		},
		Credentials: awssigv4.NewStaticCredentialsProvider(awssigv4.Credentials{
			AccessKeyID: e.AccessKey, SecretAccessKey: e.SecretKey,
		}),
	}
}
