package amqp

import (
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

func TestConfigValidate(t *testing.T) {
	valid := Host{Host: "broker", Port: 5672, Username: "user", Vhost: "/"}
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"valid", Config{Hosts: []Host{valid}}, true},
		{"no hosts", Config{}, false},
		{"empty host", Config{Hosts: []Host{{Port: 5672, Username: "user"}}}, false},
		{"missing username", Config{Hosts: []Host{{Host: "broker"}}}, false},
		{"negative port", Config{Hosts: []Host{{Host: "broker", Port: -1, Username: "user"}}}, false},
		{"port too large", Config{Hosts: []Host{{Host: "broker", Port: 70000, Username: "user"}}}, false},
		{"invalid scheme", Config{Hosts: []Host{{Host: "broker", Username: "user", Scheme: "http"}}}, false},
		{"negative heartbeat", Config{Hosts: []Host{valid}, Heartbeat: -time.Second}, false},
		{"negative timeout", Config{Hosts: []Host{valid}, ConnectionTimeout: -time.Second}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("Validate accepted an invalid config")
			}
		})
	}
}

func TestConfigWithDefaults(t *testing.T) {
	cfg := Config{Hosts: []Host{{Host: " broker ", Username: "user"}}}.withDefaults()
	host := cfg.Hosts[0]

	if host.Host != "broker" || host.Port != 5672 || host.Scheme != "amqp" || host.Vhost != "/" {
		t.Fatalf("host defaults = %+v", host)
	}
	if cfg.Heartbeat != 10*time.Second || cfg.ConnectionTimeout != 5*time.Second || cfg.Locale != "en_US" {
		t.Fatalf("config defaults = %+v", cfg)
	}
}

func TestHostURIRoundTripsReservedCharacters(t *testing.T) {
	host := Host{
		Host:     "broker",
		Port:     5671,
		Username: "user@example",
		Password: "p@ss:w/rd#?",
		Vhost:    "my_vhost",
		Scheme:   "amqps",
	}
	parsed, err := amqp091.ParseURI(host.uri())
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if parsed.Scheme != "amqps" || parsed.Host != "broker" || parsed.Port != 5671 {
		t.Fatalf("endpoint = %+v", parsed)
	}
	if parsed.Username != host.Username || parsed.Password != host.Password {
		t.Fatalf("credentials did not round-trip: %+v", parsed)
	}
	if parsed.Vhost != "my_vhost" {
		t.Fatalf("vhost = %q", parsed.Vhost)
	}
}

func TestHostURIKeepsDefaultVhost(t *testing.T) {
	parsed, err := amqp091.ParseURI(Host{Host: "broker", Username: "user", Vhost: "/"}.withDefaults().uri())
	if err != nil {
		t.Fatalf("ParseURI: %v", err)
	}
	if parsed.Vhost != "/" {
		t.Fatalf("vhost = %q", parsed.Vhost)
	}
}

func TestHostAddressOmitsCredentials(t *testing.T) {
	host := Host{Host: "broker", Port: 5672, Username: "user", Password: "secret"}.withDefaults()
	if got := host.address(); got != "broker:5672" {
		t.Fatalf("address = %q", got)
	}
}
