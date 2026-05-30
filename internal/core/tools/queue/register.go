package queue

import (
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
)

// RegisterAll adds every queue.* tool whose backend has at least one
// configured client. When no client was established, the "queue" group is
// marked skipped with a reason composed from per-endpoint build errors, so the
// startup banner explains why consumer-lag tools are absent.
func RegisterAll(reg *tools.Registry, clients Clients, skipReasons []string) {
	if clients.Empty() {
		reason := "no message-queue endpoints configured"
		if len(skipReasons) > 0 {
			reason = "no usable message-queue endpoints: " + strings.Join(skipReasons, "; ")
		}
		reg.MarkSkipped("queue", reason)
		return
	}

	if len(clients.RabbitMQ) > 0 {
		reg.MustRegister(
			newRabbitMQQueuesTool(clients.RabbitMQ),
		)
	}
	if len(clients.Kafka) > 0 {
		reg.MustRegister(
			newKafkaConsumerLagTool(clients.Kafka),
		)
	}
}
