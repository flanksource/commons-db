// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Ported from cloud.google.com/go/cloudsqlconn v1.22.1
// (internal/cloudsql/tls_verify.go,
// https://github.com/GoogleCloudPlatform/cloud-sql-go-connector), modified to use
// fmt errors instead of the connector's errtype package.

package cloudsql

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// verifyPeerCertificateFunc builds the server certificate check that replaces
// the default TLS verification, as Cloud SQL server certificates do not follow
// standard hostname rules:
//
//  1. The certificate chain must verify against the instance's server CA.
//  2. A certificate for an instance with a DNS name must have a SAN matching
//     that name, or else a Subject.CN equal to "project:instance".
//  3. An instance without a DNS name is checked on Subject.CN alone.
func verifyPeerCertificateFunc(serverName string, name connName, roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("cloudsql: %s: no server certificate to verify", name)
		}
		certs := make([]*x509.Certificate, 0, len(rawCerts))
		for _, raw := range rawCerts {
			cert, err := x509.ParseCertificate(raw)
			if err != nil {
				return fmt.Errorf("cloudsql: %s: parsing server certificate: %w", name, err)
			}
			certs = append(certs, cert)
		}
		serverCert := certs[0]

		if _, err := serverCert.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
			return fmt.Errorf("cloudsql: %s: verifying server certificate: %w",
				name, &tls.CertificateVerificationError{UnverifiedCertificates: certs, Err: err})
		}
		if serverName == "" {
			return verifyCommonName(name, serverCert)
		}
		hostErr := serverCert.VerifyHostname(serverName)
		if hostErr == nil || verifyCommonName(name, serverCert) == nil {
			return nil
		}
		return fmt.Errorf("cloudsql: %s: %w", name,
			&tls.CertificateVerificationError{UnverifiedCertificates: certs, Err: hostErr})
	}
}

func verifyCommonName(name connName, cert *x509.Certificate) error {
	want := name.project + ":" + name.name
	if cert.Subject.CommonName != want {
		return fmt.Errorf("cloudsql: %s: server certificate has CN %q, expected %q", name, cert.Subject.CommonName, want)
	}
	return nil
}
