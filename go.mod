module github.com/joschi/pgq

go 1.24.0

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgtype v1.14.4
	github.com/jackc/pgx/v5 v5.8.0
	github.com/pkg/errors v0.9.1
	go.opentelemetry.io/otel v1.40.0
	go.opentelemetry.io/otel/metric v1.40.0
	go.opentelemetry.io/otel/trace v1.40.0
	golang.org/x/sync v0.19.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/jackc/pgio v1.0.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/lib/pq v1.10.9 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
)

// dependencies from github.com/jackc/pgx/v4 v4.18.2, that's used only in tests.
require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/crypto v0.45.0 // indirect
	golang.org/x/text v0.31.0 // indirect
)
