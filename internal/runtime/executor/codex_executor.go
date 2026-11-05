package executor

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) modelLevelCooling() bool {
	return e != nil && e.cfg != nil && e.cfg.Codex.ModelLevelCooling
}

// errPolicy exposes the executor's Codex overload delegation policy to the
// error constructors so client-visible retry hints, cooldown and failover
// decisions all derive from one config key.
func (e *CodexExecutor) errPolicy() codexOverloadPolicy {
	if e == nil {
		return codexOverloadPolicy{}
	}
	return codexOverloadPolicyFromConfig(e.cfg)
}

// SupportsApplyPatch reports the actual executor contract, independent of its provider name.
func (e *CodexExecutor) SupportsApplyPatch() bool { return e != nil }
