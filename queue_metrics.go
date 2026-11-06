package pgq

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"

	"github.com/joschi/pgq/internal/pg"
)

// RegisterQueueMetrics registers OpenTelemetry gauges that describe the
// whole queue table, using the consumer's meter provider ([WithMeterProvider])
// and max consume count ([WithMaxConsumeCount]). Each gauge carries the
// queue_name attribute.
//
//   - pgq.queue.pending_messages counts due, unprocessed messages that
//     consumers can still pick up. It includes messages a consumer is
//     processing and messages waiting for a retry.
//   - pgq.queue.oldest_pending_age is now - created_at of the oldest of those
//     messages in seconds, or 0 when there are none.
//   - pgq.queue.exhausted_messages counts due, unprocessed messages whose
//     consumed_count reached the max consume count and that no consumer
//     holds a lock on. Consumers skip them. A message on its last attempt
//     stays pending until that attempt's lock is released or expires.
//
// A message is due when scheduled_for is NULL or in the past. ACKed and
// discarded messages have processed_at set and never count.
//
// The callback runs one aggregate query each time the MeterProvider collects,
// so the metric reader's interval sets the query rate. When the query fails,
// the callback reports no values and returns the error, which the
// OpenTelemetry SDK passes to its error handler.
//
// Neither NewConsumer nor Run registers the metrics. Call RegisterQueueMetrics
// on one consumer per queue, not on every replica, and call Unregister on the
// returned registration to stop collecting. The consumer does not need to be
// running. Metadata filters and history limits don't apply.
func (c *Consumer) RegisterQueueMetrics() (metric.Registration, error) {
	return registerQueueMetrics(c.db, c.queueName, c.cfg.MetricProvider, c.cfg.MaxConsumeCount)
}

func registerQueueMetrics(db *pgxpool.Pool, queueName string, mp metric.MeterProvider, maxConsumeCount uint) (metric.Registration, error) {
	meter := mp.Meter(otelScopeName)
	pending, err := meter.Int64ObservableGauge("pgq.queue.pending_messages",
		metric.WithUnit("{message}"),
		metric.WithDescription("Unprocessed, due messages that can still be consumed, including messages being processed or waiting for retry."))
	if err != nil {
		return nil, err
	}
	oldestAge, err := meter.Float64ObservableGauge("pgq.queue.oldest_pending_age",
		metric.WithUnit("s"),
		metric.WithDescription("Age (since created_at) of the oldest pending message. 0 if there is none."))
	if err != nil {
		return nil, err
	}
	exhausted, err := meter.Int64ObservableGauge("pgq.queue.exhausted_messages",
		metric.WithUnit("{message}"),
		metric.WithDescription("Unprocessed messages that reached the max consume count and are no longer consumed."))
	if err != nil {
		return nil, err
	}

	// processed_at IS NULL matches the partial processed_at_null index, so
	// PostgreSQL can skip processed rows. The plan is up to the planner.
	query := `SELECT
		count(*) FILTER (WHERE NOT exhausted),
		COALESCE(EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - min(created_at) FILTER (WHERE NOT exhausted)), 0)::float8,
		count(*) FILTER (WHERE exhausted)
	FROM (
		SELECT created_at,
			($1::int > 0 AND consumed_count >= $1::int
				AND (locked_until IS NULL OR locked_until < CURRENT_TIMESTAMP)) AS exhausted
		FROM ` + pg.QuoteIdentifier(queueName) + `
		WHERE processed_at IS NULL AND (scheduled_for IS NULL OR scheduled_for < CURRENT_TIMESTAMP)
	) q`
	attrs := metric.WithAttributes(attrQueueName.String(queueName))

	return meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		var p, e int64
		var age float64
		if err := db.QueryRow(ctx, query, maxConsumeCount).Scan(&p, &age, &e); err != nil {
			return fmt.Errorf("pgq: querying state of queue %s: %w", queueName, err)
		}
		o.ObserveInt64(pending, p, attrs)
		o.ObserveFloat64(oldestAge, age, attrs)
		o.ObserveInt64(exhausted, e, attrs)
		return nil
	}, pending, oldestAge, exhausted)
}
