package integtest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/joschi/pgq"
	pgutils "github.com/joschi/pgq/internal/pg"
	"github.com/joschi/pgq/internal/require"
	"github.com/joschi/pgq/x/schema"
)

func TestPublisher(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	type want struct {
		metadata json.RawMessage
		payload  json.RawMessage
	}
	tests := []struct {
		name          string
		msg           *pgq.MessageOutgoing
		publisherOpts []pgq.PublisherOption
		want          want
		wantErr       bool
	}{
		{
			name: "Select extra columns",
			msg: &pgq.MessageOutgoing{
				Metadata: pgq.Metadata{
					"test": "test_value",
				},
				Payload: json.RawMessage(`{"foo":"bar"}`),
			},
			publisherOpts: []pgq.PublisherOption{
				pgq.WithMetaInjectors(
					pgq.StaticMetaInjector(pgq.Metadata{"host": "localhost"}),
				),
			},
			want: want{
				metadata: json.RawMessage(`{"host": "localhost", "test": "test_value"}`),
				payload:  json.RawMessage(`{"foo": "bar"}`),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openDB(t)
			t.Cleanup(func() {
				db.Close()
			})
			queueName := t.Name()
			_, _ = db.Exec(ctx, schema.GenerateDropTableQuery(queueName))
			_, err := db.Exec(ctx, schema.GenerateCreateTableQuery(queueName))
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := db.Exec(ctx, schema.GenerateDropTableQuery(queueName))
				require.NoError(t, err)
			})
			d := pgq.NewPublisher(db, tt.publisherOpts...)
			msgIDs, err := d.Publish(ctx, queueName, tt.msg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.Equal(t, 1, len(msgIDs))
			require.NoError(t, err)
			row := db.QueryRow(ctx,
				fmt.Sprintf(
					"SELECT id, metadata, payload FROM %s WHERE id = $1",
					pgutils.QuoteIdentifier(queueName),
				),
				msgIDs[0],
			)
			var (
				id       pgtype.UUID
				metadata json.RawMessage
				payload  json.RawMessage
			)
			err = row.Scan(&id, &metadata, &payload)
			require.NoError(t, err)
			require.Equal(t, [16]byte(msgIDs[0]), id.Bytes)
			require.Equal(t, string(tt.want.metadata), string(metadata))
			require.Equal(t, string(tt.want.payload), string(payload))
		})
	}
}

func TestPublisherTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	type want struct {
		metadata json.RawMessage
		payload  json.RawMessage
	}
	tests := []struct {
		name          string
		msg           *pgq.MessageOutgoing
		publisherOpts []pgq.PublisherOption
		want          want
		wantErr       bool
	}{
		{
			name: "Select extra columns",
			msg: &pgq.MessageOutgoing{
				Metadata: pgq.Metadata{
					"test": "test_value",
				},
				Payload: json.RawMessage(`{"foo":"bar"}`),
			},
			publisherOpts: []pgq.PublisherOption{
				pgq.WithMetaInjectors(
					pgq.StaticMetaInjector(pgq.Metadata{"host": "localhost"}),
				),
			},
			want: want{
				metadata: json.RawMessage(`{"host": "localhost", "test": "test_value"}`),
				payload:  json.RawMessage(`{"foo": "bar"}`),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openDB(t)
			t.Cleanup(func() {
				db.Close()
			})
			queueName := t.Name()
			_, _ = db.Exec(ctx, schema.GenerateDropTableQuery(queueName))
			_, err := db.Exec(ctx, schema.GenerateCreateTableQuery(queueName))
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := db.Exec(ctx, schema.GenerateDropTableQuery(queueName))
				require.NoError(t, err)
			})
			d := pgq.NewPublisher(db, tt.publisherOpts...)

			tx, err := db.Begin(ctx)
			require.NoError(t, err)
			defer func() {
				_ = tx.Rollback(ctx)
			}()

			msgIDs, err := d.PublishInTx(ctx, tx, queueName, tt.msg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.Equal(t, 1, len(msgIDs))
			require.NoError(t, err)

			var (
				id       pgtype.UUID
				metadata json.RawMessage
				payload  json.RawMessage
			)
			row := db.QueryRow(ctx,
				fmt.Sprintf(
					"SELECT id, metadata, payload FROM %s WHERE id = $1",
					pgutils.QuoteIdentifier(queueName),
				),
				msgIDs[0],
			)
			err = row.Scan(&id, &metadata, &payload)
			require.ErrorIs(t, err, pgx.ErrNoRows)

			row = tx.QueryRow(ctx,
				fmt.Sprintf(
					"SELECT id, metadata, payload FROM %s WHERE id = $1",
					pgutils.QuoteIdentifier(queueName),
				),
				msgIDs[0],
			)
			err = row.Scan(&id, &metadata, &payload)
			require.NoError(t, err)
			require.Equal(t, [16]byte(msgIDs[0]), id.Bytes)
			require.Equal(t, string(tt.want.metadata), string(metadata))
			require.Equal(t, string(tt.want.payload), string(payload))

			err = tx.Rollback(ctx)
			require.NoError(t, err)

			row = db.QueryRow(ctx,
				fmt.Sprintf(
					"SELECT id, metadata, payload FROM %s WHERE id = $1",
					pgutils.QuoteIdentifier(queueName),
				),
				msgIDs[0],
			)
			err = row.Scan(&id, &metadata, &payload)
			require.ErrorIs(t, err, pgx.ErrNoRows)
		})
	}
}
