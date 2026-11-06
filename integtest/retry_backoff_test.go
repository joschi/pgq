package integtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	. "github.com/joschi/pgq"
	"github.com/joschi/pgq/internal/pg"
	"github.com/joschi/pgq/internal/require"
	"github.com/joschi/pgq/x/schema"
)

var hourBackoff = ExponentialBackoff{Initial: time.Hour, Multiplier: 2, Max: 3 * time.Hour}

func TestRetryBackoff_disabled_redelivers_immediately(t *testing.T) {
	db, queue := setupQueue(t)
	id := publishOne(t, db, queue, nil)

	// Each run stops once nothing is claimable; with no backoff the message
	// is claimable again right after its NACK.
	for range 4 {
		runNacking(t, db, queue, WithMaxConsumeCount(3))
	}

	s := readState(t, db, queue, id)
	require.Equal(t, 3, s.consumedCount)
	require.Equal(t, false, s.lockedUntil.Valid)
}

func TestRetryBackoff_consecutive_failures_and_restarts(t *testing.T) {
	db, queue := setupQueue(t)
	due := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	id := publishOne(t, db, queue, &due)
	future := time.Now().Add(time.Hour)
	futureID := publishOne(t, db, queue, &future)

	for attempt, want := range []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 3 * time.Hour} {
		runNacking(t, db, queue, WithRetryBackoff(hourBackoff), WithMaxConsumeCount(10))
		s := readState(t, db, queue, id)
		require.Equal(t, attempt+1, s.consumedCount)
		requireAbout(t, want, s.retryIn)
		require.Equal(t, true, s.scheduledFor.Time.Equal(due))
		require.Equal(t, "nacked", s.errorDetail.String)

		// A restarted consumer must not claim the message during backoff.
		runNacking(t, db, queue, WithRetryBackoff(hourBackoff), WithMaxConsumeCount(10))
		require.Equal(t, attempt+1, readState(t, db, queue, id).consumedCount)

		expireLock(t, db, queue, id)
	}
	require.Equal(t, 0, readState(t, db, queue, futureID).consumedCount)
}

func TestRetryBackoff_max_consume_count_exhausted(t *testing.T) {
	db, queue := setupQueue(t)
	id := publishOne(t, db, queue, nil)
	opts := []ConsumerOption{WithRetryBackoff(hourBackoff), WithMaxConsumeCount(2)}

	runNacking(t, db, queue, opts...)
	requireAbout(t, time.Hour, readState(t, db, queue, id).retryIn)
	expireLock(t, db, queue, id)

	// The last attempt has no delay: there is nothing left to wait for.
	runNacking(t, db, queue, opts...)
	s := readState(t, db, queue, id)
	require.Equal(t, 2, s.consumedCount)
	require.Equal(t, false, s.lockedUntil.Valid)

	runNacking(t, db, queue, opts...)
	s = readState(t, db, queue, id)
	require.Equal(t, 2, s.consumedCount)
	require.Equal(t, false, s.processedAt.Valid)
}

func TestRetryBackoff_crashed_consumer_lock_expiry(t *testing.T) {
	db, queue := setupQueue(t)
	id := publishOne(t, db, queue, nil)

	// State left behind by a consumer that crashed while holding the claim lock.
	_, err := db.Exec(context.Background(), fmt.Sprintf(
		`UPDATE %s SET consumed_count = 1, started_at = now(), locked_until = now() + interval '1 hour' WHERE id = $1`,
		pg.QuoteIdentifier(queue)), id)
	require.NoError(t, err)

	runNacking(t, db, queue, WithRetryBackoff(hourBackoff))
	require.Equal(t, 1, readState(t, db, queue, id).consumedCount)

	// Once the claim lock expires the message is redelivered and the crashed
	// attempt counts towards the backoff exponent.
	expireLock(t, db, queue, id)
	runNacking(t, db, queue, WithRetryBackoff(hourBackoff))
	s := readState(t, db, queue, id)
	require.Equal(t, 2, s.consumedCount)
	requireAbout(t, 2*time.Hour, s.retryIn)
}

func TestRetryBackoff_concurrent_consumers(t *testing.T) {
	db, queue := setupQueue(t)
	const n = 20
	msgs := make([]*MessageOutgoing, n)
	for i := range msgs {
		msgs[i] = &MessageOutgoing{Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))}
	}
	ids, err := NewPublisher(db).Publish(context.Background(), queue, msgs...)
	require.NoError(t, err)

	var mu sync.Mutex
	deliveries := map[string]int{}
	handler := MessageHandlerFunc(func(_ context.Context, m *MessageIncoming) (bool, error) {
		mu.Lock()
		deliveries[string(m.Payload)]++
		mu.Unlock()
		return MessageNotProcessed, nil
	})

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			c, err := NewConsumer(db, queue, handler,
				WithRetryBackoff(hourBackoff),
				WithMaxParallelMessages(3),
				WithPollingInterval(10*time.Millisecond),
				WithStopOnEmptyQueue(true),
			)
			if err == nil {
				err = c.Run(context.Background())
			}
			if err != io.EOF {
				t.Errorf("consumer: %v", err)
			}
		})
	}
	wg.Wait()

	require.Equal(t, n, len(deliveries))
	for _, count := range deliveries {
		require.Equal(t, 1, count)
	}
	for _, id := range ids {
		s := readState(t, db, queue, id)
		require.Equal(t, 1, s.consumedCount)
		requireAbout(t, time.Hour, s.retryIn)
	}
}

func setupQueue(t *testing.T) (*pgxpool.Pool, string) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	db := openDB(t)
	queue := t.Name()
	_, _ = db.Exec(ctx, schema.GenerateDropTableQuery(queue))
	_, err := db.Exec(ctx, schema.GenerateCreateTableQuery(queue))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec(ctx, schema.GenerateDropTableQuery(queue))
		require.NoError(t, err)
	})
	return db, queue
}

func publishOne(t *testing.T, db *pgxpool.Pool, queue string, scheduledFor *time.Time) uuid.UUID {
	ids, err := NewPublisher(db).Publish(context.Background(), queue,
		&MessageOutgoing{Payload: json.RawMessage(`{"foo":"bar"}`), ScheduledFor: scheduledFor})
	require.NoError(t, err)
	return ids[0]
}

// runNacking runs a consumer that NACKs everything until no message is
// claimable.
func runNacking(t *testing.T, db *pgxpool.Pool, queue string, opts ...ConsumerOption) {
	handler := MessageHandlerFunc(func(context.Context, *MessageIncoming) (bool, error) {
		return MessageNotProcessed, fmt.Errorf("nacked")
	})
	opts = append([]ConsumerOption{WithStopOnEmptyQueue(true), WithPollingInterval(10 * time.Millisecond)}, opts...)
	c, err := NewConsumer(db, queue, handler, opts...)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Run(ctx); err != io.EOF && err != context.DeadlineExceeded {
		t.Fatalf("unexpected Run error: %v", err)
	}
}

type msgState struct {
	consumedCount int
	lockedUntil   pgtype.Timestamptz
	scheduledFor  pgtype.Timestamptz
	processedAt   pgtype.Timestamptz
	errorDetail   pgtype.Text
	retryIn       time.Duration
}

func readState(t *testing.T, db *pgxpool.Pool, queue string, id uuid.UUID) msgState {
	var s msgState
	var retryIn pgtype.Float8
	err := db.QueryRow(context.Background(), fmt.Sprintf(
		`SELECT consumed_count, locked_until, scheduled_for, processed_at, error_detail,
		        EXTRACT(EPOCH FROM locked_until - CURRENT_TIMESTAMP)::float8
		 FROM %s WHERE id = $1`, pg.QuoteIdentifier(queue)), id,
	).Scan(&s.consumedCount, &s.lockedUntil, &s.scheduledFor, &s.processedAt, &s.errorDetail, &retryIn)
	require.NoError(t, err)
	s.retryIn = time.Duration(retryIn.Float64 * float64(time.Second))
	return s
}

// expireLock fast-forwards the message past its backoff or claim lock.
func expireLock(t *testing.T, db *pgxpool.Pool, queue string, id uuid.UUID) {
	_, err := db.Exec(context.Background(), fmt.Sprintf(
		`UPDATE %s SET locked_until = now() - interval '1 second' WHERE id = $1`, pg.QuoteIdentifier(queue)), id)
	require.NoError(t, err)
}

func requireAbout(t *testing.T, want, got time.Duration) {
	t.Helper()
	if math.Abs(float64(want-got)) > float64(10*time.Second) {
		t.Fatalf("expected about %v, got %v", want, got)
	}
}
