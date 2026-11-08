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
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	common "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/monitoring"
)

const testNamespace = "team-a"

// crdModels holds the OpenAPI models of the monitoring CRDs shipped with the
// core module in use, parsed once for all tests.
var crdModels = sync.OnceValues(loadCRDModels) //nolint:gochecknoglobals // parsed once per test binary

func loadCRDModels() (map[string]*spec.Schema, error) {
	out, err := exec.CommandContext(context.Background(), "go", "list", "-m", "-f", "{{.Dir}}", "github.com/openeverest/openeverest/v2").Output()
	if err != nil {
		return nil, err
	}
	crds := os.DirFS(filepath.Join(strings.TrimSpace(string(out)), "config", "crd", "bases"))
	models := map[string]*spec.Schema{}
	for _, plural := range []string{"monitoringclasses", "monitoringdestinations", "monitoringbindings"} {
		raw, err := fs.ReadFile(crds, "monitoring.openeverest.io_"+plural+".yaml")
		if err != nil {
			return nil, err
		}
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(raw, crd); err != nil {
			return nil, err
		}
		for _, v := range crd.Spec.Versions {
			js, err := json.Marshal(v.Schema.OpenAPIV3Schema)
			if err != nil {
				return nil, err
			}
			s := &spec.Schema{}
			if err := json.Unmarshal(js, s); err != nil {
				return nil, err
			}
			s.AddExtension("x-kubernetes-group-version-kind", []any{
				map[string]any{"group": crd.Spec.Group, "version": v.Name, "kind": crd.Spec.Names.Kind},
			})
			models[crd.Spec.Group+"."+v.Name+"."+crd.Spec.Names.Kind] = s
		}
	}
	return models, nil
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client { //nolint:ireturn // the fake is used through the client interface
	t.Helper()
	models, err := crdModels()
	require.NoError(t, err)
	tc, err := managedfields.NewTypeConverter(models, false)
	require.NoError(t, err)
	s := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(s))
	require.NoError(t, monitoringv1alpha1.AddToScheme(s))
	return fake.NewClientBuilder().
		WithScheme(s).
		WithTypeConverters(tc, managedfields.NewDeducedTypeConverter()).
		WithObjects(objs...).
		WithStatusSubresource(&corev1alpha1.Instance{}, &monitoringv1alpha1.MonitoringClass{}, &monitoringv1alpha1.MonitoringBinding{}, &monitoringv1alpha1.MonitoringDestination{}).
		Build()
}

func testInstance(sources *corev1alpha1.MonitoringSources) *corev1alpha1.Instance {
	in := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: testNamespace, UID: "uid-orders"}}
	if sources != nil {
		in.Status.Monitoring = &corev1alpha1.InstanceMonitoringStatus{Sources: sources}
	}
	return in
}

func testBinding(accepted metav1.ConditionStatus, held []monitoringv1alpha1.ComponentPodMetadata) *monitoringv1alpha1.MonitoringBinding {
	b := &monitoringv1alpha1.MonitoringBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orders-coroot-1a2b3c4d", Namespace: testNamespace, Generation: 1,
			Labels: monitoring.BindingLabels("orders", "coroot", "coroot", "coroot"),
		},
		Spec: monitoringv1alpha1.MonitoringBindingSpec{
			InstanceRef:    common.ObjectRef{Name: "orders"},
			DestinationRef: common.MonitoringDestinationRef{Kind: "MonitoringDestination", Name: "coroot"},
			ClassRef:       &common.ObjectRef{Name: "coroot"},
			ExecutionMode:  corev1alpha1.MonitoringExecutionModeExtensionManaged,
		},
		Status: monitoringv1alpha1.MonitoringBindingStatus{PodMetadata: held},
	}
	if accepted != "" {
		b.Status.Conditions = []metav1.Condition{{
			Type: monitoringv1alpha1.MonitoringBindingConditionAccepted, Status: accepted, Reason: "Test",
			ObservedGeneration: 1, LastTransitionTime: metav1.Now(),
		}}
	}
	return b
}

// testControllerName is the controller name the controller under test
// claims classes by.
const testControllerName = "openeverest.io/monitoring-coroot"

func testController(c client.Client) monitoring.ClassController {
	return monitoring.ClassController{Client: c, FieldOwner: FieldOwner, ControllerName: testControllerName}
}

// testClass is a class named name whose controllerName is controllerName.
func testClass(name, controllerName string) *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ControllerName: controllerName,
			ExtensionManaged: &monitoringv1alpha1.ExtensionManagedFulfilment{
				Requires:    monitoringv1alpha1.SourceRequirements{Connection: true, Kinds: []string{"mongodb", "postgresql"}},
				PodMetadata: &monitoringv1alpha1.PodMetadataPolicy{AnnotationPrefixes: []string{"coroot.com/"}},
			},
			AuthModes: []common.AuthMode{{Name: common.AuthModeAnonymous}},
		},
	}
}

func testDestination() *monitoringv1alpha1.MonitoringDestination {
	return &monitoringv1alpha1.MonitoringDestination{
		ObjectMeta: metav1.ObjectMeta{Name: "coroot", Namespace: testNamespace},
		Spec: monitoringv1alpha1.MonitoringDestinationSpec{
			ClassRef:       common.ObjectRef{Name: "coroot"},
			Authentication: common.Authentication{Mode: common.AuthModeAnonymous},
		},
	}
}

func TestReconcileBinding(t *testing.T) {
	t.Parallel()

	served := &corev1alpha1.MonitoringSources{
		Credentials: monitorCredential(),
		Connections: []corev1alpha1.ConnectionEndpoint{connection("engine", "mongodb", nil)},
	}
	requested := []monitoringv1alpha1.ComponentPodMetadata{{Component: "engine", Annotations: mongoAnnotations(nil)}}
	held := []monitoringv1alpha1.ComponentPodMetadata{{Component: "engine", Annotations: map[string]string{"coroot.com/mongodb-scrape": "true"}}}

	tests := []struct {
		name            string
		binding         *monitoringv1alpha1.MonitoringBinding
		instance        *corev1alpha1.Instance
		wantReason      monitoringv1alpha1.ConfiguredReason
		wantPodMetadata []monitoringv1alpha1.ComponentPodMetadata
	}{
		{
			name:            "accepted binding requests the annotations",
			binding:         testBinding(metav1.ConditionTrue, nil),
			instance:        testInstance(served),
			wantReason:      monitoringv1alpha1.ReasonConfigured,
			wantPodMetadata: requested,
		},
		{
			name:            "pending binding holds the previous request",
			binding:         testBinding("", held),
			instance:        testInstance(served),
			wantReason:      monitoringv1alpha1.ReasonPending,
			wantPodMetadata: held,
		},
		{
			name:       "rejected binding withdraws the request",
			binding:    testBinding(metav1.ConditionFalse, held),
			instance:   testInstance(served),
			wantReason: monitoringv1alpha1.ReasonNotAccepted,
		},
		{
			name:            "no connections published yet: the request is held",
			binding:         testBinding(metav1.ConditionTrue, held),
			instance:        testInstance(nil),
			wantReason:      monitoringv1alpha1.ReasonEndpointNotServing,
			wantPodMetadata: held,
		},
		{
			name:    "nothing Coroot can connect to",
			binding: testBinding(metav1.ConditionTrue, held),
			instance: testInstance(&corev1alpha1.MonitoringSources{
				Credentials: monitorCredential(),
				Connections: []corev1alpha1.ConnectionEndpoint{connection("cache", "redis", nil)},
			}),
			wantReason: monitoringv1alpha1.ReasonRenderFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			held := tt.binding.Status.PodMetadata
			tt.binding.Status.PodMetadata = nil
			c := newFakeClient(t, testClass("coroot", testControllerName), tt.binding, tt.instance, testDestination())
			r := &bindingReconciler{client: c, cc: testController(c)}
			if held != nil {
				// What an earlier reconcile requested, under the controller's field owner.
				b := &monitoringv1alpha1.MonitoringBinding{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt.binding), b))
				require.NoError(t, r.cc.SetConfiguredWithPodMetadata(ctx, b, monitoringv1alpha1.ReasonConfigured, "", held))
			}

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tt.binding)})
			require.NoError(t, err)

			got := &monitoringv1alpha1.MonitoringBinding{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt.binding), got))
			configured := meta.FindStatusCondition(got.Status.Conditions, monitoringv1alpha1.MonitoringBindingConditionConfigured)
			require.NotNil(t, configured)
			assert.Equal(t, string(tt.wantReason), configured.Reason)
			assert.Equal(t, tt.wantPodMetadata, got.Status.PodMetadata)
		})
	}
}
