/*

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

	"github.com/go-logr/logr"
	ociv1alpha1 "github.com/logmein/k8s-oci-operator/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReservedIPReconciler reconciles a ReservedIP object
type ReservedIPAssociationReconciler struct {
	client.Client
	Log logr.Logger
}

// +kubebuilder:rbac:groups=oci.k8s.logmein.com,resources=reservedipassociations,verbs=get;list;watch;create;update;patch;delete

func (r *ReservedIPAssociationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("reservedIPAssociation", req.NamespacedName)

	var reservedIPAssociation ociv1alpha1.ReservedIPAssociation
	if err := r.Get(ctx, req.NamespacedName, &reservedIPAssociation); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if reservedIPAssociation.ObjectMeta.DeletionTimestamp.IsZero() {
		if !containsString(reservedIPAssociation.ObjectMeta.Finalizers, finalizerName) {
			reservedIPAssociation.ObjectMeta.Finalizers = append(reservedIPAssociation.ObjectMeta.Finalizers, finalizerName)
			log.Info("New ReservedIP Association")
			var reservedIP ociv1alpha1.ReservedIP
			if err := r.Client.Get(ctx, client.ObjectKey{
				Namespace: req.Namespace,
				Name:      reservedIPAssociation.Spec.ReservedIPName,
			}, &reservedIP); err != nil {
				return ctrl.Result{}, err
			}

			if reservedIP.Spec.Assignment == nil && reservedIP.Status.State == "allocated" {
				reservedIP.Spec.Assignment = reservedIPAssociation.Spec.Assignment
			} else {
				return ctrl.Result{}, fmt.Errorf("Cannot assign ReservedIP because it isn't in allocated state.")
			}

			if err := r.Update(ctx, &reservedIP); err != nil {
				return ctrl.Result{}, err
			}

			return ctrl.Result{}, r.Update(ctx, &reservedIPAssociation)
		}
	} else {
		// Association is being deleted we want to unassign ReservedIP
		if containsString(reservedIPAssociation.ObjectMeta.Finalizers, finalizerName) {
			var reservedIP ociv1alpha1.ReservedIP
			if err := r.Client.Get(ctx, client.ObjectKey{
				Namespace: req.Namespace,
				Name:      reservedIPAssociation.Spec.ReservedIPName,
			}, &reservedIP); err != nil {
				return ctrl.Result{}, err
			}

			if reservedIP.Status.Assignment != nil && reservedIP.Status.Assignment.PodName == reservedIPAssociation.Spec.Assignment.PodName {
				log.Info("Unassigning corresponding ReservedIP")
				reservedIP.Spec.Assignment = nil
				if err := r.Update(ctx, &reservedIP); err != nil {
					return ctrl.Result{}, err
				}
			}
			reservedIPAssociation.ObjectMeta.Finalizers = removeString(reservedIPAssociation.ObjectMeta.Finalizers, finalizerName)
			return ctrl.Result{}, r.Update(ctx, &reservedIPAssociation)
		}
	}

	return ctrl.Result{}, nil
}

func (r *ReservedIPAssociationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ociv1alpha1.ReservedIPAssociation{}).
		Complete(r)
}
