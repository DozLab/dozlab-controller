// Package events publishes LabSession phase changes to the DozLab event bus
// (RabbitMQ), where dozlab-api consumes them.
//
// Events go to the durable topic exchange "dozlab.events" with the event type as
// the routing key, in the JSON shape of dozlab-api's websocket.Event.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// ExchangeName is the topic exchange dozlab-api declares and consumes from.
	ExchangeName = "dozlab.events"
	// TypeLabSessionPhaseChanged is the event type (and routing key) of a phase change.
	TypeLabSessionPhaseChanged = "labsession.phase_changed"
	// Source identifies the controller as the event's origin.
	Source = "dozlab-controller"

	defaultPublishTimeout = 10 * time.Second
)

// Event is the wire format shared with dozlab-api (websocket.Event).
type Event struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Source    string                 `json:"source"`
	SessionID string                 `json:"session_id,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	Data      map[string]interface{} `json:"data"`
	Timestamp time.Time              `json:"timestamp"`
}

// PhaseChange describes a LabSession that reached a new phase.
type PhaseChange struct {
	// UID is the LabSession's metadata.uid; with Phase it forms the event ID, so a
	// re-published phase has the same ID and consumers can deduplicate.
	UID       string
	Namespace string
	Name      string
	UserID    string
	SessionID string
	Phase     string
	Message   string
	Reason    string
	Endpoints map[string]string
}

// Event converts the phase change into the event published on the bus.
func (p PhaseChange) Event(now time.Time) Event {
	data := map[string]interface{}{
		"phase":     p.Phase,
		"message":   p.Message,
		"namespace": p.Namespace,
		"name":      p.Name,
	}
	if p.Reason != "" {
		data["reason"] = p.Reason
	}
	if len(p.Endpoints) > 0 {
		data["endpoints"] = p.Endpoints
	}
	return Event{
		ID:        p.UID + "." + p.Phase,
		Type:      TypeLabSessionPhaseChanged,
		Source:    Source,
		SessionID: p.SessionID,
		UserID:    p.UserID,
		Data:      data,
		Timestamp: now,
	}
}

// Publisher publishes LabSession phase changes.
type Publisher interface {
	PublishPhaseChange(ctx context.Context, change PhaseChange) error
}

// RabbitPublisher publishes with publisher confirms and persistent delivery. It
// connects on first use and reconnects on the next publish after a drop, so the
// controller keeps reconciling while the broker is unavailable.
type RabbitPublisher struct {
	url            string
	publishTimeout time.Duration

	mu     sync.Mutex
	conn   *amqp.Connection
	ch     *amqp.Channel
	closed bool
}

// NewRabbitPublisher returns a publisher for the broker at url. It does not connect yet.
func NewRabbitPublisher(url string) *RabbitPublisher {
	return &RabbitPublisher{url: url, publishTimeout: defaultPublishTimeout}
}

// PublishPhaseChange publishes the phase change and waits for the broker's confirm.
func (p *RabbitPublisher) PublishPhaseChange(ctx context.Context, change PhaseChange) error {
	event := change.Event(time.Now())
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.publishTimeout)
		defer cancel()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	ch, err := p.channel()
	if err != nil {
		return err
	}
	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, ExchangeName, event.Type, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    event.ID,
		Timestamp:    event.Timestamp,
		Type:         event.Type,
		AppId:        Source,
		Body:         body,
	})
	if err != nil {
		p.reset()
		return fmt.Errorf("publish: %w", err)
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		p.reset()
		return fmt.Errorf("wait for publisher confirm: %w", err)
	}
	if !acked {
		return errors.New("publish: broker did not confirm the message")
	}
	return nil
}

// channel returns an open confirm-mode channel, (re)connecting if needed. p.mu is held.
func (p *RabbitPublisher) channel() (*amqp.Channel, error) {
	if p.closed {
		return nil, errors.New("rabbitmq publisher closed")
	}
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}
	p.reset()

	conn, err := amqp.Dial(p.url)
	if err != nil {
		return nil, fmt.Errorf("connect to RabbitMQ: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	// Same arguments as dozlab-api, so either side can declare it first.
	if err := ch.ExchangeDeclare(ExchangeName, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("declare exchange %s: %w", ExchangeName, err)
	}
	if err := ch.Confirm(false); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enable publisher confirms: %w", err)
	}
	p.conn, p.ch = conn, ch
	return ch, nil
}

// reset drops the current connection so the next publish reconnects. p.mu is held.
func (p *RabbitPublisher) reset() {
	if p.conn != nil && !p.conn.IsClosed() {
		p.conn.Close()
	}
	p.conn, p.ch = nil, nil
}

// Close closes the connection; later publishes fail.
func (p *RabbitPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	var err error
	if p.conn != nil && !p.conn.IsClosed() {
		err = p.conn.Close()
	}
	p.conn, p.ch = nil, nil
	return err
}
