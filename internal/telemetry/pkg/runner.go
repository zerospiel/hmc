package telemetry

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/K0rdent/kcm/internal/telemetry/pkg/tracker"
)

// Runner is a controller-runtime Runnable that periodically invokes the tracker.
type Runner struct {
	Tracker   tracker.Tracker
	Collector tracker.Collector
	Logger    logr.Logger
	Clusters  []string
	Frequency time.Duration
}

// Verify interface compliance
var _ manager.Runnable = (*Runner)(nil)

func (r *Runner) Start(ctx context.Context) error {
	r.Logger.Info("Starting telemetry runner", "interval", r.Frequency)
	ticker := time.NewTicker(r.Frequency)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.Logger.Info("Shutting down telemetry runner")
			finishCtx, cancel := context.WithTimeout(context.Background(), time.Second*15)
			_ = r.Tracker.Flush(finishCtx)    //nolint:contextcheck // false-positive
			err := r.Tracker.Close(finishCtx) //nolint:contextcheck // false-positive
			cancel()
			return err
		case <-ticker.C:
			r.Logger.Info("Running telemetry collection")
			for _, cluster := range r.Clusters {
				if err := r.Tracker.Collect(ctx, cluster, r.Collector); err != nil {
					r.Logger.Error(err, "tracker.Collect failed", "cluster", cluster)
					continue
				}
			}
			if err := r.Tracker.Flush(ctx); err != nil {
				r.Logger.Error(err, "tracker.Flush failed")
				continue
			}
			r.Logger.Info("Telemetry collection complete")
		}
	}
}
