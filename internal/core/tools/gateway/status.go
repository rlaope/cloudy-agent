package gateway

import (
	"context"
	"fmt"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/core/tools"
	gatewaystatus "github.com/rlaope/cloudy/internal/gateway"
	"github.com/rlaope/cloudy/internal/secrets"
)

func newStatusTool() tools.Tool {
	schema := tools.MustJSON(map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	})
	return tools.Spec[struct{}]{
		Name:        "gateway.status",
		Description: "Report the local ChatOps gateway setup status: enabled platforms, required Discord/Slack/Telegram inputs, missing env-backed secrets, public URL, listen address, and session-map path. Read-only; no network calls.",
		Schema:      schema,
		Run: func(context.Context, struct{}) (tools.Observation, error) {
			_ = secrets.Load()
			cfg, err := config.Load(config.Path())
			if err != nil {
				return tools.Observation{}, fmt.Errorf("gateway.status: config: %w", err)
			}
			rep := gatewaystatus.Status(cfg)
			return tools.Observation{
				Text: gatewaystatus.FormatText(rep),
				Raw:  rep,
			}, nil
		},
	}.Build()
}
