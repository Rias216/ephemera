package llm

import "strings"

// PromptProfile describes provider-facing prompt preferences without leaking
// provider-specific branches into the agent's planning or execution logic.
type PromptProfile struct {
	Name                     string
	SystemGuidance           string
	NativeToolGuidance       string
	StructuredOutputGuidance string
	ReasoningGuidance        string
	Compact                  bool
}

// ProfileFor derives a conservative prompt profile from negotiated provider
// capabilities. Unknown providers receive the portable default profile.
func ProfileFor(provider Provider) PromptProfile {
	profile := PromptProfile{
		Name:                     "portable",
		SystemGuidance:           "Use explicit sections, direct instructions, and evidence-backed conclusions.",
		NativeToolGuidance:       "Prefer native tool calls over describing commands in prose.",
		StructuredOutputGuidance: "Return exactly one valid JSON object when requesting tools; do not wrap it in Markdown.",
		ReasoningGuidance:        "Expose only a concise decision summary with assumptions, evidence, risks, and verification.",
	}
	if provider == nil {
		return profile
	}
	caps := Capabilities(provider)
	name := strings.ToLower(strings.TrimSpace(provider.Name()))
	switch {
	case caps.ToolCallFormat == "anthropic" || name == "anthropic":
		profile.Name = "anthropic"
		profile.SystemGuidance = "Treat XML-like section boundaries as authoritative; follow direct positive instructions and keep each section internally consistent."
		profile.NativeToolGuidance = "Use tool calls as soon as the required arguments are known, then ground the next decision in the returned tool_result blocks."
		profile.StructuredOutputGuidance = "When native tools are unavailable, place one JSON decision object inside the requested response contract with no surrounding commentary."
		profile.ReasoningGuidance = "Provide a brief visible rationale summary; never reveal private scratch work or extended hidden reasoning."
	case caps.ToolCallFormat == "ollama" || name == "ollama":
		profile.Name = "ollama"
		profile.Compact = true
		profile.SystemGuidance = "Use short sentences, a small number of rules, and one concrete next action at a time."
		profile.NativeToolGuidance = "Prefer one minimal tool batch; avoid speculative or duplicate calls."
		profile.StructuredOutputGuidance = "Emit compact valid JSON only. Omit optional prose and keep arrays short."
		profile.ReasoningGuidance = "Keep the visible decision summary brief and concrete."
	case caps.ToolCallFormat == "openai" || name == "openai" || name == "codex" || name == "chatgpt":
		profile.Name = "openai"
		profile.SystemGuidance = "Follow the structured response contract exactly and keep tool arguments schema-valid."
		profile.NativeToolGuidance = "Use native function calls, preserve tool_call_id continuity, and batch independent calls when safe."
		profile.StructuredOutputGuidance = "Return one schema-conforming JSON object with no code fence when native tools are unavailable."
		profile.ReasoningGuidance = "Use the requested reasoning effort and surface only the concise reasoning summary."
	}
	return profile
}
