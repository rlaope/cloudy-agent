package chatops

import (
	"fmt"
	"strings"
)

// ChunkText splits platform responses without cutting inside a rune.
func ChunkText(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		return []string{text}
	}
	var chunks []string
	var b strings.Builder
	count := 0
	for _, r := range text {
		if count >= maxRunes {
			chunks = append(chunks, b.String())
			b.Reset()
			count = 0
		}
		b.WriteRune(r)
		count++
	}
	if b.Len() > 0 || len(chunks) == 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// FormatMessage applies the minimum platform-specific escaping needed before
// handing text to delivery clients.
func FormatMessage(platform string, msg Message) string {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = "Cloudy returned no text."
	}
	if msg.SessionID != "" {
		text += fmt.Sprintf("\n\nsession: `%s`", msg.SessionID)
	}
	switch platform {
	case PlatformSlack:
		return suppressSlackMentions(text)
	case PlatformDiscord:
		return suppressDiscordMentions(text)
	case PlatformTelegram:
		return escapeTelegramMarkdownV2(text)
	default:
		return text
	}
}

func suppressDiscordMentions(s string) string {
	replacer := strings.NewReplacer(
		"@everyone", "@\u200beveryone",
		"@here", "@\u200bhere",
	)
	return replacer.Replace(s)
}

func suppressSlackMentions(s string) string {
	replacer := strings.NewReplacer(
		"<!here", "<!\u200bhere",
		"<!channel", "<!\u200bchannel",
		"<!everyone", "<!\u200beveryone",
		"<!subteam", "<!\u200bsubteam",
		"<@", "<@\u200b",
	)
	return replacer.Replace(s)
}

func escapeTelegramMarkdownV2(s string) string {
	replacer := strings.NewReplacer(
		"_", `\_`,
		"*", `\*`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"~", `\~`,
		"`", "\\`",
		">", `\>`,
		"#", `\#`,
		"+", `\+`,
		"-", `\-`,
		"=", `\=`,
		"|", `\|`,
		"{", `\{`,
		"}", `\}`,
		".", `\.`,
		"!", `\!`,
	)
	return replacer.Replace(s)
}
