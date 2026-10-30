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
	"fmt"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/intstr"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
)

// AnnotationPrefix is the only annotation prefix the class requests.
const AnnotationPrefix = "coroot.com/"

// Database names of coroot-cluster-agent's scrape annotations.
const (
	corootMongoDB  = "mongodb"
	corootPostgres = "postgres"
)

// corootDatabases maps OpenEverest engine kinds to the database names of
// coroot-cluster-agent's scrape annotations.
var corootDatabases = map[string]string{ //nolint:gochecknoglobals // read-only lookup table
	"mongodb":    corootMongoDB,
	"postgresql": corootPostgres,
}

// Keys of kubernetes.io/tls Secrets, which client certificates are
// published as.
const (
	tlsCertKey = "tls.crt"
	tlsKeyKey  = "tls.key"
)

// podMetadataFor turns the Instance's connection endpoints into the
// annotations coroot-cluster-agent discovers databases by, one entry per
// component. Endpoints it cannot express are skipped and explained in notes.
func podMetadataFor(sources *corev1alpha1.MonitoringSources) ([]monitoringv1alpha1.ComponentPodMetadata, []string) {
	var out []monitoringv1alpha1.ComponentPodMetadata
	var notes []string
	for _, ep := range sources.Connections {
		annotations, err := annotationsFor(sources, ep)
		if err != nil {
			notes = append(notes, fmt.Sprintf("component %s: %v", ep.Component, err))
			continue
		}
		out = append(out, monitoringv1alpha1.ComponentPodMetadata{Component: ep.Component, Annotations: annotations})
	}
	slices.SortFunc(out, func(a, b monitoringv1alpha1.ComponentPodMetadata) int {
		return strings.Compare(a.Component, b.Component)
	})
	return out, notes
}

func annotationsFor(sources *corev1alpha1.MonitoringSources, ep corev1alpha1.ConnectionEndpoint) (map[string]string, error) {
	database, ok := corootDatabases[ep.Kind]
	if !ok {
		return nil, fmt.Errorf("coroot has no integration for kind %q", ep.Kind)
	}
	if ep.Port.Type != intstr.Int || ep.Port.IntVal <= 0 {
		return nil, fmt.Errorf("coroot needs a port number, got %q", ep.Port.String())
	}
	key := func(suffix string) string { return AnnotationPrefix + database + "-scrape" + suffix }

	a := map[string]string{
		key(""):      "true",
		key("-port"): strconv.Itoa(int(ep.Port.IntVal)),
	}
	if ep.Auth != nil {
		i := slices.IndexFunc(sources.Credentials, func(c corev1alpha1.MonitoringCredential) bool {
			return c.Profile == ep.Auth.CredentialProfile
		})
		if i < 0 {
			return nil, fmt.Errorf("credential profile %q is not published", ep.Auth.CredentialProfile)
		}
		a[key("-credentials-secret-name")] = sources.Credentials[i].SecretRef.Name
		a[key("-credentials-secret-username-key")] = corev1alpha1.CredentialSecretKeyUsername
		a[key("-credentials-secret-password-key")] = corev1alpha1.CredentialSecretKeyPassword
	}
	if ep.TLS != nil {
		if err := addTLS(a, key, database, ep.TLS); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// addTLS configures TLS whenever the endpoint accepts it, not only when it
// requires it, so the annotations (and the pods) do not change when TLS is
// tightened. Coroot reads the CA and the client certificate from one Secret.
func addTLS(a map[string]string, key func(string) string, database string, tls *corev1alpha1.ConnectionTLS) error {
	var caSecret, caKey string
	if tls.CA != nil && tls.CA.SecretKeyRef != nil {
		caSecret, caKey = tls.CA.SecretKeyRef.Name, tls.CA.SecretKeyRef.Key
	}
	var clientSecret string
	if tls.ClientCertificateSecretRef != nil {
		clientSecret = tls.ClientCertificateSecretRef.Name
	}
	if caSecret != "" && clientSecret != "" && caSecret != clientSecret {
		return fmt.Errorf("coroot reads the CA and the client certificate from one Secret, got %q and %q", caSecret, clientSecret)
	}

	verified := caSecret != ""
	switch database {
	case corootMongoDB:
		a[key("-param-tls")] = "skip-verify"
		if verified {
			a[key("-param-tls")] = "true"
		}
	case corootPostgres:
		a[key("-param-sslmode")] = "require"
		if verified {
			a[key("-param-sslmode")] = "verify-ca"
		}
	}

	tlsSecret := caSecret
	if tlsSecret == "" {
		tlsSecret = clientSecret
	}
	if tlsSecret != "" {
		a[key("-tls-secret-name")] = tlsSecret
	}
	if caSecret != "" {
		a[key("-tls-secret-ca-key")] = caKey
	}
	if clientSecret != "" {
		a[key("-tls-secret-cert-key")] = tlsCertKey
		a[key("-tls-secret-key-key")] = tlsKeyKey
	}
	return nil
}
