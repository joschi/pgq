package pgq

import (
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

var (
	keyQueueName  = attribute.Key("queue_name")
	keyMessageID  = attribute.Key("message_id")
	keyResolution = attribute.Key("resolution")

	noopMeter  = metricnoop.NewMeterProvider().Meter("noop")
	noopTracer = tracenoop.NewTracerProvider().Tracer("noop")
)
