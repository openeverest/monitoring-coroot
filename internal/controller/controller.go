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

// Package controller is the Coroot MonitoringClass controller: it claims
// the class, reports its destinations Ready and requests the coroot.com/*
// pod annotations of every bound Instance on the binding.
package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/monitoring"
)

// FieldOwner is the server-side apply field manager of everything the
// controller writes.
const FieldOwner = "openeverest.io/monitoring-coroot"

// Setup registers the class, destination and binding controllers for the
// MonitoringClass named className.
func Setup(mgr ctrl.Manager, className string) error {
	cc := monitoring.ClassController{Client: mgr.GetClient(), FieldOwner: FieldOwner, ClassName: className}

	if err := ctrl.NewControllerManagedBy(mgr).
		Named("monitoring-coroot-class").
		For(&monitoringv1alpha1.MonitoringClass{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				class, ok := obj.(*monitoringv1alpha1.MonitoringClass)
				return ok && cc.OwnsClass(class)
			}),
		)).
		Complete(&classReconciler{client: mgr.GetClient(), cc: cc}); err != nil {
		return fmt.Errorf("set up MonitoringClass controller: %w", err)
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		Named("monitoring-coroot-destination").
		For(&monitoringv1alpha1.MonitoringDestination{}, builder.WithPredicates(
			predicate.GenerationChangedPredicate{},
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				dst, ok := obj.(*monitoringv1alpha1.MonitoringDestination)
				return ok && dst.Spec.ClassRef.Name == className
			}),
		)).
		Complete(&destinationReconciler{client: mgr.GetClient(), cc: cc}); err != nil {
		return fmt.Errorf("set up MonitoringDestination controller: %w", err)
	}

	r := &bindingReconciler{client: mgr.GetClient(), cc: cc, className: className}
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("monitoring-coroot").
		For(&monitoringv1alpha1.MonitoringBinding{}, builder.WithPredicates(monitoring.BindingPredicate(className))).
		Watches(&corev1alpha1.Instance{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForInstance),
			builder.WithPredicates(sourcesChanged())).
		Complete(r); err != nil {
		return fmt.Errorf("set up MonitoringBinding controller: %w", err)
	}
	return nil
}

type classReconciler struct {
	client client.Client
	cc     monitoring.ClassController
}

func (r *classReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	class := &monitoringv1alpha1.MonitoringClass{}
	if err := r.client.Get(ctx, req.NamespacedName, class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, r.cc.ClaimClass(ctx, class)
}

type destinationReconciler struct {
	client client.Client
	cc     monitoring.ClassController
}

// Reconcile reports the destination Ready: coroot-cluster-agent is
// configured with the Coroot server when it is installed, so a destination
// has nothing to probe; the class schema admits no parameters.
func (r *destinationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	dst := &monitoringv1alpha1.MonitoringDestination{}
	if err := r.client.Get(ctx, req.NamespacedName, dst); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if dst.Spec.ClassRef.Name != r.cc.ClassName {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.cc.SetDestinationReady(ctx, dst, monitoringv1alpha1.ReasonReady,
		"coroot-cluster-agent discovers the annotated database pods", "")
}

// sourcesChanged passes Instance updates that change the published
// connections or credentials.
func sourcesChanged() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldIn, okOld := e.ObjectOld.(*corev1alpha1.Instance)
			newIn, okNew := e.ObjectNew.(*corev1alpha1.Instance)
			return okOld && okNew && !equality.Semantic.DeepEqual(sourcesOf(oldIn), sourcesOf(newIn))
		},
	}
}

func sourcesOf(in *corev1alpha1.Instance) *corev1alpha1.MonitoringSources {
	if in.Status.Monitoring == nil {
		return nil
	}
	return in.Status.Monitoring.Sources
}

func (r *bindingReconciler) bindingsForInstance(ctx context.Context, obj client.Object) []reconcile.Request {
	in, ok := obj.(*corev1alpha1.Instance)
	if !ok {
		return nil
	}
	bindings, err := monitoring.ListBindingsForInstance(ctx, r.client, in)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing bindings for Instance failed", "instance", in.Name)
		return nil
	}
	var reqs []reconcile.Request
	for i := range bindings {
		if r.executes(&bindings[i]) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&bindings[i])})
		}
	}
	return reqs
}
