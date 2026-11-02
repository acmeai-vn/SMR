package state

import "testing"

func TestExtractResponsesLastUser(t *testing.T) {
	payload := map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "first"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "latest"}}},
		},
	}
	if got := Extract("responses", payload); got != "latest" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractMessagesSkipsToolResult(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "fix the test"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "running"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "content": "output"}}},
		},
	}
	if got := Extract("messages", payload); got != "fix the test" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractToolOnlyFallsBackToAssistantButNotTool(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "content": "secret tool output"}}},
		},
	}
	if got := Extract("messages", payload); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestTruncateKeepsBeginning(t *testing.T) {
	if got := Truncate("abcdef", 3); got != "abc" {
		t.Fatalf("got %q", got)
	}
}
