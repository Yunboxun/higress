package router

import (
	"strings"
	"testing"
)

func TestRewriteRequestModel(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	got, err := rewriteRequestModel(body, "model", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	if !strings.Contains(string(got), `"model":"gpt-4o-mini"`) {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestRewriteRequestModelInvalidJSON(t *testing.T) {
	_, err := rewriteRequestModel([]byte(`not-json`), "model", "gpt-4o-mini")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
