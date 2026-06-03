package pgq_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joschi/pgq"
)

type Handler struct{}

func (h *Handler) HandleMessage(ctx context.Context, msg *pgq.MessageIncoming) (res bool, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		log.Println("Recovered in 'Handler.HandleMessage()'", r)
		// nack the message, it will be retried
		res = pgq.MessageNotProcessed
		if e, ok := r.(error); ok {
			err = e
		} else {
			err = fmt.Errorf("%v", r)
		}
	}()
	if msg.Metadata["heaviness"] == "heavy" {
		// nack the message, it will be retried
		// Message won't contain error detail in the database.
		return pgq.MessageNotProcessed, nil
	}
	var myPayload struct {
		Foo string `json:"foo"`
	}
	if err := json.Unmarshal(msg.Payload, &myPayload); err != nil {
		// discard the message, it will not be retried
		// Message will contain error detail in the database.
		return pgq.MessageProcessed, fmt.Errorf("invalid payload: %v", err)
	}
	// doSomethingWithThePayload(ctx, myPayload)
	return pgq.MessageProcessed, nil
}

func ExampleConsumer() {
	config, err := pgxpool.ParseConfig("user=postgres password=postgres host=localhost port=5432 dbname=postgres")
	if err != nil {
		log.Fatal("Error parsing database config:", err)
	}
	db, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatal("Error opening database:", err)
	}
	defer db.Close()
	const queueName = "test_queue"
	c, err := pgq.NewConsumer(db, queueName, &Handler{})
	if err != nil {
		log.Fatal("Error creating consumer:", err)
	}
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal("Error running consumer:", err)
	}
}

// ExampleConsumer_Shutdown demonstrates a graceful drain on SIGINT/SIGTERM:
// Run is started in a goroutine, and when a signal arrives Shutdown is
// called with a bounded grace period. In-flight handlers run to their
// per-message deadline; Run returns nil once they finish.
func ExampleConsumer_Shutdown() {
	config, err := pgxpool.ParseConfig("user=postgres password=postgres host=localhost port=5432 dbname=postgres")
	if err != nil {
		log.Fatal("Error parsing database config:", err)
	}
	db, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatal("Error opening database:", err)
	}
	defer db.Close()
	const queueName = "test_queue"
	c, err := pgq.NewConsumer(db, queueName, &Handler{})
	if err != nil {
		log.Fatal("Error creating consumer:", err)
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runErr := make(chan error, 1)
	go func() { runErr <- c.Run(context.Background()) }()

	<-sigCtx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Shutdown(shutdownCtx); err != nil {
		log.Println("Shutdown timed out; in-flight handlers may still be running:", err)
	}

	if err := <-runErr; err != nil {
		log.Fatal("Error running consumer:", err)
	}
}
