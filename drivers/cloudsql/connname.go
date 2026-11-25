// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Ported from cloud.google.com/go/cloudsqlconn v1.22.1 (instance/conn_name.go,
// https://github.com/GoogleCloudPlatform/cloud-sql-go-connector), reduced to the
// parts the dialer needs and modified to use fmt errors.

package cloudsql

import (
	"fmt"
	"regexp"
)

// connNameRegex matches <PROJECT>:<REGION>:<INSTANCE>, where the project may be
// domain-scoped ("example.com:project").
var connNameRegex = regexp.MustCompile(`^([^:]+(?::[^:]+)?):([^:]+):([^:]+)$`)

// connName is a parsed instance connection name.
type connName struct {
	project, region, name string
}

func parseConnName(raw string) (connName, error) {
	match := connNameRegex.FindStringSubmatch(raw)
	if match == nil {
		return connName{}, fmt.Errorf("cloudsql: invalid instance connection name %q, expected PROJECT:REGION:INSTANCE", raw)
	}
	return connName{project: match[1], region: match[2], name: match[3]}, nil
}

func (c connName) String() string {
	return fmt.Sprintf("%s:%s:%s", c.project, c.region, c.name)
}
