package queue

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/config"
)

func TestBuildClients_RabbitMQ(t *testing.T) {
	clients, skips := BuildClients([]config.QueueEndpoint{
		{Name: "rmq", Kind: "rabbitmq", URL: "http://localhost:15672"},
		{Name: "rmq-upper", Kind: "RabbitMQ", URL: "http://other:15672"}, // kind is case-insensitive
	})
	if len(skips) != 0 {
		t.Fatalf("valid endpoints should not skip, got: %v", skips)
	}
	if len(clients.RabbitMQ) != 2 {
		t.Fatalf("want 2 rabbitmq clients, got %d", len(clients.RabbitMQ))
	}
	if clients.Empty() {
		t.Error("Empty() should be false when a client is configured")
	}
}

func TestBuildClients_SkipsMalformed(t *testing.T) {
	clients, skips := BuildClients([]config.QueueEndpoint{
		{Name: "no-url", Kind: "rabbitmq"},
		{Name: "no-kind"},
		{Name: "exotic", Kind: "pulsar", URL: "http://x"},
		{Name: "good", Kind: "rabbitmq", URL: "http://localhost:15672"},
		{Name: "no-brokers", Kind: "kafka"},
		{Name: "bad-sasl", Kind: "kafka", Brokers: "b:9092", SASLMechanism: "kerberos"},
	})
	if len(clients.RabbitMQ) != 1 {
		t.Fatalf("only the well-formed rabbitmq endpoint should build, got %d", len(clients.RabbitMQ))
	}
	joined := strings.Join(skips, " | ")
	for _, want := range []string{
		"missing name or url", "missing kind", "unsupported kind \"pulsar\"",
		"missing name or brokers", "requires sasl_user and a non-empty PasswordEnv",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("skip reasons should mention %q; got: %s", want, joined)
		}
	}
}

func TestBuildClients_Kafka(t *testing.T) {
	clients, skips := BuildClients([]config.QueueEndpoint{
		{Name: "kfk", Kind: "kafka", Brokers: "b1:9092,b2:9092"},
	})
	if len(skips) != 0 {
		t.Fatalf("a valid kafka endpoint should not skip, got: %v", skips)
	}
	if len(clients.Kafka) != 1 {
		t.Fatalf("want 1 kafka client, got %d", len(clients.Kafka))
	}
	if clients.Empty() {
		t.Error("Empty() should be false with a kafka client configured")
	}
	clients.Close()
}

func TestBuildClients_EmptyWhenNoEndpoints(t *testing.T) {
	clients, skips := BuildClients(nil)
	if !clients.Empty() {
		t.Error("no endpoints should yield an empty client set")
	}
	if len(skips) != 0 {
		t.Errorf("no endpoints should produce no skip reasons, got: %v", skips)
	}
}

func TestPickRabbitMQ(t *testing.T) {
	a, _ := httpapi.NewClient("a", "http://a", httpapi.Auth{})
	b, _ := httpapi.NewClient("b", "http://b", httpapi.Auth{})

	// Single endpoint, no name → that one.
	single := map[string]*httpapi.Client{"a": a}
	if got, err := pickRabbitMQ(single, ""); err != nil || got != a {
		t.Errorf("single-endpoint default failed: got %v, %v", got, err)
	}

	// Named lookup wins.
	pair := map[string]*httpapi.Client{"a": a, "b": b}
	if got, err := pickRabbitMQ(pair, "b"); err != nil || got != b {
		t.Errorf("named lookup failed: got %v, %v", got, err)
	}

	// Ambiguous (>1, no name) → error.
	if _, err := pickRabbitMQ(pair, ""); err == nil {
		t.Error("ambiguous endpoint with no name should error")
	}

	// Unknown name → error.
	if _, err := pickRabbitMQ(pair, "nope"); err == nil {
		t.Error("unknown endpoint name should error")
	}
}
