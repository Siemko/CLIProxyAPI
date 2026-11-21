package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityRequestPreservesExactEffortVariant(t *testing.T) {
	for _, model := range []string{
		"gemini-4-hydrogen-low", "gemini-4-hydrogen-medium", "gemini-4-hydrogen-high", "gemini-4-hydrogen-xhigh",
		"gemini-4-neon-low", "gemini-4-argon-medium", "gemini-3.8-flash-low",
		"gemini-4-hydrogen-tiered-high", "claude-opus-5-5-low", "claude-sonnet-5-5-medium",
	} {
		for _, stream := range []bool{false, true} {
			mode := "/execute"
			if stream {
				mode = "/stream"
			}
			t.Run(model+mode, func(t *testing.T) {
				executor := &AntigravityExecutor{}
				auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "test-project"}}
				req, errRequest := executor.buildRequest(t.Context(), auth, "test-token", model, []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`), stream, "", "https://example.invalid")
				if errRequest != nil {
					t.Fatal(errRequest)
				}
				defer func() {
					if errClose := req.Body.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				body := requestBody(t, req)
				if body["model"] != model {
					t.Fatalf("upstream model = %v, want exact variant %s", body["model"], model)
				}
			})
		}
	}
}
