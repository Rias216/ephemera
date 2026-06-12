package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ephemera-ai/ephemera/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/debuglog"
)

var fallbackCodexModels = []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}

const defaultCodexBridgeMaxTokens int64 = 2_048

// Codex uses the local Codex ChatGPT login instead of an OpenAI API key.
// It is intentionally run as an isolated model bridge: Ephemera owns tools,
// filesystem access, approvals, and workspace mutations.
type Codex struct {
	bridgeMaxTokens int64
}

func init() {
	RegisterProvider("codex", func(cfg config.Config) (Provider, error) { return NewCodex(cfg.CodexBridgeMaxTokens), nil })
}

func NewCodex(bridgeMaxTokens int64) *Codex {
	if bridgeMaxTokens < 512 {
		bridgeMaxTokens = defaultCodexBridgeMaxTokens
	}
	if bridgeMaxTokens > 8_000 {
		bridgeMaxTokens = 8_000
	}
	return &Codex{bridgeMaxTokens: bridgeMaxTokens}
}

func (p *Codex) Name() string { return "codex" }

func (p *Codex) ListModels(context.Context) ([]string, error) {
	return ListCodexModels()
}

func (p *Codex) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		Streaming:         true,
		SupportsReasoning: true,
		MaxParallelTools:  1,
		ToolCallFormat:    "text",
		StreamingFormat:   "process",
	}
}

func (p *Codex) Generate(ctx context.Context, req Request) (string, error) {
	if _, err := loadCodexAccessToken(); err != nil {
		return "", err
	}
	exe, err := codexExecutable()
	if err != nil {
		return "", err
	}

	output, err := os.CreateTemp("", "ephemera-codex-*.txt")
	if err != nil {
		return "", err
	}
	outputPath := output.Name()
	_ = output.Close()
	defer os.Remove(outputPath)

	text, commandOutput, err := p.runCodexCommand(ctx, exe, req, outputPath, false, true)
	if err != nil && codexBridgeCompatibilityFailure(commandOutput) && ctx.Err() == nil {
		debuglog.WarningCtx(ctx, "provider", "codex bridge compatibility fallback", trimCommandOutput(commandOutput), providerLogFields(p, req, nil))
		_ = os.WriteFile(outputPath, nil, 0o600)
		text, commandOutput, err = p.runCodexCommand(ctx, exe, req, outputPath, false, false)
	}
	if err != nil {
		return "", fmt.Errorf("codex exec failed: %w\n\n%s", err, trimCommandOutput(commandOutput))
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("codex returned an empty response")
	}
	return strings.TrimSpace(text), nil
}

func (p *Codex) runCodexCommand(ctx context.Context, exe string, req Request, outputPath string, stream, optimized bool) (string, []byte, error) {
	bridgeDir, err := codexBridgeDirectory()
	if err != nil {
		return "", nil, err
	}
	cmd := exec.CommandContext(ctx, exe, p.execArgs(req, outputPath, stream, optimized)...)
	cmd.Dir = bridgeDir
	cmd.Stdin = strings.NewReader(p.prompt(req))
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", data, err
	}
	result, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		return "", data, fmt.Errorf("codex did not write a final response: %w", readErr)
	}
	return strings.TrimSpace(string(result)), data, nil
}

func (p *Codex) execArgs(req Request, outputPath string, stream, optimized bool) []string {
	args := []string{"exec"}
	if stream {
		args = append(args, "--json")
	}
	args = append(args,
		"--model", req.Model,
		"--sandbox", "workspace-write",
		"--ephemeral",
		"--skip-git-repo-check",
	)
	if optimized {
		args = append(args, "--ignore-rules", "--ignore-user-config")
	}
	args = append(args,
		"--color", "never",
		"--output-last-message", outputPath,
	)
	if optimized {
		for _, override := range p.bridgeOverrides(req) {
			args = append(args, "-c", override)
		}
	}
	return append(args, "-")
}

func (p *Codex) bridgeOverrides(req Request) []string {
	summary := "none"
	hideReasoning := "true"
	if req.ReasoningSummary {
		summary = "concise"
		hideReasoning = "false"
	}
	return []string{
		`approval_policy="never"`,
		`sandbox_mode="workspace-write"`,
		`web_search="disabled"`,
		`history.persistence="none"`,
		`project_doc_max_bytes=0`,
		`model_reasoning_effort="` + codexBridgeReasoningEffort(req.ReasoningEffort) + `"`,
		`model_reasoning_summary="` + summary + `"`,
		`hide_agent_reasoning=` + hideReasoning,
		`model_verbosity="low"`,
		`personality="none"`,
		`tool_output_token_limit=512`,
		`features.apps=false`,
		`features.hooks=false`,
		`features.memories=false`,
		`features.multi_agent=false`,
		`features.shell_snapshot=false`,
		`features.shell_tool=false`,
		`features.skill_mcp_dependency_install=false`,
		`tools.view_image=false`,
		`developer_instructions="Act only as Ephemera's isolated model backend. Never use Codex tools, inspect the local workspace, or report sandbox limitations. Return only the response requested by the supplied Ephemera instructions."`,
	}
}

func codexBridgeReasoningEffort(requested string) string {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "high", "xhigh":
		return "high"
	case "minimal":
		return "minimal"
	default:
		// Ephemera already performs the outer planning/tool loop. Low effort keeps
		// each stateless bridge turn fast without disabling model reasoning.
		return "low"
	}
}

func (p *Codex) prompt(req Request) string {
	req, _ = NormalizeRequestUTF8(req)
	budget := req.MaxTokens
	if budget <= 0 || budget > p.bridgeMaxTokens {
		budget = p.bridgeMaxTokens
	}
	var b strings.Builder
	b.WriteString("EPHEMERA MODEL BRIDGE\n")
	b.WriteString("The surrounding Ephemera process is the only agent. It owns filesystem reads/writes, shell commands, web access, MCP, approvals, retries, and persistence.\n")
	b.WriteString("Do not inspect the current directory, run commands, call Codex tools, modify files, or mention the Codex sandbox as a limitation. When action is needed, request it only through the Ephemera tool protocol contained in the system instructions.\n")
	fmt.Fprintf(&b, "Keep the visible response near or below %d tokens; prefer the smallest complete answer or tool request.\n\n", budget)
	if strings.TrimSpace(req.System) != "" {
		b.WriteString("EPHEMERA SYSTEM INSTRUCTIONS:\n")
		b.WriteString(req.System)
		b.WriteString("\n\n")
	}
	b.WriteString("EPHEMERA CONVERSATION:\n")
	for _, message := range req.Messages {
		b.WriteString(strings.ToUpper(message.Role))
		b.WriteString(":\n")
		b.WriteString(message.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("Return only the assistant's next response for Ephemera. Do not perform the work through Codex CLI tools.")
	return b.String()
}

func codexBridgeDirectory() (string, error) {
	path := filepath.Join(os.TempDir(), "ephemera-codex-bridge")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", fmt.Errorf("create isolated Codex bridge directory: %w", err)
	}
	return path, nil
}

func codexBridgeCompatibilityFailure(data []byte) bool {
	text := strings.ToLower(string(data))
	for _, marker := range []string{
		"unexpected argument",
		"unknown argument",
		"unrecognized option",
		"unrecognized field",
		"unknown field",
		"unknown feature",
		"unknown config",
		"unknown variant",
		"invalid config",
		"failed to parse config",
		"could not parse config",
		"error loading config",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func ListCodexModels() ([]string, error) {
	if models, err := cachedCodexModels(); err == nil && len(models) > 0 {
		return models, nil
	}
	return append([]string(nil), fallbackCodexModels...), nil
}

func cachedCodexModels() ([]string, error) {
	data, err := os.ReadFile(filepath.Join(codexHome(), "models_cache.json"))
	if err != nil {
		return nil, err
	}
	var cache struct {
		Models []struct {
			Slug           string `json:"slug"`
			Visibility     string `json:"visibility"`
			SupportedInAPI bool   `json:"supported_in_api"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(cache.Models))
	for _, model := range cache.Models {
		if strings.TrimSpace(model.Slug) == "" || model.Visibility != "list" {
			continue
		}
		if !model.SupportedInAPI {
			continue
		}
		models = append(models, model.Slug)
	}
	models = cleanModelIDs(models)
	sort.SliceStable(models, func(i, j int) bool {
		return codexModelRank(models[i]) < codexModelRank(models[j])
	})
	return models, nil
}

func codexModelRank(model string) int {
	for i, preferred := range fallbackCodexModels {
		if model == preferred {
			return i
		}
	}
	return len(fallbackCodexModels) + 1
}

func codexHome() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func loadCodexAccessToken() (string, error) {
	path := strings.TrimSpace(os.Getenv("EPHEMERA_CODEX_AUTH_FILE"))
	if path == "" {
		path = filepath.Join(codexHome(), "auth.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Codex ChatGPT login not found; open Codex or run its login flow, then retry /connect codex")
	}
	var auth struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", fmt.Errorf("Codex auth file is unreadable; open Codex and sign in again")
	}
	if strings.ToLower(strings.TrimSpace(auth.AuthMode)) != "chatgpt" {
		return "", fmt.Errorf("Codex is not signed in with ChatGPT; open Codex and choose ChatGPT login")
	}
	token := strings.TrimSpace(auth.Tokens.AccessToken)
	if token == "" {
		return "", fmt.Errorf("Codex ChatGPT access token is missing; open Codex and sign in again")
	}
	return token, nil
}

func codexExecutable() (string, error) {
	for _, value := range []string{
		os.Getenv("EPHEMERA_CODEX_CLI"),
		os.Getenv("CODEX_CLI_PATH"),
		codexCLIPathFromConfig(),
		"codex",
	} {
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("Codex CLI was not found")
}

func codexCLIPathFromConfig() string {
	data, err := os.ReadFile(filepath.Join(codexHome(), "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CODEX_CLI_PATH") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

func trimCommandOutput(data []byte) string {
	text := strings.Join(strings.Fields(string(data)), " ")
	if len(text) > 600 {
		return text[:597] + "..."
	}
	return text
}
