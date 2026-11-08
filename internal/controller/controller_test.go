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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
)

const otherController = "example.com/coroot"

func TestClassReconcilerClaimsClassesNamingTheController(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		class *monitoringv1alpha1.MonitoringClass
		want  bool
	}{
		{name: "the chart's class", class: testClass("coroot", testControllerName), want: true},
		{name: "a second class naming the controller", class: testClass("coroot-eu", testControllerName), want: true},
		{name: "a class of another controller", class: testClass("coroot", otherController)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			c := newFakeClient(t, tt.class)
			r := &classReconciler{client: c, cc: testController(c)}

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tt.class)})
			require.NoError(t, err)

			got := &monitoringv1alpha1.MonitoringClass{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tt.class), got))
			accepted := meta.FindStatusCondition(got.Status.Conditions, monitoringv1alpha1.MonitoringClassConditionAccepted)
			if !tt.want {
				assert.Nil(t, accepted)
				return
			}
			require.NotNil(t, accepted)
			assert.Equal(t, metav1.ConditionTrue, accepted.Status)
			assert.Equal(t, "claimed by "+testControllerName, accepted.Message)
			assert.Equal(t, int64(1), accepted.ObservedGeneration)
		})
	}
}

func TestDestinationReconcilerReportsReadyForServedClasses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		class     string
		wantReady bool
	}{
		{name: "destination of a served class", class: "coroot", wantReady: true},
		{name: "destination of a class of another controller", class: "coroot-other"},
		{name: "destination of a class that does not exist", class: "datadog"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			dst := testDestination()
			dst.Spec.ClassRef.Name = tt.class
			c := newFakeClient(t, testClass("coroot", testControllerName), testClass("coroot-other", otherController), dst)
			r := &destinationReconciler{client: c, cc: testController(c)}

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dst)})
			require.NoError(t, err)

			got := &monitoringv1alpha1.MonitoringDestination{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dst), got))
			ready := meta.FindStatusCondition(got.Status.Conditions, monitoringv1alpha1.MonitoringDestinationConditionReady)
			if !tt.wantReady {
				assert.Nil(t, ready)
				return
			}
			require.NotNil(t, ready)
			assert.Equal(t, metav1.ConditionTrue, ready.Status)
		})
	}
}

func TestDestinationWatchEnqueuesDestinationsOfServedClasses(t *testing.T) {
	t.Parallel()
	destination := func(name, class string) client.Object {
		dst := testDestination()
		dst.Name = name
		dst.Spec.ClassRef.Name = class
		return dst
	}
	served, other := testClass("coroot", testControllerName), testClass("coroot-other", otherController)
	c := newFakeClient(t, served.DeepCopy(), other.DeepCopy(),
		destination("coroot", "coroot"),
		destination("coroot-2", "coroot"),
		destination("other", "coroot-other"),
	)
	r := &destinationReconciler{client: c, cc: testController(c)}
	names := func(reqs []reconcile.Request) []string {
		out := make([]string, 0, len(reqs))
		for _, req := range reqs {
			out = append(out, req.Name)
		}
		return out
	}

	assert.ElementsMatch(t, []string{"coroot", "coroot-2"}, names(r.destinationsOfClass(t.Context(), served)))
	assert.Empty(t, r.destinationsOfClass(t.Context(), other))
}

func TestReconcileIgnoresBindingsOfOtherControllers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	b := testBinding(metav1.ConditionTrue, nil)
	b.Spec.ClassRef.Name = "coroot-other"
	b.Labels[monitoringv1alpha1.BindingClassLabel] = "coroot-other"
	c := newFakeClient(t, testClass("coroot", testControllerName), testClass("coroot-other", otherController), b, testInstance(nil), testDestination())
	r := &bindingReconciler{client: c, cc: testController(c)}

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b)})
	require.NoError(t, err)

	got := &monitoringv1alpha1.MonitoringBinding{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(b), got))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, monitoringv1alpha1.MonitoringBindingConditionConfigured))
}
