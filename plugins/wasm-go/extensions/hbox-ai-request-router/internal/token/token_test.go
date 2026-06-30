package token

import (
	"strings"
	"testing"
)

func TestEstimateInputTokensHeuristicMessages(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "hello world"}
		]
	}`)
	count := EstimateInputTokensHeuristic(body, 4)
	if count != 3 {
		t.Fatalf("expected 3 tokens, got %d", count)
	}
}

func TestEstimateInputTokensHeuristicMultimodal(t *testing.T) {
	body := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "abcd"},
					{"type": "image_url", "image_url": {"url": "http://example.com/a.png"}}
				]
			}
		]
	}`)
	count := EstimateInputTokensHeuristic(body, 4)
	if count != 1 {
		t.Fatalf("expected 1 token, got %d", count)
	}
}

func TestEstimateInputTokensHeuristicInputField(t *testing.T) {
	body := []byte(`{
		"model": "text-embedding-3-small",
		"input": "12345678"
	}`)
	count := EstimateInputTokensHeuristic(body, 4)
	if count != 2 {
		t.Fatalf("expected 2 tokens, got %d", count)
	}
}

func TestExtractSourceModel(t *testing.T) {
	body := []byte(`{"model":"deepseek-r1","messages":[{"role":"user","content":"hi"}]}`)
	if got := ExtractSourceModel(body, "model"); got != "deepseek-r1" {
		t.Fatalf("unexpected source model: %s", got)
	}
}

func TestParseTokenizeResponse(t *testing.T) {
	body := []byte(`{"count": 12345}`)
	if got := ParseTokenizeResponse(body); got != 12345 {
		t.Fatalf("expected 12345, got %d", got)
	}
	body = []byte(`{"usage":{"prompt_tokens":999}}`)
	if got := ParseTokenizeResponse(body); got != 999 {
		t.Fatalf("expected 999, got %d", got)
	}
	body = []byte(`{"usage":{"prompt_tokens":100,"total_tokens":200}}`)
	if got := ParseTokenizeResponse(body); got != 100 {
		t.Fatalf("expected prompt_tokens 100, got %d", got)
	}
	body = []byte(`{"count":0,"usage":{"prompt_tokens":999}}`)
	if got := ParseTokenizeResponse(body); got != 999 {
		t.Fatalf("expected 999 when count is zero, got %d", got)
	}
}

func TestExtractInputTextCombinesMessagesAndSystem(t *testing.T) {
	body := []byte(`{
		"system": "sys",
		"messages": [{"role":"user","content":"user"}]
	}`)
	text := ExtractInputText(body)
	if text != "user\nsys" {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestExtractInputTextToolCalls(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"role": "assistant",
			"tool_calls": [{
				"type": "function",
				"function": {
					"name": "get_weather",
					"arguments": "{\"city\":\"Boston\"}"
				}
			}]
		}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather",
				"parameters": {"type":"object"}
			}
		}]
	}`)
	text := ExtractInputText(body)
	for _, want := range []string{"get_weather", "Boston", "Get weather"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected text to contain %q, got %q", want, text)
		}
	}
}
