package tracker

import "context"

// Collector defines an interface that provides metrics from a cluster.
type (
	Collector interface {
		CollectFromCluster(ctx context.Context, cluster string) (map[string]any, error)
	}

	Tracker interface {
		// Called periodically by the controller to trigger a collection round.
		Collect(context.Context, string, Collector) error
		// Called to persist/send any buffered data.
		Flush(context.Context) error
		// Called once during shutdown to release resources and flush final data.
		Close(context.Context) error
	}
)

var _ Tracker = nil
