package pgq

import (
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	// otelScopeName is the instrumentation scope name.
	otelScopeName = "github.com/joschi/pgq"
)

var (
	attrQueueName  = attribute.Key("queue_name")
	attrMessageID  = attribute.Key("message_id")
	attrResolution = attribute.Key("resolution")

	noopMeterProvider  = metricnoop.NewMeterProvider()
	noopTracerProvider = tracenoop.NewTracerProvider()
)
