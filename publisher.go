package pgq

import (
	"context"
	stderrors "errors"
	"maps"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgtype"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pkg/errors"

	"github.com/joschi/pgq/internal/pg"
)

type publisher struct {
	db  *pgxpool.Pool
	cfg publisherConfig
}

// Publisher publishes messages to Postgres queue.
type Publisher interface {
	Publish(ctx context.Context, queue string, msg ...*MessageOutgoing) ([]uuid.UUID, error)
}

type publisherConfig struct {
	metaInjectors []func(context.Context, Metadata)
}

// PublisherOption configures the publisher. Multiple options can be passed to
// NewPublisher. Options are applied in the order they are given. The last option
// overrides any previous ones. If no options are passed to NewPublisher, the
// default values are used.
type PublisherOption func(*publisherConfig)

// WithMetaInjectors adds Metadata injectors to the publisher. Injectors are run in the order they are given.
func WithMetaInjectors(injectors ...func(context.Context, Metadata)) PublisherOption {
	return func(c *publisherConfig) {
		c.metaInjectors = append(c.metaInjectors, injectors...)
	}
}

// StaticMetaInjector returns a Metadata injector that injects given Metadata.
func StaticMetaInjector(m Metadata) func(context.Context, Metadata) {
	staticMetadata := maps.Clone(m)
	return func(_ context.Context, metadata Metadata) {
		maps.Copy(metadata, staticMetadata)
	}
}

// NewPublisher initializes the publisher with given *pgxpool.Pool client.
func NewPublisher(db *pgxpool.Pool, opts ...PublisherOption) Publisher {
	cfg := publisherConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &publisher{db: db, cfg: cfg}
}

// Publish publishes the message.
func (d *publisher) Publish(ctx context.Context, queue string, msgs ...*MessageOutgoing) (ids []uuid.UUID, err error) {
	if len(msgs) < 1 {
		return []uuid.UUID{}, nil
	}
	query := buildInsertQuery(queue, len(msgs))
	args := d.buildArgs(ctx, msgs)
	// transaction is used to have secured read of query result.
	tx, err := d.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "couldn't start transaction")
	}
	defer func() {
		r := recover()
		rErr := tx.Rollback(ctx)
		if rErr != nil && !errors.Is(rErr, pgx.ErrTxClosed) {
			if err != nil {
				// this is tricky, but we want to return both errors
				err = stderrors.Join(err, rErr)
			} else {
				err = rErr
			}
		}
		if r != nil {
			panic(r)
		}
	}()

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer rows.Close()
	ids = make([]uuid.UUID, 0, len(msgs))
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, errors.WithStack(err)
		}
		ids = append(ids, id.Bytes)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WithStack(err)
	}

	_, notifyErr := tx.Exec(ctx, "SELECT pg_notify($1, $2)", channelName(queue), "")
	if notifyErr != nil {
		return nil, errors.WithStack(notifyErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errors.WithStack(err)
	}
	return ids, nil
}

func buildInsertQuery(queue string, msgCount int) string {
	var sb strings.Builder
	sb.WriteString("INSERT INTO ")
	sb.WriteString(pg.QuoteIdentifier(queue))
	sb.WriteString(" (")
	sb.WriteString(dbFieldsString)
	sb.WriteString(") VALUES ")
	var params pg.StmtParams
	for rowIdx := 0; rowIdx < msgCount; rowIdx++ {
		if rowIdx != 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(")
		sb.WriteString(params.Next())
		sb.WriteString(",")
		sb.WriteString(params.Next())
		sb.WriteString(",")
		sb.WriteString(params.Next())
		sb.WriteString(")")
	}
	sb.WriteString(` RETURNING "id"`)
	return sb.String()
}

func (d *publisher) buildArgs(ctx context.Context, msgs []*MessageOutgoing) []any {
	args := make([]any, 0, len(msgs)*fieldCountPerMessageOutgoing)
	for _, msg := range msgs {
		for _, injector := range d.cfg.metaInjectors {
			injector(ctx, msg.Metadata)
		}
		metadata := msg.Metadata
		if metadata == nil {
			metadata = make(Metadata)
		}
		args = append(args, msg.ScheduledFor, msg.Payload, metadata)
	}
	return args
}
