/*
Copyright 2020 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/reference"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/metrics"
	"github.com/fluxcd/pkg/runtime/predicates"

	"github.com/fluxcd/notification-controller/api/v1beta1"
)

// AlertReconciler reconciles a Alert object
type AlertReconciler struct {
	client.Client
	Log             logr.Logger
	Scheme          *runtime.Scheme
	MetricsRecorder *metrics.Recorder
}

// +kubebuilder:rbac:groups=notification.toolkit.fluxcd.io,resources=alerts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=notification.toolkit.fluxcd.io,resources=alerts/status,verbs=get;update;patch

func (r *AlertReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	reconcileStart := time.Now()

	var alert v1beta1.Alert
	if err := r.Get(ctx, req.NamespacedName, &alert); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log := r.Log.WithValues("controller", strings.ToLower(alert.Kind), "request", req.NamespacedName)

	// record reconciliation duration
	if r.MetricsRecorder != nil {
		objRef, err := reference.GetReference(r.Scheme, &alert)
		if err != nil {
			return ctrl.Result{}, err
		}
		defer r.MetricsRecorder.RecordDuration(*objRef, reconcileStart)
	}

	// validate alert spec and provider
	if err := r.validate(ctx, alert); err != nil {
		meta.SetResourceCondition(&alert, meta.ReadyCondition, metav1.ConditionFalse, meta.ReconciliationFailedReason, err.Error())
		if err := r.Status().Update(ctx, &alert); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
		return ctrl.Result{Requeue: true}, err
	}

	if !apimeta.IsStatusConditionTrue(alert.Status.Conditions, meta.ReadyCondition) {
		meta.SetResourceCondition(&alert, meta.ReadyCondition, metav1.ConditionTrue, v1beta1.InitializedReason, v1beta1.InitializedReason)
		if err := r.Status().Update(ctx, &alert); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
		log.Info("Alert initialised")
	}

	r.recordReadiness(alert, false)

	return ctrl.Result{}, nil
}

func (r *AlertReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Alert{}).
		WithEventFilter(predicates.ChangePredicate{}).
		Complete(r)
}

func (r *AlertReconciler) validate(ctx context.Context, alert v1beta1.Alert) error {
	var provider v1beta1.Provider
	providerName := types.NamespacedName{Namespace: alert.Namespace, Name: alert.Spec.ProviderRef.Name}
	if err := r.Get(ctx, providerName, &provider); err != nil {
		return fmt.Errorf("failed to get provider %s, error: %w", providerName.String(), err)
	}

	if !apimeta.IsStatusConditionTrue(provider.Status.Conditions, meta.ReadyCondition) {
		return fmt.Errorf("provider %s is not ready", providerName.String())
	}

	return nil
}

func (r *AlertReconciler) recordReadiness(alert v1beta1.Alert, deleted bool) {
	if r.MetricsRecorder == nil {
		return
	}

	objRef, err := reference.GetReference(r.Scheme, &alert)
	if err != nil {
		r.Log.WithValues(
			strings.ToLower(alert.Kind),
			fmt.Sprintf("%s/%s", alert.GetNamespace(), alert.GetName()),
		).Error(err, "unable to record readiness metric")
		return
	}
	if rc := apimeta.FindStatusCondition(alert.Status.Conditions, meta.ReadyCondition); rc != nil {
		r.MetricsRecorder.RecordCondition(*objRef, *rc, deleted)
	} else {
		r.MetricsRecorder.RecordCondition(*objRef, metav1.Condition{
			Type:   meta.ReadyCondition,
			Status: metav1.ConditionUnknown,
		}, deleted)
	}
}
