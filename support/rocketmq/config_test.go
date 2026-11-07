package rocketmq

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	valid := Config{Endpoint: "127.0.0.1:8081"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := (Config{Endpoint: "127.0.0.1:8081", StartupTopics: []string{"orders"}}).Validate(); err != nil {
		t.Fatalf("optional startup topics: %v", err)
	}
	if err := (Config{Endpoint: "127.0.0.1:8081", SkipStartupCheck: true}).Validate(); err != nil {
		t.Fatalf("skipping the startup check must not require topics: %v", err)
	}

	tests := []struct {
		name string
		cfg  Config
		want error
	}{
		{
			name: "missing endpoint",
			cfg:  Config{StartupTopics: []string{"orders"}},
			want: ErrEndpointRequired,
		},
		{
			name: "empty startup topic",
			cfg:  Config{Endpoint: "127.0.0.1:8081", StartupTopics: []string{" "}},
			want: nil,
		},
		{
			name: "access key without secret",
			cfg: Config{
				Endpoint:      "127.0.0.1:8081",
				StartupTopics: []string{"orders"},
				AccessKey:     "key",
			},
			want: ErrCredentialsIncomplete,
		},
		{
			name: "secret without access key",
			cfg: Config{
				Endpoint:      "127.0.0.1:8081",
				StartupTopics: []string{"orders"},
				AccessSecret:  "secret",
			},
			want: ErrCredentialsIncomplete,
		},
		{
			name: "security token without keys",
			cfg: Config{
				Endpoint:      "127.0.0.1:8081",
				StartupTopics: []string{"orders"},
				SecurityToken: "token",
			},
			want: ErrCredentialsIncomplete,
		},
		{
			name: "negative timeout",
			cfg: Config{
				Endpoint:      "127.0.0.1:8081",
				StartupTopics: []string{"orders"},
				SendTimeout:   -time.Second,
			},
			want: nil,
		},
		{
			name: "negative attempts",
			cfg: Config{
				Endpoint:      "127.0.0.1:8081",
				StartupTopics: []string{"orders"},
				MaxAttempts:   -1,
			},
			want: nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.cfg.Validate()
			if err == nil {
				t.Fatal("Validate succeeded, want an error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Validate = %v, want %v", err, test.want)
			}
		})
	}
}

func TestConfigWithDefaults(t *testing.T) {
	cfg := Config{
		Endpoint:      " 127.0.0.1:8081 ",
		Namespace:     " ns ",
		AccessKey:     " key ",
		AccessSecret:  " secret ",
		SecurityToken: " token ",
		StartupTopics: []string{" orders ", "pay"},
	}.withDefaults()

	if cfg.Endpoint != "127.0.0.1:8081" || cfg.Namespace != "ns" {
		t.Fatalf("endpoint/namespace not trimmed: %+v", cfg)
	}
	if cfg.AccessKey != "key" || cfg.AccessSecret != "secret" || cfg.SecurityToken != "token" {
		t.Fatalf("credentials not trimmed: %+v", cfg)
	}
	if cfg.StartupTopics[0] != "orders" || cfg.StartupTopics[1] != "pay" {
		t.Fatalf("startup topics not trimmed: %v", cfg.StartupTopics)
	}
	if cfg.SendTimeout != defaultSendTimeout || cfg.MaxAttempts != defaultMaxAttempts {
		t.Fatalf("defaults not applied: %+v", cfg)
	}

	raw := cfg.rmqConfig("order-group")
	if raw.Endpoint != cfg.Endpoint || raw.NameSpace != cfg.Namespace || raw.ConsumerGroup != "order-group" {
		t.Fatalf("rmq config = %+v", raw)
	}
	if raw.Credentials == nil {
		t.Fatal("rmq config has nil credentials")
	}
	if raw.Credentials.AccessKey != "key" || raw.Credentials.AccessSecret != "secret" ||
		raw.Credentials.SecurityToken != "token" {
		t.Fatalf("rmq credentials = %+v", raw.Credentials)
	}
}

func TestConfigAlwaysProvidesCredentials(t *testing.T) {
	raw := Config{Endpoint: "127.0.0.1:8081"}.withDefaults().rmqConfig("")
	if raw.Credentials == nil {
		t.Fatal("the SDK dereferences Credentials, it must never be nil")
	}
	if raw.Credentials.AccessKey != "" || raw.Credentials.AccessSecret != "" {
		t.Fatalf("unexpected credentials: %+v", raw.Credentials)
	}
	empty := Config{}.withDefaults()
	if empty.StartupTopics == nil || len(empty.StartupTopics) != 0 {
		t.Fatalf("startup topics = %v, want an empty slice", empty.StartupTopics)
	}
}

func TestConfigValidateDoesNotLeakSecret(t *testing.T) {
	// A target without a port fails parsing, so the test stays fast; the
	// startup-check path itself is covered by TestNewFailsWhenEndpointUnreachable.
	cfg := Config{
		Endpoint:      "127.0.0.1",
		AccessKey:     "access",
		AccessSecret:  "super-secret-value",
		SendTimeout:   50 * time.Millisecond,
		StartupTopics: []string{"probe"},
	}
	_, err := New(context.Background(), cfg)
	if err == nil {
		t.Fatal("New succeeded, want an error")
	}
	if strings.Contains(err.Error(), cfg.AccessSecret) {
		t.Fatalf("error leaks the secret: %v", err)
	}
}
