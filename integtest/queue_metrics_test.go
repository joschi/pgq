package integtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	. "github.com/joschi/pgq"
	"github.com/joschi/pgq/internal/pg"
	"github.com/joschi/pgq/internal/require"
	"github.com/joschi/pgq/x/schema"
)

func TestQueueMetrics_message_states(t *testing.T) {
	db := openDB(t)
	queue := createMetricsQueue(t, db)
	reader := sdkmetric.NewManualReader()
	_, err := RegisterQueueMetrics(db, queue, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	require.NoError(t, err)

	// Empty queue.
	require.Equal(t, queueState{}, collectQueueState(t, reader, queue))

	_, err = db.Exec(context.Background(), fmt.Sprintf(`INSERT INTO %s
		(created_at, scheduled_for, processed_at, error_detail, consumed_count, locked_until, payload, metadata) VALUES
		(now() - interval '10 seconds', NULL, NULL, NULL, 0, NULL, '{}', '{}'),                             -- new
		(now() - interval '30 seconds', now() - interval '1 second', NULL, NULL, 0, NULL, '{}', '{}'),      -- scheduled, due
		(now() - interval '60 seconds', NULL, NULL, NULL, 1, now() + interval '1 hour', '{}', '{}'),        -- being processed / retry delay
		(now() - interval '20 seconds', NULL, NULL, 'nack', 1, NULL, '{}', '{}'),                           -- NACKed
		(now() - interval '3 hours', now() + interval '1 hour', NULL, NULL, 0, NULL, '{}', '{}'),           -- scheduled, future
		(now() - interval '1 hour', NULL, now(), NULL, 1, NULL, '{}', '{}'),                                -- ACKed
		(now() - interval '2 hours', NULL, now(), 'bad', 1, NULL, '{}', '{}'),                              -- discarded
		(now() - interval '4 hours', NULL, NULL, 'nack', 3, NULL, '{}', '{}')                               -- exhausted
	`, pg.QuoteIdentifier(queue)))
	require.NoError(t, err)

	got := collectQueueState(t, reader, queue)
	require.Equal(t, int64(4), got.pending)
	requireSeconds(t, 60, got.oldestAge)
	require.Equal(t, int64(1), got.exhausted)

	// The exhausted classification follows the configured max consume count.
	for _, tc := range []struct {
		max                uint
		pending, exhausted int64
		oldestAge          float64
	}{
		{max: 0, pending: 5, exhausted: 0, oldestAge: 4 * 3600},
		// The row being processed is on its last attempt: still pending, not exhausted.
		{max: 1, pending: 3, exhausted: 2, oldestAge: 60},
		{max: 5, pending: 5, exhausted: 0, oldestAge: 4 * 3600},
	} {
		r := sdkmetric.NewManualReader()
		_, err := RegisterQueueMetrics(db, queue, sdkmetric.NewMeterProvider(sdkmetric.WithReader(r)),
			WithQueueMetricsMaxConsumeCount(tc.max))
		require.NoError(t, err)
		got := collectQueueState(t, r, queue)
		require.Equal(t, tc.pending, got.pending)
		require.Equal(t, tc.exhausted, got.exhausted)
		requireSeconds(t, tc.oldestAge, got.oldestAge)
	}
}

func TestQueueMetrics_consumers_and_queues(t *testing.T) {
	db := openDB(t)
	queue := createMetricsQueue(t, db)
	otherQueue := createMetricsQueue(t, db, "other")
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	for _, q := range []string{queue, otherQueue} {
		_, err := RegisterQueueMetrics(db, q, mp, WithQueueMetricsMaxConsumeCount(1))
		require.NoError(t, err)
	}

	_, err := NewPublisher(db).Publish(context.Background(), queue,
		&MessageOutgoing{Payload: json.RawMessage(`{"n":1}`)},
		&MessageOutgoing{Payload: json.RawMessage(`{"n":2}`)},
	)
	require.NoError(t, err)

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	handler := MessageHandlerFunc(func(context.Context, *MessageIncoming) (bool, error) {
		started <- struct{}{}
		<-release
		return MessageProcessed, nil
	})
	var wg sync.WaitGroup
	for range 2 {
		c, err := NewConsumer(db, queue, handler, WithMaxConsumeCount(1), WithStopOnEmptyQueue(true), WithPollingInterval(10*time.Millisecond))
		require.NoError(t, err)
		wg.Go(func() {
			if err := c.Run(context.Background()); err != io.EOF {
				t.Errorf("consumer: %v", err)
			}
		})
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("handlers did not start")
		}
	}

	// Messages being processed on their last attempt are pending, not exhausted.
	require.Equal(t, int64(2), collectQueueState(t, reader, queue).pending)
	require.Equal(t, int64(0), collectQueueState(t, reader, queue).exhausted)
	require.Equal(t, queueState{}, collectQueueState(t, reader, otherQueue))

	releaseAll()
	wg.Wait()
	require.Equal(t, queueState{}, collectQueueState(t, reader, queue))
}

func TestQueueMetrics_query_error_and_unregister(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	db := openDB(t)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	missing, err := RegisterQueueMetrics(db, "pgq_missing_queue_table", mp)
	require.NoError(t, err)
	var rm metricdata.ResourceMetrics
	err = reader.Collect(context.Background(), &rm)
	if err == nil || !strings.Contains(err.Error(), "pgq_missing_queue_table") {
		t.Fatalf("expected query error, got %v", err)
	}
	require.Equal(t, 0, countDataPoints(rm))

	queue := createMetricsQueue(t, db)
	reg, err := RegisterQueueMetrics(db, queue, mp)
	require.NoError(t, err)
	require.NoError(t, missing.Unregister())
	require.Equal(t, queueState{}, collectQueueState(t, reader, queue))

	require.NoError(t, reg.Unregister())
	rm = metricdata.ResourceMetrics{}
	require.NoError(t, reader.Collect(context.Background(), &rm))
	require.Equal(t, 0, countDataPoints(rm))
}

func TestQueueMetrics_large_history(t *testing.T) {
	db := openDB(t)
	queue := createMetricsQueue(t, db)
	ctx := context.Background()
	_, err := db.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %[1]s (created_at, processed_at, consumed_count, payload, metadata)
		SELECT now() - make_interval(secs => i), now() - make_interval(secs => i), 1, '{}', '{}'
		FROM generate_series(1, 200000) AS i;
		INSERT INTO %[1]s (created_at, payload, metadata) VALUES (now() - interval '5 seconds', '{}', '{}');
		ANALYZE %[1]s;`, pg.QuoteIdentifier(queue)))
	require.NoError(t, err)

	reader := sdkmetric.NewManualReader()
	_, err = RegisterQueueMetrics(db, queue, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	require.NoError(t, err)

	start := time.Now()
	got := collectQueueState(t, reader, queue)
	t.Logf("collection over 200k processed rows took %v", time.Since(start))
	require.Equal(t, int64(1), got.pending)
	requireSeconds(t, 5, got.oldestAge)
	require.Equal(t, int64(0), got.exhausted)
}

type queueState struct {
	pending, exhausted int64
	oldestAge          float64
}

func collectQueueState(t *testing.T, reader *sdkmetric.ManualReader, queue string) queueState {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var s queueState
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				for _, dp := range data.DataPoints {
					if v, _ := dp.Attributes.Value(attribute.Key("queue_name")); v.AsString() != queue {
						continue
					}
					switch m.Name {
					case "pgq.queue.pending_messages":
						s.pending = dp.Value
					case "pgq.queue.exhausted_messages":
						s.exhausted = dp.Value
					}
				}
			case metricdata.Gauge[float64]:
				for _, dp := range data.DataPoints {
					if v, _ := dp.Attributes.Value(attribute.Key("queue_name")); v.AsString() == queue && m.Name == "pgq.queue.oldest_pending_age" {
						s.oldestAge = dp.Value
					}
				}
			}
		}
	}
	return s
}

func countDataPoints(rm metricdata.ResourceMetrics) int {
	n := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				n += len(data.DataPoints)
			case metricdata.Gauge[float64]:
				n += len(data.DataPoints)
			}
		}
	}
	return n
}

func requireSeconds(t *testing.T, want, got float64) {
	t.Helper()
	if math.Abs(want-got) > 5 {
		t.Fatalf("expected about %vs, got %vs", want, got)
	}
}

func createMetricsQueue(t *testing.T, db *pgxpool.Pool, suffix ...string) string {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	queue := strings.Join(append([]string{t.Name()}, suffix...), "_")
	_, _ = db.Exec(ctx, schema.GenerateDropTableQuery(queue))
	_, err := db.Exec(ctx, schema.GenerateCreateTableQuery(queue))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec(ctx, schema.GenerateDropTableQuery(queue))
		require.NoError(t, err)
	})
	return queue
}
