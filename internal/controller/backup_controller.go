// Copyright 2024
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	hmcmirantiscomv1alpha1 "github.com/Mirantis/hmc/api/v1alpha1"
)

// BackupReconciler reconciles a Backup object
type BackupReconciler struct {
	client.Client
}

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	backup := new(hmcmirantiscomv1alpha1.Backup)
	err := r.Client.Get(ctx, req.NamespacedName, backup)
	if ierr := client.IgnoreNotFound(err); ierr != nil {
		l.Error(ierr, "unable to fetch Backup")
		return ctrl.Result{}, ierr
	}

	if apierrors.IsNotFound(err) {
		// fetch mgmt, check if names are equal, if so, create a backup with the schedule
		// mark as scheduled type of backup
		// separated logic for scheduled/oneshot backups create/update events
		// because i need to create schedule/backup according to the type
		time.Sleep(1 * time.Second)
		_ = ctx
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hmcmirantiscomv1alpha1.Backup{}).
		Watches(&hmcmirantiscomv1alpha1.Management{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []ctrl.Request {
			return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
		}), builder.WithPredicates(
			predicate.Funcs{
				GenericFunc: func(event.TypedGenericEvent[client.Object]) bool { return false },
				DeleteFunc:  func(event.TypedDeleteEvent[client.Object]) bool { return false },
				CreateFunc: func(tce event.TypedCreateEvent[client.Object]) bool {
					mgmt, ok := tce.Object.(*hmcmirantiscomv1alpha1.Management)
					if !ok {
						return false
					}

					return mgmt.Spec.Backup.Enabled
				},
				UpdateFunc: func(tue event.TypedUpdateEvent[client.Object]) bool {
					oldMgmt, ok := tue.ObjectOld.(*hmcmirantiscomv1alpha1.Management)
					if !ok {
						return false
					}

					newMgmt, ok := tue.ObjectNew.(*hmcmirantiscomv1alpha1.Management)
					if !ok {
						return false
					}

					return (newMgmt.Spec.Backup.Enabled != oldMgmt.Spec.Backup.Enabled ||
						newMgmt.Spec.Backup.Schedule != oldMgmt.Spec.Backup.Schedule)
				},
			},
		)).
		Complete(r)
}
