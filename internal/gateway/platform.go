package gateway

import (
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/config"
)

const (
	PlatformSlack    = "slack"
	PlatformDiscord  = "discord"
	PlatformTelegram = "telegram"
)

type platformSpec struct {
	name   string
	setup  func(setupWizard, *config.Config) error
	report func(config.ChatOpsConfig) PlatformReport
}

var platformSpecs = []platformSpec{
	{name: PlatformSlack, setup: setupWizard.setupSlack, report: slackReport},
	{name: PlatformDiscord, setup: setupWizard.setupDiscord, report: discordReport},
	{name: PlatformTelegram, setup: setupWizard.setupTelegram, report: telegramReport},
}

func lookupPlatform(name string) (platformSpec, bool) {
	name = normalizePlatform(name)
	for _, spec := range platformSpecs {
		if spec.name == name {
			return spec, true
		}
	}
	return platformSpec{}, false
}

func normalizePlatform(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func platformChoicePrompt() string {
	return strings.Join(platformNames(), "/")
}

// SupportedPlatformsText returns the operator-facing list of gateway platforms.
func SupportedPlatformsText() string {
	return platformListText()
}

func platformListText() string {
	return strings.Join(platformNames(), ", ")
}

func platformNames() []string {
	names := make([]string, 0, len(platformSpecs))
	for _, spec := range platformSpecs {
		names = append(names, spec.name)
	}
	return names
}

func unknownPlatformError() error {
	return fmt.Errorf("gateway setup: platform must be one of: %s", platformListText())
}
