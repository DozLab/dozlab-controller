package events

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Runs only when RABBITMQ_URL is set, e.g.
//
//	RABBITMQ_URL=amqp://guest:guest@localhost:5672/ go test ./internal/events/
func TestRabbitPublisherDeliversAndReconnects(t *testing.T) {
	url := os.Getenv("RABBITMQ_URL")
	if url == "" {
		t.Skip("RABBITMQ_URL not set; skipping RabbitMQ integration test")
	}
	pub := NewRabbitPublisher(url)
	defer pub.Close()
	ctx := context.Background()

	// The first publish declares the exchange; then bind a throwaway queue to it.
	if err := pub.PublishPhaseChange(ctx, PhaseChange{UID: "warmup", Phase: "Pending"}); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind(q.Name, TypeLabSessionPhaseChanged, ExchangeName, false, nil); err != nil {
		t.Fatal(err)
	}
	deliveries, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	change := PhaseChange{UID: "it-uid", Name: "lab-session-it", Namespace: "dozlab-labs", UserID: "u", SessionID: "s", Phase: "Running"}
	if err := pub.PublishPhaseChange(ctx, change); err != nil {
		t.Fatalf("publish: %v", err)
	}
	expect(t, deliveries, "it-uid.Running")

	// Drop the connection underneath the publisher; the next publish reconnects.
	pub.mu.Lock()
	pub.conn.Close()
	pub.mu.Unlock()
	change.Phase = "Terminating"
	if err := pub.PublishPhaseChange(ctx, change); err != nil {
		t.Fatalf("publish after drop: %v", err)
	}
	expect(t, deliveries, "it-uid.Terminating")
}

func expect(t *testing.T, deliveries <-chan amqp.Delivery, id string) {
	t.Helper()
	select {
	case d := <-deliveries:
		var e Event
		if err := json.Unmarshal(d.Body, &e); err != nil {
			t.Fatal(err)
		}
		if d.MessageId != id || e.ID != id || d.RoutingKey != TypeLabSessionPhaseChanged || d.DeliveryMode != amqp.Persistent {
			t.Errorf("got message %s (event %s, key %s, mode %d), want %s persistent on %s",
				d.MessageId, e.ID, d.RoutingKey, d.DeliveryMode, id, TypeLabSessionPhaseChanged)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no message %s", id)
	}
}
