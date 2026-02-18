// Package nats provides a NATS client wrapper and bridge for inter-agent communication.
package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/helmcode/agent-crew/internal/protocol"
)

// ClientConfig holds the configuration for the NATS client.
type ClientConfig struct {
	URL              string
	Name             string // connection name for monitoring
	Token            string // auth token (optional, must match NATS server --auth flag)
	MaxReconnects    int
	ReconnectWait    time.Duration
	JetStreamEnabled bool
}

// DefaultConfig returns a ClientConfig with sensible defaults.
func DefaultConfig(url, name string) ClientConfig {
	return ClientConfig{
		URL:              url,
		Name:             name,
		MaxReconnects:    -1, // unlimited reconnects
		ReconnectWait:    2 * time.Second,
		JetStreamEnabled: true,
	}
}

// Client wraps a NATS connection with helpers for the AgentCrew protocol.
type Client struct {
	conn   *nats.Conn
	js     jetstream.JetStream
	config ClientConfig
	subs   []*nats.Subscription
}

// Connect establishes a connection to the NATS server.
func Connect(config ClientConfig) (*Client, error) {
	opts := []nats.Option{
		nats.Name(config.Name),
		nats.MaxReconnects(config.MaxReconnects),
		nats.ReconnectWait(config.ReconnectWait),
		nats.Timeout(5 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			slog.Info("nats reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			slog.Info("nats connection closed")
		}),
	}

	if config.Token != "" {
		opts = append(opts, nats.Token(config.Token))
	}

	nc, err := nats.Connect(config.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to nats %s: %w", config.URL, err)
	}

	client := &Client{
		conn:   nc,
		config: config,
	}

	if config.JetStreamEnabled {
		js, err := jetstream.New(nc)
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("creating jetstream context: %w", err)
		}
		client.js = js
	}

	slog.Info("nats connected", "url", config.URL, "name", config.Name)
	return client, nil
}

// EnsureStream creates or updates a JetStream stream for team message persistence.
func (c *Client) EnsureStream(ctx context.Context, teamName string) error {
	if c.js == nil {
		return fmt.Errorf("jetstream not enabled")
	}

	streamName := "TEAM_" + teamName
	subjects := []string{fmt.Sprintf("team.%s.>", teamName)}

	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  subjects,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    24 * time.Hour,
		Storage:   jetstream.MemoryStorage,
		Replicas:  1,
	})
	if err != nil {
		return fmt.Errorf("creating stream %s: %w", streamName, err)
	}

	slog.Info("jetstream stream ensured", "stream", streamName, "subjects", subjects)
	return nil
}

// Publish sends a protocol message to the specified NATS subject.
func (c *Client) Publish(subject string, msg *protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}
	return c.conn.Publish(subject, data)
}

// Subscribe registers a handler for messages on the given subject.
// The handler receives parsed protocol messages.
func (c *Client) Subscribe(subject string, handler func(*protocol.Message)) error {
	sub, err := c.conn.Subscribe(subject, func(natsMsg *nats.Msg) {
		var msg protocol.Message
		if err := json.Unmarshal(natsMsg.Data, &msg); err != nil {
			slog.Warn("failed to unmarshal nats message", "subject", subject, "error", err)
			return
		}
		handler(&msg)
	})
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", subject, err)
	}

	c.subs = append(c.subs, sub)
	slog.Debug("subscribed", "subject", subject)
	return nil
}

// Request sends a protocol message and waits for a reply within the given timeout.
// This implements the request/reply pattern used for leader → agent task assignment.
func (c *Client) Request(subject string, msg *protocol.Message, timeout time.Duration) (*protocol.Message, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	reply, err := c.conn.Request(subject, data, timeout)
	if err != nil {
		return nil, fmt.Errorf("request to %s: %w", subject, err)
	}

	var response protocol.Message
	if err := json.Unmarshal(reply.Data, &response); err != nil {
		return nil, fmt.Errorf("unmarshaling reply: %w", err)
	}
	return &response, nil
}

// QueueSubscribe registers a handler for messages using a queue group.
// This distributes messages among multiple subscribers in the same group.
func (c *Client) QueueSubscribe(subject, queue string, handler func(*protocol.Message)) error {
	sub, err := c.conn.QueueSubscribe(subject, queue, func(natsMsg *nats.Msg) {
		var msg protocol.Message
		if err := json.Unmarshal(natsMsg.Data, &msg); err != nil {
			slog.Warn("failed to unmarshal nats message", "subject", subject, "error", err)
			return
		}
		handler(&msg)
	})
	if err != nil {
		return fmt.Errorf("queue subscribing to %s: %w", subject, err)
	}

	c.subs = append(c.subs, sub)
	slog.Debug("queue subscribed", "subject", subject, "queue", queue)
	return nil
}

// Flush flushes the connection buffer to the server.
func (c *Client) Flush() error {
	return c.conn.Flush()
}

// Close drains all subscriptions and closes the connection.
func (c *Client) Close() {
	for _, sub := range c.subs {
		if err := sub.Drain(); err != nil {
			slog.Debug("draining subscription", "subject", sub.Subject, "error", err)
		}
	}
	c.conn.Close()
	slog.Info("nats client closed")
}

// IsConnected returns true if the client is currently connected.
func (c *Client) IsConnected() bool {
	return c.conn.IsConnected()
}
