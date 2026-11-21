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

// TestIntegrationDirectPublishConsume requires a real RabbitMQ broker and is
// skipped unless the GOFY_TEST_AMQP_* variables below are set.
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

// TestIntegrationClosedConnectionFailsPublish: a client closed on purpose must
// make Publish fail immediately with ErrClientClosed — shutdown must not hang
// on a publish, and a closed client must not silently reconnect.
// TestIntegrationClosedConnectionFailsPublish：主动关闭的客户端必须让 Publish
// 立刻以 ErrClientClosed 失败——停机不能卡在发布上，已关闭的客户端也不得静默重连。
func TestIntegrationClosedConnectionFailsPublish(t *testing.T) {
	host := integrationHost(t)
	client, err := New(context.Background(), Config{Hosts: []Host{host}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	queue := "gofy.test.closed." + suffix
	q, err := NewQueue(client, QueueConfig{
		Topology: Topology{Exchange: queue, Queue: queue, RoutingKey: "message", AutoDelete: true},
		Confirm:  true,
	})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	cleanupTopology(t, client, queue, queue)

	if err := q.Publish(context.Background(), []byte("before-close"), PublishOptions{}); err != nil {
		t.Fatalf("Publish before close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- q.Publish(context.Background(), []byte("after-close"), PublishOptions{})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClientClosed) {
			t.Fatalf("Publish after Close = %v, want ErrClientClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Publish after Close did not fail fast")
	}
}

// TestIntegrationQueueRecreatedAfterDelete is the connection-drop + queue
// rebuild scenario in one: deleting the queue behind a live consumer breaks
// its channel, Consume reconnects, re-declares the topology and receives
// messages published afterwards. The deletion is expected to lose the first
// message — the assertion is that the flow recovers without a restart.
// TestIntegrationQueueRecreatedAfterDelete 把「连接断开 + 队列重建」合成一个
// 场景：删除存活消费者身后的队列会打断它的 channel，Consume 重连、重新声明
// 拓扑并收到之后发布的消息。第一条消息随删除丢失是预期——断言的是流程
// 不重启就能恢复。
func TestIntegrationQueueRecreatedAfterDelete(t *testing.T) {
	host := integrationHost(t)
	client, err := New(context.Background(), Config{Hosts: []Host{host}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	closeClient(t, client)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	exchange := "gofy.test.rebuild." + suffix
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ready := make(chan struct{}, 4)
	reconnectErrors := make(chan error, 8)
	consumed := make(chan string, 4)
	go func() {
		_ = q.Consume(ctx, func(_ context.Context, msg amqp091.Delivery) error {
			consumed <- string(msg.Body)
			return nil
		}, ConsumeOptions{
			OnReady: func() { ready <- struct{}{} },
			OnError: func(err error) {
				select {
				case reconnectErrors <- err:
				default:
				}
			},
		})
	}()

	// Wait for the first subscription to be live, then drop the queue from an
	// independent channel — the broker kills the consumer's channel on the spot.
	// 等第一次订阅就绪后，用独立 channel 删队列——broker 会立刻掐断消费者的 channel。
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("consumer never became ready")
	}
	drop, err := client.Channel()
	if err != nil {
		t.Fatalf("drop channel: %v", err)
	}
	if _, err := drop.QueueDelete(queue, false, false, false); err != nil {
		t.Fatalf("QueueDelete: %v", err)
	}
	_ = drop.Close()

	// The consumer reconnects (OnError may or may not surface to our channel
	// first) and re-declares the queue. The pre-delete publish lands nowhere;
	// after the rebuild the next publish is delivered.
	// 消费者重连（OnError 是否先冒到我们的管道不作断言）并重新声明队列。删除前
	// 发布的消息落空；重建后下一条发布应被送达。
	rebuildDeadline := time.Now().Add(20 * time.Second)
	delivered := false
	for attempt := 1; time.Now().Before(rebuildDeadline); attempt++ {
		if err := q.Publish(context.Background(), []byte("after-rebuild"), PublishOptions{}); err != nil {
			// The queue may not be re-declared yet; retry until the deadline.
			// 队列可能尚未重新声明；在期限内重试。
			time.Sleep(200 * time.Millisecond)
			continue
		}
		select {
		case body := <-consumed:
			if body != "after-rebuild" {
				t.Fatalf("body = %q", body)
			}
			delivered = true
		case <-time.After(5 * time.Second):
			// The publish may have raced the re-declare: try again.
			// 发布可能与重新声明竞争：再试一轮。
			continue
		}
		break
	}
	if !delivered {
		select {
		case err := <-reconnectErrors:
			t.Fatalf("queue never recovered after delete; last consume error: %v", err)
		default:
			t.Fatal("queue never recovered after delete")
		}
	}
}
