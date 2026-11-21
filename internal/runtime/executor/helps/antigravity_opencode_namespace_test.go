package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestAntigravityOpenCodeNamespaceReplacementOnlyChangesSystemInstructions(t *testing.T) {
	matcher := BuildAntigravitySensitiveWordMatcher(nil, map[string]string{
		"tools.opencode.": `tools["opencode"].`,
	})
	for _, field := range []string{"systemInstruction", "system_instruction"} {
		for _, textOnly := range []bool{false, true} {
			doc := ojson.NewDoc([]byte(`{"model":"gemini-4-neon-high","request":{"contents":[{"role":"user","parts":[{"text":"Keep tools.opencode.session_move as source code."}]}],"tools":[{"functionDeclarations":[{"name":"execute","description":"Call tools.opencode.session_move with the exact tool name."}]}]}}`))
			path := "request." + field
			if !textOnly {
				path += ".parts.0.text"
			}
			const original = "Call tools.opencode.session_move and tools.opencode.models. Keep the exact tool IDs."
			if errSet := doc.Set(path, original); errSet != nil {
				t.Fatal(errSet)
			}
			contents, tools := doc.Get("request.contents").Raw, doc.Get("request.tools").Raw
			if !ObfuscateSensitiveWordsInSystemInstructionDoc(doc, matcher) {
				t.Fatal("expected the configured namespace replacement")
			}
			const want = `Call tools["opencode"].session_move and tools["opencode"].models. Keep the exact tool IDs.`
			if got := doc.Get(path).String(); got != want {
				t.Fatalf("system instruction = %q, want %q", got, want)
			}
			if doc.Get("request.contents").Raw != contents || doc.Get("request.tools").Raw != tools {
				t.Fatal("namespace replacement changed user content or actual tool declarations")
			}
			if doc.Get("model").String() != "gemini-4-neon-high" {
				t.Fatal("namespace replacement changed the exact model ID")
			}
			if ObfuscateSensitiveWordsInSystemInstructionDoc(doc, matcher) {
				t.Fatal("namespace replacement is not idempotent")
			}
		}
	}
}
