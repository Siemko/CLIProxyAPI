package cliproxy

import (
	"context"
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityEffortVariantsRequireAccountAndKnownFamily(t *testing.T) {
	template := &ModelInfo{
		ID: "gemini-4-hydrogen-xhigh", Name: "gemini-4-hydrogen-xhigh", DisplayName: "Gemini 4 Hydrogen",
		OwnedBy: "antigravity", Type: "antigravity", ContextLength: 1048576, MaxCompletionTokens: 262144,
		Thinking: &registry.ThinkingSupport{Min: 1, Max: 65535, Levels: []string{"low", "medium", "high", "xhigh"}},
	}
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{
		"gemini-4-hydrogen-low": {}, "gemini-4-hydrogen-medium": {}, "gemini-4-hydrogen-high": {},
		"gemini-4-hydrogen-xhigh": {}, "gemini-4-hydrogen-tiered-high": {}, "gemini-4-hydrogen-tiered-xhigh": {},
		"gemini-4-hydrogen-unknown": {}, "gemini-99-unknown-low": {}, "gemini-4-hydrogen-disabled": {},
	}, WebSearchModelIDs: map[string]struct{}{"gemini-4-hydrogen-low": {}}}
	models := filterAntigravityModels([]*ModelInfo{nil, template}, hints)
	want := []string{"gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-high", "gemini-4-hydrogen-low", "gemini-4-hydrogen-medium", "gemini-4-hydrogen-tiered-high", "gemini-4-hydrogen-tiered-xhigh"}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
		if model.ContextLength != template.ContextLength || model.MaxCompletionTokens != template.MaxCompletionTokens {
			t.Fatalf("variant lost family token limits: %+v", model)
		}
		if model.ID != template.ID && (model.Name != model.ID || model.Thinking == template.Thinking) {
			t.Fatalf("variant metadata was not independently cloned: %+v", model)
		}
		if model.ID == "gemini-4-hydrogen-low" {
			if model.DisplayName != "Gemini 4 Hydrogen (Low)" || !model.SupportsWebSearch {
				t.Fatalf("incorrect low variant metadata: %+v", model)
			}
			model.Thinking.Levels[0] = "changed"
		}
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("models = %v, want %v", ids, want)
	}
	if template.ID != "gemini-4-hydrogen-xhigh" || template.Thinking.Levels[0] != "low" {
		t.Fatal("variant registration mutated the canonical catalog")
	}
	if got := filterAntigravityModels([]*ModelInfo{template}, antigravityModelCapabilityHints{}); len(got) != 0 {
		t.Fatalf("unknown account catalog exposed models: %v", got)
	}
	limited := *template
	limited.Thinking = &registry.ThinkingSupport{Levels: []string{"high"}}
	if got := filterAntigravityModels([]*ModelInfo{&limited}, antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-4-hydrogen-low": {}}}); len(got) != 0 {
		t.Fatalf("unsupported reasoning level exposed: %v", got)
	}
}

func TestAntigravityEffortVariantsRespectExclusionsAliasesAndPrefixes(t *testing.T) {
	auth := antigravityTestAuth(t.Name(), "http://127.0.0.1:1")
	auth.Prefix = "tenant"
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{
		"gemini-4-hydrogen-low": {}, "gemini-4-hydrogen-medium": {}, "gemini-4-hydrogen-high": {}, "gemini-4-hydrogen-xhigh": {},
	}}
	cfg := &config.Config{
		OAuthExcludedModels: map[string][]string{"antigravity": {"gemini-4-hydrogen-medium"}},
		OAuthModelAlias:     map[string][]config.OAuthModelAlias{"antigravity": {{Name: "gemini-4-hydrogen-low", Alias: "hydrogen-low", Fork: true}}},
	}
	service := &Service{cfg: cfg}
	present := map[string]bool{}
	for _, model := range service.antigravityModelsForHints(auth, hints) {
		present[model.ID] = true
	}
	for _, id := range []string{"gemini-4-hydrogen-low", "hydrogen-low", "tenant/hydrogen-low", "tenant/gemini-4-hydrogen-low", "gemini-4-hydrogen-xhigh"} {
		if !present[id] {
			t.Errorf("missing registered model %s", id)
		}
	}
	if present["gemini-4-hydrogen-medium"] || present["tenant/gemini-4-hydrogen-medium"] {
		t.Fatal("excluded variant was reintroduced")
	}
}

func TestAntigravityEffortVariantsRouteExactModelIDs(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		name := "bare"
		if affinity {
			name = "affinity"
		}
		t.Run(name, func(t *testing.T) {
			ctx := coreauth.WithSkipPersist(context.Background())
			var selector coreauth.Selector = &coreauth.SoonestResetFirstSelector{}
			if affinity {
				sticky := coreauth.NewSessionAffinitySelector(selector)
				t.Cleanup(sticky.Stop)
				selector = sticky
			}
			manager := coreauth.NewManager(nil, selector, nil)
			capture := &antigravityVariantCaptureExecutor{noopServiceIssue7431Executor: noopServiceIssue7431Executor{provider: "antigravity"}}
			manager.RegisterExecutor(capture)
			auth := antigravityTestAuth(t.Name(), "http://127.0.0.1:1")
			if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			ids := []string{"gemini-4-hydrogen-low", "gemini-4-hydrogen-medium", "gemini-4-hydrogen-high", "gemini-4-hydrogen-xhigh", "claude-opus-5-5-low", "claude-sonnet-5-5-medium"}
			hints := antigravityModelCapabilityHints{ModelIDs: make(map[string]struct{})}
			for _, id := range ids {
				hints.ModelIDs[id] = struct{}{}
			}
			service := &Service{cfg: &config.Config{}, coreManager: manager}
			models := service.antigravityModelsForHints(auth, hints)
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "antigravity", models)
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation_id":"antigravity-variants"}`)}
			for _, id := range ids {
				selected, errPick := manager.SelectAuth(ctx, "antigravity", id, opts)
				if errPick != nil || selected == nil || selected.ID != auth.ID {
					t.Fatalf("variant %s: auth=%v error=%v", id, selected, errPick)
				}
				info := registry.GetGlobalRegistry().GetModelInfo(id, "antigravity")
				if info == nil || info.ID != id || info.Name != id {
					t.Fatalf("variant %s was silently mapped to another model: %+v", id, info)
				}
				response, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: id}, opts)
				if errExecute != nil || string(response.Payload) != id {
					t.Fatalf("variant %s non-stream forwarding: payload=%s error=%v", id, response.Payload, errExecute)
				}
				streamOpts := opts
				streamOpts.Stream = true
				stream, errStream := manager.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: id}, streamOpts)
				if errStream != nil || stream == nil {
					t.Fatalf("variant %s stream forwarding: stream=%v error=%v", id, stream, errStream)
				}
				for chunk := range stream.Chunks {
					if chunk.Err != nil || string(chunk.Payload) != id {
						t.Fatalf("variant %s stream payload=%s error=%v", id, chunk.Payload, chunk.Err)
					}
				}
			}
		})
	}
}

type antigravityVariantCaptureExecutor struct {
	noopServiceIssue7431Executor
}

func (e *antigravityVariantCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *antigravityVariantCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(req.Model)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
