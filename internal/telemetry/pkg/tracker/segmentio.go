package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
)

type OnlineTracker struct {
	client     *http.Client
	writeKey   string
	endpoint   string
	buffer     []SegmentEvent
	retryLimit int
	timeout    time.Duration
}

type SegmentEvent struct {
	AnonymousID string         `json:"anonymousId"`
	Event       string         `json:"event"`
	Properties  map[string]any `json:"properties"`
	Timestamp   string         `json:"timestamp"`
}

func NewOnlineTracker(writeKey, endpoint string, retryLimit int, timeout time.Duration) *OnlineTracker {
	return &OnlineTracker{
		writeKey:   writeKey,
		endpoint:   endpoint,
		client:     &http.Client{Timeout: timeout},
		retryLimit: retryLimit,
		timeout:    timeout,
		buffer:     make([]SegmentEvent, 0),
	}
}

func (o *OnlineTracker) Collect(ctx context.Context, cluster string, collector Collector) error {
	data, err := collector.CollectFromCluster(ctx, cluster)
	if err != nil {
		return fmt.Errorf("failed to collect metrics from cluster %q: %w", cluster, err)
	}

	event := SegmentEvent{
		AnonymousID: uuid.New().String(),
		Event:       "ClusterTelemetry",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Properties: map[string]any{
			"cluster": cluster,
			"metrics": data,
		},
	}
	o.buffer = append(o.buffer, event)
	return nil
}

func (o *OnlineTracker) Flush(ctx context.Context) error {
	if len(o.buffer) == 0 {
		return nil
	}
	events := o.buffer
	o.buffer = nil

	// TODO: no need
	payload := map[string]any{
		"batch": events,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	retryPolicy := backoff.WithMaxRetries(backoff.NewExponentialBackOff(), uint64(o.retryLimit))

	send := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(jsonBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(o.writeKey, "")
		resp, err := o.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close() //nolint:errcheck // no need

		if resp.StatusCode >= 300 {
			return fmt.Errorf("segment returned status %d", resp.StatusCode)
		}
		return nil
	}

	if err := backoff.Retry(send, retryPolicy); err != nil {
		return fmt.Errorf("failed to send telemetry after retries: %w", err)
	}
	return nil
}

func (o *OnlineTracker) Close(ctx context.Context) error {
	return o.Flush(ctx)
}
