package amqp

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// silentBroker accepts TCP connections and never answers the AMQP handshake.
// It makes the dial block in the handshake, which is the state a shutdown has
// to be able to interrupt.
// silentBroker 接受 TCP 连接但从不回应 AMQP 握手，让拨号停在握手阶段——正是停机
// 需要能打断的状态。
func silentBroker(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			// Deliberately no read: the client waits for Connection.Start
			// until its handshake deadline.
			// 刻意不读：客户端会一直等 Connection.Start 直到握手 deadline。
		}
	}()
	return ln.Addr().String()
}

// silentClient builds a managed client pointing at host without connecting:
// New would dial, and the test needs the client in the disconnected state.
// silentClient 构造一个指向 host 但未连接的受管客户端：New 会立即拨号，而测试
// 需要的是"已断开"这个初始状态。
func silentClient(t *testing.T, addr string, timeout time.Duration) *Client {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	cfg := Config{
		Hosts:             []Host{{Host: host, Port: n, Username: "guest", Vhost: "/"}},
		ConnectionTimeout: timeout,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	return &Client{cfg: cfg.withDefaults(), managed: true}
}

// TestChannelContextHonoursCancellationDuringHandshake: the reconnect must
// follow the caller's context. Without it, Channel dials with a background
// context and shutdown waits out every host's timeout.
// TestChannelContextHonoursCancellationDuringHandshake：重连必须跟随调用方
// context。否则 Channel 用后台 context 拨号，停机要等完每个 host 的超时。
func TestChannelContextHonoursCancellationDuringHandshake(t *testing.T) {
	client := silentClient(t, silentBroker(t), 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.ChannelContext(ctx)
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ChannelContext error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("cancellation took %v, want under 1s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ChannelContext ignored the cancelled context")
	}
}

// TestCloseIsNotBlockedByAnInFlightDial: Close must not queue behind a dial.
// The old implementation held the client mutex for the whole reconnect, so a
// shutdown waited for the network timeout.
// TestCloseIsNotBlockedByAnInFlightDial：Close 不能排在拨号之后。旧实现整段
// 重连都持客户端锁，停机因此要等网络超时。
func TestCloseIsNotBlockedByAnInFlightDial(t *testing.T) {
	client := silentClient(t, silentBroker(t), 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.ChannelContext(ctx)
		done <- err
	}()
	time.Sleep(150 * time.Millisecond) // let the dial reach the handshake

	start := time.Now()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(start)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the dial did not stop after cancellation")
	}
	if elapsed > time.Second {
		t.Fatalf("Close waited %v for the in-flight dial", elapsed)
	}
}
