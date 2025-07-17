package telemetry

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/go-logr/logr"
	"github.com/segmentio/analytics-go/v3"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/K0rdent/kcm/internal/build"
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

var _ manager.Runnable = (*Runner)(nil)

type Mode string

const (
	ModeDisabled Mode = "disabled"
	ModeLocal    Mode = "local"
	ModeOnline   Mode = "online"
)

type Config struct {
	Interval        time.Duration
	Mode            Mode
	MgmtClient      client.Client
	SystemNamespace string
}

var segmentToken string // TODO: preserve just for now, remove when the whole package replaces the old one

func NewRunner(cfg Config) (*Runner, error) {
	if cfg.Mode == ModeOnline {
		tz := ""
		if loc := time.Now().Location(); loc != nil {
			tz = loc.String()
		}

		analyticsClient, err := analytics.NewWithConfig(segmentToken, analytics.Config{
			BatchSize: 500,
			Interval:  time.Minute,
			DefaultContext: &analytics.Context{
				App: analytics.AppInfo{
					Build:     build.Commit,
					Name:      build.Name,
					Version:   build.Version,
					Namespace: cfg.SystemNamespace,
				},
				OS: analytics.OSInfo{
					Name:    runtime.GOOS,
					Version: runtime.GOARCH,
				},
				Timezone: tz,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to init segmentio client: %w", err)
		}
		_ = analyticsClient
	}

	return &Runner{}, nil
}

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
