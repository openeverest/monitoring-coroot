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
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/monitoring"
)

type bindingReconciler struct {
	client    client.Client
	cc        monitoring.ClassController
	className string
}

// Reconcile requests the coroot.com/* annotations for the bound Instance's
// connection endpoints on the binding, with Configured. The provider renders
// them on the pods, which rolls them, so the request is held whenever the
// outcome is not certain: while the binding is pending and while the
// Instance publishes no connections (e.g. during a restart).
func (r *bindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	b := &monitoringv1alpha1.MonitoringBinding{}
	if err := r.client.Get(ctx, req.NamespacedName, b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !b.DeletionTimestamp.IsZero() || !r.executes(b) {
		return ctrl.Result{}, nil
	}
	held := b.Status.PodMetadata

	if !monitoring.IsAccepted(b) {
		accepted := meta.FindStatusCondition(b.Status.Conditions, monitoringv1alpha1.MonitoringBindingConditionAccepted)
		if accepted != nil && accepted.ObservedGeneration == b.Generation && accepted.Status == metav1.ConditionFalse {
			return ctrl.Result{}, r.cc.SetConfiguredWithPodMetadata(ctx, b, monitoringv1alpha1.ReasonNotAccepted,
				"binding not accepted: "+accepted.Reason, nil)
		}
		return ctrl.Result{}, r.cc.SetConfiguredWithPodMetadata(ctx, b, monitoringv1alpha1.ReasonPending,
			"waiting for the binding to be accepted", held)
	}

	in, _, err := r.cc.Resolve(ctx, b)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, r.cc.SetConfiguredWithPodMetadata(ctx, b, monitoringv1alpha1.ReasonRenderFailed, err.Error(), held)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	sources := sourcesOf(in)
	if sources == nil || len(sources.Connections) == 0 {
		return ctrl.Result{}, r.cc.SetConfiguredWithPodMetadata(ctx, b, monitoringv1alpha1.ReasonEndpointNotServing,
			fmt.Sprintf("Instance %s publishes no connection endpoints yet", in.Name), held)
	}

	podMetadata, notes := podMetadataFor(sources)
	if len(podMetadata) == 0 {
		return ctrl.Result{}, r.cc.SetConfiguredWithPodMetadata(ctx, b, monitoringv1alpha1.ReasonRenderFailed,
			strings.Join(notes, "; "), nil)
	}
	return ctrl.Result{}, r.cc.SetConfiguredWithPodMetadata(ctx, b, monitoringv1alpha1.ReasonConfigured,
		configuredMessage(podMetadata, notes), podMetadata)
}

func (r *bindingReconciler) executes(b *monitoringv1alpha1.MonitoringBinding) bool {
	return b.Spec.ExecutionMode == corev1alpha1.MonitoringExecutionModeExtensionManaged &&
		b.Spec.ClassRef != nil && b.Spec.ClassRef.Name == r.className
}

func configuredMessage(podMetadata []monitoringv1alpha1.ComponentPodMetadata, notes []string) string {
	components := make([]string, 0, len(podMetadata))
	for _, pm := range podMetadata {
		components = append(components, pm.Component)
	}
	msg := "coroot.com annotations requested for " + strings.Join(components, ", ") +
		"; the provider renders them on the pods"
	if len(notes) > 0 {
		msg += "; skipped " + strings.Join(notes, "; ")
	}
	return msg
}
