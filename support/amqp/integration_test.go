package amqp

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// TestIntegrationDirectPublishConsume 需要真实 RabbitMQ：
//
//	GOFY_TEST_AMQP_ADDR=127.0.0.1:5672 \
//	GOFY_TEST_AMQP_USERNAME=rabbitmq \
//	GOFY_TEST_AMQP_PASSWORD=... \
//	GOFY_TEST_AMQP_VHOST=my_vhost \
//	  go test ./support/amqp -run Integration -v
func TestIntegrationDirectPublishConsume(t *testing.T) {
	host := integrationHost(t)
	client, err := New(context.Background(), Config{Hosts: []Host{host}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	closeClient(t, client)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	exchange := "gofy.test.direct." + suffix
	queue := exchange
	q, err := NewQueue(client, QueueConfig{
		Topology: Topology{
			Exchange:   exchange,
			Queue:      queue,
			RoutingKey: "message",
			AutoDelete: true,
		},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	cleanupTopology(t, client, exchange, queue)

	if err := q.Publish(context.Background(), []byte("hello"), PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	received := make(chan string, 1)
	consumed := make(chan error, 1)
	go func() {
		consumed <- q.Consume(ctx, func(_ context.Context, msg amqp091.Delivery) error {
			received <- string(msg.Body)
			cancel()
			return nil
		}, ConsumeOptions{})
	}()

	select {
	case body := <-received:
		if body != "hello" {
			t.Fatalf("body = %q", body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the message")
	}
	if err := <-consumed; err != nil {
		t.Fatalf("Consume: %v", err)
	}
}

func TestIntegrationDelayQueue(t *testing.T) {
	host := integrationHost(t)
	client, err := New(context.Background(), Config{Hosts: []Host{host}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	closeClient(t, client)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	sourceExchange := "gofy.test.delay.src." + suffix
	sourceQueue := sourceExchange
	targetExchange := "gofy.test.delay.dst." + suffix
	targetQueue := targetExchange
	q, err := NewQueue(client, QueueConfig{
		Topology: Topology{
			Exchange:   sourceExchange,
			Queue:      sourceQueue,
			RoutingKey: "delayed",
			AutoDelete: true,
		},
		Delay: &DelayConfig{
			TTL: 300 * time.Millisecond,
			Target: Topology{
				Exchange:   targetExchange,
				Queue:      targetQueue,
				RoutingKey: "ready",
				AutoDelete: true,
			},
		},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	cleanupTopology(t, client, sourceExchange, sourceQueue)
	cleanupTopology(t, client, targetExchange, targetQueue)

	start := time.Now()
	if err := q.Publish(context.Background(), []byte("later"), PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	received := make(chan string, 1)
	consumed := make(chan error, 1)
	go func() {
		consumed <- q.Consume(ctx, func(_ context.Context, msg amqp091.Delivery) error {
			received <- string(msg.Body)
			cancel()
			return nil
		}, ConsumeOptions{})
	}()

	select {
	case body := <-received:
		if body != "later" {
			t.Fatalf("body = %q", body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the delayed message")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("message arrived after %s, before the 300ms ttl", elapsed)
	}
	if err := <-consumed; err != nil {
		t.Fatalf("Consume: %v", err)
	}
}

func TestIntegrationHandlerErrorRequeues(t *testing.T) {
	host := integrationHost(t)
	client, err := New(context.Background(), Config{Hosts: []Host{host}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	closeClient(t, client)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	exchange := "gofy.test.retry." + suffix
	queue := exchange
	q, err := NewQueue(client, QueueConfig{
		Topology: Topology{
			Exchange:   exchange,
			Queue:      queue,
			RoutingKey: "message",
			AutoDelete: true,
		},
		Confirm: true,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	cleanupTopology(t, client, exchange, queue)

	if err := q.Publish(context.Background(), []byte("retry"), PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	consumed := make(chan error, 1)
	go func() {
		consumed <- q.Consume(ctx, func(_ context.Context, msg amqp091.Delivery) error {
			attempts++
			if attempts == 1 {
				return errRetry
			}
			cancel()
			return nil
		}, ConsumeOptions{})
	}()

	if err := <-consumed; err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("handler ran %d times, want a redelivery after the nack", attempts)
	}
}

var errRetry = errors.New("retry this message")

func TestIntegrationMultiHostFallback(t *testing.T) {
	host := integrationHost(t)
	unreachable := Host{
		Host:     "127.0.0.1",
		Port:     1,
		Username: host.Username,
		Password: host.Password,
		Vhost:    host.Vhost,
	}
	client, err := New(context.Background(), Config{
		Hosts:             []Host{unreachable, host},
		ConnectionTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New with fallback host: %v", err)
	}
	defer func() { _ = client.Close() }()

	ch, err := client.Channel()
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	_ = ch.Close()
}

func TestIntegrationWrap(t *testing.T) {
	host := integrationHost(t).withDefaults()
	conn, err := amqp091.Dial(host.uri())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	client := Wrap(conn)
	defer func() { _ = client.Close() }()

	ch, err := client.Channel()
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("close channel: %v", err)
	}
}

func integrationHost(t *testing.T) Host {
	t.Helper()

	addr := os.Getenv("GOFY_TEST_AMQP_ADDR")
	if addr == "" {
		t.Skip("set GOFY_TEST_AMQP_ADDR to run the AMQP integration tests")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("GOFY_TEST_AMQP_ADDR: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("GOFY_TEST_AMQP_ADDR port: %v", err)
	}
	return Host{
		Host:     host,
		Port:     port,
		Username: envOrDefault("GOFY_TEST_AMQP_USERNAME", "guest"),
		Password: os.Getenv("GOFY_TEST_AMQP_PASSWORD"),
		Vhost:    envOrDefault("GOFY_TEST_AMQP_VHOST", "/"),
	}
}

func cleanupTopology(t *testing.T, client *Client, exchange, queue string) {
	t.Helper()

	t.Cleanup(func() {
		ch, err := client.Channel()
		if err != nil {
			return
		}
		_, _ = ch.QueueDelete(queue, false, false, false)
		_ = ch.ExchangeDelete(exchange, false, false)
		_ = ch.Close()
	})
}

func closeClient(t *testing.T, client *Client) {
	t.Helper()
	t.Cleanup(func() { _ = client.Close() })
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
