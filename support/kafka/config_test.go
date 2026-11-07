package kafka

import (
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no brokers", Config{}, "at least one broker"},
		{"empty broker", Config{Brokers: []string{"127.0.0.1:9092", " "}}, "broker 1 is empty"},
		{"negative dial timeout", Config{Brokers: []string{"127.0.0.1:9092"}, DialTimeout: -time.Second}, "dial_timeout"},
		{"record retries below -1", Config{Brokers: []string{"127.0.0.1:9092"}, RecordRetries: -2}, "record_retries"},
		{"valid", Config{Brokers: []string{"127.0.0.1:9092"}}, ""},
		{"valid without port", Config{Brokers: []string{"kafka.internal"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate returned nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestConfigWithDefaults(t *testing.T) {
	cfg := Config{Brokers: []string{" 127.0.0.1:9092 ", "127.0.0.2:9092"}, ClientID: "  "}.withDefaults()

	if len(cfg.Brokers) != 2 || cfg.Brokers[0] != "127.0.0.1:9092" || cfg.Brokers[1] != "127.0.0.2:9092" {
		t.Fatalf("brokers = %#v", cfg.Brokers)
	}
	if cfg.ClientID != defaultClientID {
		t.Fatalf("client id = %q", cfg.ClientID)
	}
	if cfg.DialTimeout != defaultDialTimeout {
		t.Fatalf("dial timeout = %s", cfg.DialTimeout)
	}
	if cfg.RecordRetries != defaultRecordRetries {
		t.Fatalf("record retries = %d", cfg.RecordRetries)
	}
	if cfg.Idempotent {
		t.Fatal("idempotence must default to off")
	}
}

func TestConfigWithDefaultsKeepsExplicitValues(t *testing.T) {
	cfg := Config{
		Brokers:       []string{"broker:9092"},
		ClientID:      "svc",
		DialTimeout:   time.Second,
		Idempotent:    true,
		RecordRetries: -1,
	}.withDefaults()

	if cfg.ClientID != "svc" || cfg.DialTimeout != time.Second || !cfg.Idempotent || cfg.RecordRetries != -1 {
		t.Fatalf("defaults overrode explicit values: %+v", cfg)
	}
}
