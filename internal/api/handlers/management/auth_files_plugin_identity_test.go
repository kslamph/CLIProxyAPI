package management

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// fakePluginAuthParser mimics a plugin that owns provider "opencode-go" and
// assigns its own stable, content-derived auth ID instead of the file name.
type fakePluginAuthParser struct {
	provider string
	id       string
	calls    int
}

func (p *fakePluginAuthParser) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	auths, handled, err := p.ParseAuths(ctx, req)
	if err != nil || !handled || len(auths) == 0 {
		return nil, handled, err
	}
	return auths[0], true, nil
}

func (p *fakePluginAuthParser) ParseAuths(_ context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	p.calls++
	if req.Provider != p.provider {
		return nil, false, nil
	}
	return []*coreauth.Auth{{
		ID:         p.id,
		Provider:   p.provider,
		FileName:   req.FileName,
		Attributes: map[string]string{"api_key": "plugin-owned"},
	}}, true, nil
}

var _ synthesizer.PluginMultiAuthParser = (*fakePluginAuthParser)(nil)

func newIdentityTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	authDir := t.TempDir()
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, coreauth.NewManager(nil, nil, nil)), authDir
}

// A file owned by a plugin must keep the plugin-assigned ID when it is
// (re)saved through the management API. Otherwise the upload registers
// "<file>.json" while the file watcher registers the plugin ID, and one
// credential shows up twice.
func TestBuildAuthFromFileData_KeepsPluginAssignedID(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	parser := &fakePluginAuthParser{provider: "opencode-go", id: "opencode-go-key-abc123"}
	path := filepath.Join(authDir, "Primary-2.json")
	data := []byte(`{"type":"opencode-go","id":"opencode-go-key-abc123","label":"Primary","api_key":"k","excluded_models":["m1"]}`)

	auth, err := h.buildAuthFromFileDataWithParser(path, data, parser)
	if err != nil {
		t.Fatalf("buildAuthFromFileDataWithParser: %v", err)
	}
	if auth.ID != "opencode-go-key-abc123" {
		t.Fatalf("auth ID = %q, want plugin-assigned %q", auth.ID, "opencode-go-key-abc123")
	}
	if auth.FileName != "Primary-2.json" {
		t.Fatalf("auth FileName = %q, want %q", auth.FileName, "Primary-2.json")
	}
	if auth.Provider != "opencode-go" {
		t.Fatalf("auth Provider = %q, want opencode-go", auth.Provider)
	}
}

// Re-saving a plugin-owned file must update the already registered auth
// (same ID) instead of registering a second one.
func TestWriteAuthFile_PluginOwnedFileDoesNotDuplicate(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	parser := &fakePluginAuthParser{provider: "opencode-go", id: "opencode-go-key-abc123"}
	path := filepath.Join(authDir, "Primary-2.json")
	ctx := context.Background()

	// The watcher/plugin registered the credential under the plugin ID first.
	pre, err := h.buildAuthFromFileDataWithParser(path, []byte(`{"type":"opencode-go","api_key":"k"}`), parser)
	if err != nil {
		t.Fatalf("build initial auth: %v", err)
	}
	if err := h.upsertAuthRecord(ctx, pre); err != nil {
		t.Fatalf("register initial auth: %v", err)
	}

	// The operator edits excluded models in the UI: same file name, new body.
	edited := []byte(`{"type":"opencode-go","api_key":"k","excluded_models":["m1"]}`)
	auth, err := h.buildAuthFromFileDataWithParser(path, edited, parser)
	if err != nil {
		t.Fatalf("build edited auth: %v", err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := h.upsertAuthRecord(ctx, auth); err != nil {
		t.Fatalf("upsert edited auth: %v", err)
	}

	var forFile []*coreauth.Auth
	for _, a := range h.authManager.List() {
		if a.FileName == "Primary-2.json" {
			forFile = append(forFile, a)
		}
	}
	if len(forFile) != 1 {
		ids := make([]string, 0, len(forFile))
		for _, a := range forFile {
			ids = append(ids, a.ID)
		}
		t.Fatalf("auths for Primary-2.json = %d (%v), want exactly 1", len(forFile), ids)
	}
	if forFile[0].ID != "opencode-go-key-abc123" {
		t.Fatalf("auth ID = %q, want plugin-assigned ID", forFile[0].ID)
	}
}

// Files no plugin claims keep the historical path-derived ID.
func TestBuildAuthFromFileData_UnclaimedFileKeepsPathID(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	parser := &fakePluginAuthParser{provider: "opencode-go", id: "opencode-go-key-abc123"}
	path := filepath.Join(authDir, "codex-user@example.com-plus.json")
	data := []byte(`{"type":"codex","email":"user@example.com","access_token":"t"}`)

	auth, err := h.buildAuthFromFileDataWithParser(path, data, parser)
	if err != nil {
		t.Fatalf("buildAuthFromFileDataWithParser: %v", err)
	}
	if auth.ID != "codex-user@example.com-plus.json" {
		t.Fatalf("auth ID = %q, want path-derived ID", auth.ID)
	}
	if parser.calls == 0 {
		t.Fatalf("plugin parser was never consulted")
	}
}
