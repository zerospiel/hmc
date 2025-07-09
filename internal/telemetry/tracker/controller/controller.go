package collector

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/K0rdent/kcm/internal/telemetry/tracker/config"
	"github.com/K0rdent/kcm/internal/telemetry/tracker/storage"
)

type TelemetryRunner struct {
	cfg     *config.TelemetryConfig
	store   storage.Store
	mgr     manager.Manager
	cancel  context.CancelFunc
	running bool
}

var _ manager.Runnable = (*TelemetryRunner)(nil)

func NewTelemetryRunner(cfg *config.TelemetryConfig, mgr manager.Manager) (*TelemetryRunner, error) {
	store := storage.NewFileStore(cfg.StorageDir)
	return &TelemetryRunner{
		cfg:   cfg,
		store: store,
		mgr:   mgr,
	}, nil
}

func (t *TelemetryRunner) Start(ctx context.Context) error {
	if t.running {
		return fmt.Errorf("already running")
	}
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.running = true

	// open this week’s file
	if err := t.store.OpenWeekly(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(t.cfg.Frequency)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				t.store.Flush(context.Background())
				return
			case <-ticker.C:
				t.collectOnce(ctx)
			}
		}
	}()
	return nil
}

func (t *TelemetryRunner) collectOnce(ctx context.Context) {
	// 1) if new week, rotate
	t.store.OpenWeekly(ctx)

	// 2) list Cluster CRs (pseudo-code)
	// var list ClusterList
	// t.mgr.GetClient().List(ctx, &list)
	// for _, cr := range list.Items {
	//     cfg, err := clientcmd.BuildConfigFromFlags("", cr.Kubeconfig)
	//     // create client, fetch metrics...
	//     key := fmt.Sprintf("%s.node.cpu", cr.Name)
	//     t.store.Inc(ctx, key, 1)
	// }

	// stub: increment a dummy metric
	t.store.Inc(ctx, "dummy.metric", 1)
}

func (t *TelemetryRunner) Stop(ctx context.Context) error {
	if t.cancel != nil {
		t.cancel()
	}
	t.running = false
	return nil
}
