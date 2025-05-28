package sveltos

import (
	"context"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/K0rdent/kcm/internal/utils/ratelimit"
)

// ClusterReconciler reconciles a SveltosCluster object.
type ClusterReconciler struct {
	client.Client

	SystemNamespace string
}

func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := ctrl.LoggerFrom(ctx)
	l.Info("Reconciling")

	sveltosCluster := new(libsveltosv1beta1.SveltosCluster)
	if err := r.Get(ctx, req.NamespacedName, sveltosCluster); err != nil {
		l.Error(err, "unable to fetch ManagementBackup")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO: get SA (api.Config or spec), get TokenRequest, regenerate kubeconfig, put in the cluter's secret

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("sveltos_cluster_controller").
		For(&libsveltosv1beta1.SveltosCluster{}).
		WithOptions(controller.TypedOptions[ctrl.Request]{
			RateLimiter: ratelimit.DefaultFastSlow(),
		}).
		WithEventFilter(predicate.Funcs{
			GenericFunc: func(event.TypedGenericEvent[client.Object]) bool { return false },
			CreateFunc:  func(event.TypedCreateEvent[client.Object]) bool { return false },
			DeleteFunc:  func(event.TypedDeleteEvent[client.Object]) bool { return false },
			UpdateFunc: func(_ event.TypedUpdateEvent[client.Object]) bool {
				// TODO: only if status changed(?), if status is down (check failureMessage?), if last updated token != nil && now.Sub(lasttoken) > threshold
				return true
			},
		}).
		Complete(r)
}
