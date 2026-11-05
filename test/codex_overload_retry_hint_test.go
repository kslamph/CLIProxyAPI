package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Recorded Codex upstream overload rejection (journal bytes, LXC 115,
// 2026-11-05): delivered inside an HTTP 200 SSE stream right after the
// handshake events. The official Codex client retries this class only when
// the error object's headers carry a retry-after, then sleeps exactly that
// long; it never reads x-retry-metadata.
const codexOverloadJournalEvent = `{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"x-retry-metadata":"NO_MORE_RETRY","retry-after":"316","retry-after-ms":"316000"},"message":"Our servers are currently overloaded. Please try again later.","param":null},"sequence_number":3}`

const codexOverloadJournalCreated = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_overload"}}`
const codexOverloadJournalInProgress = `{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_overload"}}`

func intPtrForOverload(v int) *int { return &v }

func overloadUpstream(t *testing.T, events []string, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, event := range events {
			if _, errWrite := fmt.Fprintf(w, "data: %s\n\n", event); errWrite != nil {
				t.Errorf("write SSE: %v", errWrite)
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
}

func overloadManager(t *testing.T, cfg *config.Config, serverURL string, authIDs ...string) *cliproxyauth.Manager {
	t.Helper()
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(2, time.Minute, 2)
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
	for i, authID := range authIDs {
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
			ID:         authID,
			Provider:   "codex",
			Status:     cliproxyauth.StatusActive,
			Attributes: map[string]string{"base_url": serverURL, "api_key": fmt.Sprintf("key-%d", i)},
			Metadata:   map[string]any{"type": "codex"},
		}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	return manager
}

func overloadResponsesRequest(t *testing.T, recorder *httptest.ResponseRecorder, manager *cliproxyauth.Manager, userAgent string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"gpt-5.4","input":"hello","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		c.Request.Header.Set("User-Agent", userAgent)
	}
	cfg := &config.Config{}
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	openaihandlers.NewOpenAIResponsesAPIHandler(base).Responses(c)
}

// The committed-stream path: with the default policy the client-visible
// response.failed frame (the one Codex actually reads) carries retry-after
// "5" / retry-after-ms "5000" and no x-retry-metadata; everything else in the
// frame is unchanged; the credential is not cooled; no failover happened
// within the request; and the next request still reaches the upstream.
func TestCodexOverloadInStreamRewritesHintWithoutCooldownOrFailover(t *testing.T) {
	var hits int32
	events := []string{codexOverloadJournalCreated, codexOverloadJournalInProgress, codexOverloadJournalEvent}
	server := overloadUpstream(t, events, &hits)
	defer server.Close()

	cfg := &config.Config{}
	manager := overloadManager(t, cfg, server.URL, "codex-overload-1", "codex-overload-2")

	recorder := httptest.NewRecorder()
	overloadResponsesRequest(t, recorder, manager, "codex_cli_rs/0.166.0 (Ubuntu 22.04.0; x86_64) Linux")

	body := recorder.Body.String()
	if strings.Count(body, "server_is_overloaded") != 1 {
		t.Fatalf("the error must surface exactly once, body:\n%s", body)
	}
	if strings.Contains(body, "316") || strings.Contains(body, "NO_MORE_RETRY") {
		t.Fatalf("upstream hint leaked to the client, body:\n%s", body)
	}
	detail := terminalStreamErrorDetail(t, body)
	if got := detail["code"]; got != "server_is_overloaded" {
		t.Fatalf("error.code = %v, want server_is_overloaded (other clients key on it)", got)
	}
	if got := detail["type"]; got != "service_unavailable_error" {
		t.Fatalf("error.type = %v, want service_unavailable_error", got)
	}
	if got := detail["message"]; got != "Our servers are currently overloaded. Please try again later." {
		t.Fatalf("error.message changed: %v", got)
	}
	headers, ok := detail["headers"].(map[string]any)
	if !ok {
		t.Fatalf("error.headers missing from the terminal frame: %#v", detail)
	}
	if got := headers["retry-after"]; got != "5" {
		t.Fatalf("headers.retry-after = %v, want \"5\"", got)
	}
	if got := headers["retry-after-ms"]; got != "5000" {
		t.Fatalf("headers.retry-after-ms = %v, want \"5000\"", got)
	}
	if _, exists := headers["x-retry-metadata"]; exists {
		t.Fatalf("x-retry-metadata must be removed: %#v", headers)
	}
	// The frame Codex reads is response.failed for Codex clients.
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("expected a response.failed terminal frame, body:\n%s", body)
	}

	// No failover within the request, and the credential stays selectable.
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (no failover, no handler/conductor replay)", got)
	}
	for _, authID := range []string{"codex-overload-1", "codex-overload-2"} {
		authState, okState := manager.GetByID(authID)
		if !okState {
			continue
		}
		if authState.Unavailable || !authState.NextRetryAfter.IsZero() {
			t.Fatalf("%s cooled after a delegated overload: unavailable=%v next=%v", authID, authState.Unavailable, authState.NextRetryAfter)
		}
	}

	// The next client request must reach the upstream again immediately.
	recorder2 := httptest.NewRecorder()
	overloadResponsesRequest(t, recorder2, manager, "codex_cli_rs/0.166.0 (Ubuntu 22.04.0; x86_64) Linux")
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("upstream hits after retry = %d, want 2 (credential must stay immediately selectable)", got)
	}
}

// The uncommitted path: when the overload is the first event (nothing written
// to the client yet) the request ends as HTTP 429 with Retry-After: 5 and the
// rewritten body, even when the only other credential carries a pre-existing
// cooldown (the old instant-503-with-no-upstream-attempt state).
func TestCodexOverloadUncommittedReturns429WithRetryAfter5(t *testing.T) {
	var hits int32
	server := overloadUpstream(t, []string{codexOverloadJournalEvent}, &hits)
	defer server.Close()

	cfg := &config.Config{}
	manager := overloadManager(t, cfg, server.URL, "codex-overload-first", "codex-overload-cooled")
	// Pre-existing quota cooldown on the other paid credential.
	quotaHint := time.Hour
	manager.MarkResult(context.Background(), cliproxyauth.Result{
		AuthID:     "codex-overload-cooled",
		Provider:   "codex",
		Model:      "gpt-5.4",
		Success:    false,
		RetryAfter: &quotaHint,
		Error: &cliproxyauth.Error{
			Code:       "usage_limit_reached",
			Message:    "usage limit",
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	recorder := httptest.NewRecorder()
	overloadResponsesRequest(t, recorder, manager, "codex_cli_rs/0.166.0 (Ubuntu 22.04.0; x86_64) Linux")

	if got := recorder.Code; got != http.StatusTooManyRequests {
		t.Fatalf("HTTP status = %d, want 429 (headers were uncommitted)", got)
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After header = %q, want \"5\"", got)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (no failover despite the sibling cooldown)", got)
	}
	var payload struct {
		Error struct {
			Code    string         `json:"code"`
			Type    string         `json:"type"`
			Headers map[string]any `json:"headers"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal error body %q: %v", recorder.Body.String(), errUnmarshal)
	}
	if payload.Error.Code != "server_is_overloaded" || payload.Error.Type != "service_unavailable_error" {
		t.Fatalf("body semantics changed: %#v", payload)
	}
	if got := payload.Error.Headers["retry-after"]; got != "5" {
		t.Fatalf("body headers.retry-after = %v, want \"5\"", got)
	}
	if got := payload.Error.Headers["retry-after-ms"]; got != "5000" {
		t.Fatalf("body headers.retry-after-ms = %v, want \"5000\"", got)
	}
	if _, exists := payload.Error.Headers["x-retry-metadata"]; exists {
		t.Fatalf("x-retry-metadata must be removed: %#v", payload.Error.Headers)
	}
	// The sibling keeps its pre-existing cooldown; the reporting credential does not cool.
	cooled, _ := manager.GetByID("codex-overload-cooled")
	if !cooled.Unavailable {
		t.Fatal("pre-existing sibling cooldown must be untouched")
	}
}

// Kill switch (explicit 0): the client-visible frames carry the upstream hint
// (316 / NO_MORE_RETRY) untouched and the failing credential cools down, i.e.
// exactly today's behaviour.
func TestCodexOverloadKillSwitchRestoresTodaysClientBehaviour(t *testing.T) {
	var hits int32
	events := []string{codexOverloadJournalCreated, codexOverloadJournalInProgress, codexOverloadJournalEvent}
	server := overloadUpstream(t, events, &hits)
	defer server.Close()

	cfg := &config.Config{Codex: config.CodexConfig{OverloadRetryAfterSeconds: intPtrForOverload(0)}}
	manager := overloadManager(t, cfg, server.URL, "codex-overload-kill1", "codex-overload-kill2")

	recorder := httptest.NewRecorder()
	overloadResponsesRequest(t, recorder, manager, "codex_cli_rs/0.166.0 (Ubuntu 22.04.0; x86_64) Linux")

	body := recorder.Body.String()
	if !strings.Contains(body, `"retry-after":"316"`) || !strings.Contains(body, "NO_MORE_RETRY") {
		t.Fatalf("kill switch must pass the upstream hint through, body:\n%s", body)
	}
	detail := terminalStreamErrorDetail(t, body)
	if got := detail["code"]; got != "server_is_overloaded" {
		t.Fatalf("kill switch must keep error.code, got %v", got)
	}
	// Today's behaviour: the in-stream 502 cools the reporting credential for
	// the transient cooldown (plus failover made the second credential answer,
	// both point at the same test upstream).
	if got := atomic.LoadInt32(&hits); got < 1 {
		t.Fatalf("upstream hits = %d, want >= 1", got)
	}
	cooled := false
	for _, authID := range []string{"codex-overload-kill1", "codex-overload-kill2"} {
		authState, _ := manager.GetByID(authID)
		if authState != nil && (authState.Unavailable || !authState.NextRetryAfter.IsZero()) {
			cooled = true
		}
	}
	if !cooled {
		t.Fatal("kill switch: expected the failing credential to cool (today's behaviour)")
	}
}
