// Package state extracts one representative text from a coding-agent request
// without reading system prompts, tool results, or provider credentials.
package state

import "strings"

// Extract returns the last user text, falling back to the last assistant text.
func Extract(api string, payload map[string]any) string {
	switch api {
	case "responses":
		if input, ok := payload["input"].(string); ok {
			return strings.TrimSpace(input)
		}
		return lastText(asSlice(payload["input"]))
	case "messages", "chat":
		return lastText(asSlice(payload["messages"]))
	default:
		if messages, ok := payload["messages"]; ok {
			return lastText(asSlice(messages))
		}
		if input, ok := payload["input"].(string); ok {
			return strings.TrimSpace(input)
		}
		return lastText(asSlice(payload["input"]))
	}
}

// Truncate keeps the beginning of the state, matching the server rule.
func Truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if maximum <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func lastText(items []any) string {
	lastAssistant := ""
	for i := len(items) - 1; i >= 0; i-- {
		message, ok := items[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}
		text := messageText(message["content"])
		if text == "" {
			continue
		}
		if role == "user" {
			return text
		}
		if lastAssistant == "" {
			lastAssistant = text
		}
	}
	return lastAssistant
}

func messageText(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		var parts []string
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := block["type"].(string)
			switch kind {
			case "text", "input_text", "output_text":
				text, _ := block["text"].(string)
				if strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}
