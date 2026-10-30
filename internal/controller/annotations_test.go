// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/intstr"

	common "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
)

func monitorCredential() []corev1alpha1.MonitoringCredential {
	return []corev1alpha1.MonitoringCredential{{
		Profile:   corev1alpha1.CredentialProfileMonitor,
		SecretRef: common.SecretRef{Name: "orders-monitor"},
	}}
}

func connection(component, kind string, tls *corev1alpha1.ConnectionTLS) corev1alpha1.ConnectionEndpoint {
	return corev1alpha1.ConnectionEndpoint{
		Component:   component,
		Kind:        kind,
		PodSelector: map[string]string{"core.openeverest.io/instance": "orders"},
		Container:   "mongod",
		Port:        intstr.FromInt32(27017),
		TLS:         tls,
		Auth:        &corev1alpha1.MetricsEndpointAuth{CredentialProfile: corev1alpha1.CredentialProfileMonitor},
	}
}

func caFrom(secret string) *common.CABundleSource {
	return &common.CABundleSource{SecretKeyRef: &common.SecretKeyRef{Name: secret, Key: "ca.crt"}}
}

func mongoAnnotations(extra map[string]string) map[string]string {
	a := map[string]string{
		"coroot.com/mongodb-scrape":                                 "true",
		"coroot.com/mongodb-scrape-port":                            "27017",
		"coroot.com/mongodb-scrape-credentials-secret-name":         "orders-monitor",
		"coroot.com/mongodb-scrape-credentials-secret-username-key": "username",
		"coroot.com/mongodb-scrape-credentials-secret-password-key": "password",
	}
	maps.Copy(a, extra)
	return a
}

func TestPodMetadataFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		connections []corev1alpha1.ConnectionEndpoint
		credentials []corev1alpha1.MonitoringCredential
		want        []monitoringv1alpha1.ComponentPodMetadata
		wantNotes   []string
	}{
		{
			name:        "plaintext MongoDB",
			connections: []corev1alpha1.ConnectionEndpoint{connection("engine", "mongodb", nil)},
			credentials: monitorCredential(),
			want:        []monitoringv1alpha1.ComponentPodMetadata{{Component: "engine", Annotations: mongoAnnotations(nil)}},
		},
		{
			name: "TLS accepted but not required is configured all the same",
			connections: []corev1alpha1.ConnectionEndpoint{
				connection("engine", "mongodb", &corev1alpha1.ConnectionTLS{CA: caFrom("orders-ssl")}),
			},
			credentials: monitorCredential(),
			want: []monitoringv1alpha1.ComponentPodMetadata{{Component: "engine", Annotations: mongoAnnotations(map[string]string{
				"coroot.com/mongodb-scrape-param-tls":         "true",
				"coroot.com/mongodb-scrape-tls-secret-name":   "orders-ssl",
				"coroot.com/mongodb-scrape-tls-secret-ca-key": "ca.crt",
			})}},
		},
		{
			name: "TLS without a CA skips verification",
			connections: []corev1alpha1.ConnectionEndpoint{
				connection("engine", "mongodb", &corev1alpha1.ConnectionTLS{Required: true}),
			},
			credentials: monitorCredential(),
			want: []monitoringv1alpha1.ComponentPodMetadata{{Component: "engine", Annotations: mongoAnnotations(map[string]string{
				"coroot.com/mongodb-scrape-param-tls": "skip-verify",
			})}},
		},
		{
			name: "client certificate in the CA's Secret",
			connections: []corev1alpha1.ConnectionEndpoint{
				connection("engine", "mongodb", &corev1alpha1.ConnectionTLS{
					CA:                         caFrom("orders-ssl"),
					ClientCertificateSecretRef: &common.SecretRef{Name: "orders-ssl"},
				}),
			},
			credentials: monitorCredential(),
			want: []monitoringv1alpha1.ComponentPodMetadata{{Component: "engine", Annotations: mongoAnnotations(map[string]string{
				"coroot.com/mongodb-scrape-param-tls":           "true",
				"coroot.com/mongodb-scrape-tls-secret-name":     "orders-ssl",
				"coroot.com/mongodb-scrape-tls-secret-ca-key":   "ca.crt",
				"coroot.com/mongodb-scrape-tls-secret-cert-key": "tls.crt",
				"coroot.com/mongodb-scrape-tls-secret-key-key":  "tls.key",
			})}},
		},
		{
			name: "PostgreSQL verifies the CA through sslmode",
			connections: []corev1alpha1.ConnectionEndpoint{
				connection("engine", "postgresql", &corev1alpha1.ConnectionTLS{CA: caFrom("pg-ca")}),
			},
			credentials: monitorCredential(),
			want: []monitoringv1alpha1.ComponentPodMetadata{{Component: "engine", Annotations: map[string]string{
				"coroot.com/postgres-scrape":                                 "true",
				"coroot.com/postgres-scrape-port":                            "27017",
				"coroot.com/postgres-scrape-credentials-secret-name":         "orders-monitor",
				"coroot.com/postgres-scrape-credentials-secret-username-key": "username",
				"coroot.com/postgres-scrape-credentials-secret-password-key": "password",
				"coroot.com/postgres-scrape-param-sslmode":                   "verify-ca",
				"coroot.com/postgres-scrape-tls-secret-name":                 "pg-ca",
				"coroot.com/postgres-scrape-tls-secret-ca-key":               "ca.crt",
			}}},
		},
		{
			name: "components sorted, unsupported ones skipped with a note",
			connections: []corev1alpha1.ConnectionEndpoint{
				connection("engine", "mongodb", nil),
				connection("configServer", "mongodb", nil),
				connection("cache", "redis", nil),
			},
			credentials: monitorCredential(),
			want: []monitoringv1alpha1.ComponentPodMetadata{
				{Component: "configServer", Annotations: mongoAnnotations(nil)},
				{Component: "engine", Annotations: mongoAnnotations(nil)},
			},
			wantNotes: []string{`component cache: coroot has no integration for kind "redis"`},
		},
		{
			name: "CA and client certificate in different Secrets",
			connections: []corev1alpha1.ConnectionEndpoint{
				connection("engine", "mongodb", &corev1alpha1.ConnectionTLS{
					CA:                         caFrom("orders-ca"),
					ClientCertificateSecretRef: &common.SecretRef{Name: "orders-client"},
				}),
			},
			credentials: monitorCredential(),
			wantNotes:   []string{`component engine: coroot reads the CA and the client certificate from one Secret, got "orders-ca" and "orders-client"`},
		},
		{
			name:        "credential profile not published",
			connections: []corev1alpha1.ConnectionEndpoint{connection("engine", "mongodb", nil)},
			wantNotes:   []string{`component engine: credential profile "monitor" is not published`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, notes := podMetadataFor(&corev1alpha1.MonitoringSources{Connections: tt.connections, Credentials: tt.credentials})

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantNotes, notes)
		})
	}
}
