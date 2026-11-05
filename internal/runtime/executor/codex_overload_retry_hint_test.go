package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Real production overload rejection (journal, LXC 115, 2026-11-05): a bare
// type:error event inside an HTTP 200 SSE stream right after the handshake
// events, carrying the upstream retry hint inside error.headers.
const codexOverloadJournalEvent = `{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"x-retry-metadata":"NO_MORE_RETRY","retry-after":"316","retry-after-ms":"316000"},"message":"Our servers are currently overloaded. Please try again later.","param":null},"sequence_number":3}`

func codexOverloadConfig(seconds *int) *config.Config {
	return &config.Config{Codex: config.CodexConfig{OverloadRetryAfterSeconds: seconds}}
}

func overloadPolicyOn(seconds int) codexOverloadPolicy {
	return codexOverloadPolicy{retryAfterSeconds: seconds}
}

// The delegated shaping: 429, the configured short hint, rewritten body
// headers, the delegation marker, and nothing else changed.
func TestCodexOverloadDelegationShapesStatusErr(t *testing.T) {
	event := []byte(codexOverloadJournalEvent)
	streamErr, body, ok := codexTerminalFailureErrForPolicy(overloadPolicyOn(5), event, false)
	if !ok {
		t.Fatal("expected the overload event to be classified as a terminal failure")
	}
	if streamErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", streamErr.StatusCode())
	}
	if !streamErr.IsCodexOverloadDelegated() {
		t.Fatal("expected the overload delegation marker")
	}
	if streamErr.IsCredentialScoped() || streamErr.IsTransientRateLimit() {
		t.Fatal("overload delegation must not be credential-scoped or a transient rate limit")
	}
	retryAfter := streamErr.RetryAfter()
	if retryAfter == nil || *retryAfter != 5*time.Second {
		t.Fatalf("retryAfter = %v, want 5s", retryAfter)
	}
	doc := ojson.NewDoc([]byte(streamErr.Error()))
	if got := doc.Get("error.headers.retry-after").String(); got != "5" {
		t.Fatalf("error.headers.retry-after = %q, want \"5\"", got)
	}
	if got := doc.Get("error.headers.retry-after-ms").String(); got != "5000" {
		t.Fatalf("error.headers.retry-after-ms = %q, want \"5000\"", got)
	}
	if doc.Get("error.headers.x-retry-metadata").Exists() {
		t.Fatalf("x-retry-metadata must be removed, body: %s", streamErr.Error())
	}
	if got := doc.Get("error.code").String(); got != "server_is_overloaded" {
		t.Fatalf("error.code = %q, want server_is_overloaded", got)
	}
	if got := doc.Get("error.type").String(); got != "service_unavailable_error" {
		t.Fatalf("error.type = %q, want service_unavailable_error", got)
	}
	if got := doc.Get("error.message").String(); got != "Our servers are currently overloaded. Please try again later." {
		t.Fatalf("error.message changed: %q", got)
	}
	if got := doc.Get("error.param").Type; got != ojson.Null {
		t.Fatalf("error.param = %v, want null", got)
	}
	if got := doc.Get("sequence_number").Int(); got != 3 {
		t.Fatalf("sequence_number = %d, want 3", got)
	}
	if strings.Contains(streamErr.Error(), "NO_MORE_RETRY") || strings.Contains(streamErr.Error(), "316") {
		t.Fatalf("body still carries the upstream hint: %s", streamErr.Error())
	}
	_ = body
}

// Kill switch: with a zero policy the journal body passes through byte
// unchanged and the classification is exactly today's (502, no retry hint).
func TestCodexOverloadKillSwitchKeepsTodaysStatusErr(t *testing.T) {
	event := []byte(codexOverloadJournalEvent)
	today, _, ok := codexTerminalFailureErrForPolicy(codexOverloadPolicy{}, event, false)
	if !ok {
		t.Fatal("expected the overload event to be classified as a terminal failure")
	}
	if today.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (today's mapping)", today.StatusCode())
	}
	if today.RetryAfter() != nil {
		t.Fatalf("retryAfter = %v, want nil", today.RetryAfter())
	}
	if today.IsCodexOverloadDelegated() {
		t.Fatal("kill switch must not set the delegation marker")
	}
	want := `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"x-retry-metadata":"NO_MORE_RETRY","retry-after":"316","retry-after-ms":"316000"},"message":"Our servers are currently overloaded. Please try again later.","param":null},"sequence_number":3}`
	if today.Error() != want {
		t.Fatalf("kill-switch body changed:\n got %s\nwant %s", today.Error(), want)
	}
}

// Rate-limit and quota errors must be untouched by the delegation policy.
func TestCodexOverloadDelegationLeavesOtherClassesAlone(t *testing.T) {
	rateLimit := `{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","headers":{"x-retry-metadata":"NO_MORE_RETRY","retry-after":"42","retry-after-ms":"42000"},"message":"You exceeded your rate limit."},"sequence_number":7}`
	usageLimit := `{"type":"error","error":{"type":"usage_limit_reached","code":null,"message":"You've hit your usage limit","resets_at":4102444800},"sequence_number":8}`
	for name, body := range map[string]string{"rate_limit": rateLimit, "usage_limit": usageLimit} {
		t.Run(name, func(t *testing.T) {
			policyErr, _, okPolicy := codexTerminalFailureErrForPolicy(overloadPolicyOn(5), []byte(body), false)
			todayErr, _, okToday := codexTerminalFailureErrForPolicy(codexOverloadPolicy{}, []byte(body), false)
			if !okPolicy || !okToday {
				t.Fatal("expected terminal failure classification")
			}
			if policyErr.StatusCode() != todayErr.StatusCode() {
				t.Fatalf("status changed: %d vs today %d", policyErr.StatusCode(), todayErr.StatusCode())
			}
			if policyErr.Error() != todayErr.Error() {
				t.Fatalf("body changed:\n got %s\nwant %s", policyErr.Error(), todayErr.Error())
			}
			if policyErr.IsCodexOverloadDelegated() {
				t.Fatal("non-overload class must never carry the delegation marker")
			}
			if (policyErr.RetryAfter() == nil) != (todayErr.RetryAfter() == nil) {
				t.Fatalf("retryAfter presence changed: %v vs today %v", policyErr.RetryAfter(), todayErr.RetryAfter())
			}
		})
	}
	if isCodexServerOverloadError([]byte(rateLimit)) {
		t.Fatal("rate_limit_exceeded must not be classified as overload")
	}
	if isCodexServerOverloadError([]byte(usageLimit)) {
		t.Fatal("usage_limit_reached must not be classified as overload")
	}
	if !isCodexServerOverloadError([]byte(codexOverloadJournalEvent)) {
		t.Fatal("journal event must be classified as overload")
	}
}

// HTTP-level answers (status on the wire) get the same shaping, and the
// upstream Retry-After header never overrides the configured hint.
func TestCodexOverloadDelegationHTTPStatusBody(t *testing.T) {
	body := []byte(`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"retry-after":"316","retry-after-ms":"316000"},"message":"Our servers are currently overloaded. Please try again later.","param":null}}`)
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway} {
		err := newCodexStatusErrForPolicy(overloadPolicyOn(5), status, http.Header{"Retry-After": []string{"316"}}, body, false)
		if err.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("status %d: got %d, want 429", status, err.StatusCode())
		}
		if retryAfter := err.RetryAfter(); retryAfter == nil || *retryAfter != 5*time.Second {
			t.Fatalf("status %d: retryAfter = %v, want 5s", status, retryAfter)
		}
		if !err.IsCodexOverloadDelegated() {
			t.Fatalf("status %d: missing delegation marker", status)
		}
		if strings.Contains(err.Error(), "316") {
			t.Fatalf("status %d: upstream hint leaked: %s", status, err.Error())
		}
	}
	// Kill switch keeps today's mapping and header precedence untouched.
	kill := newCodexStatusErrForPolicy(codexOverloadPolicy{}, http.StatusServiceUnavailable, http.Header{"Retry-After": []string{"316"}}, body, false)
	if kill.StatusCode() != http.StatusServiceUnavailable || kill.RetryAfter() != nil || !strings.Contains(kill.Error(), "316") {
		t.Fatalf("kill switch changed the HTTP-level behaviour: %d %v %s", kill.StatusCode(), kill.RetryAfter(), kill.Error())
	}
}

// The websocket parser shapes the same class identically.
func TestCodexOverloadDelegationWebsocketParse(t *testing.T) {
	payload := []byte(`{"type":"error","status":503,"error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"x-retry-metadata":"NO_MORE_RETRY","retry-after":"316","retry-after-ms":"316000"},"message":"Our servers are currently overloaded. Please try again later.","param":null}}`)
	wsErr, ok := parseCodexWebsocketErrorForPolicy(overloadPolicyOn(5), payload, false)
	if !ok {
		t.Fatal("expected websocket error classification")
	}
	statusErrValue, isStatus := wsErr.(statusErrWithHeaders)
	if !isStatus {
		t.Fatalf("expected statusErrWithHeaders, got %T", wsErr)
	}
	if statusErrValue.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", statusErrValue.StatusCode())
	}
	if retryAfter := statusErrValue.RetryAfter(); retryAfter == nil || *retryAfter != 5*time.Second {
		t.Fatalf("retryAfter = %v, want 5s", retryAfter)
	}
	if !statusErrValue.IsCodexOverloadDelegated() {
		t.Fatal("missing delegation marker")
	}
	if strings.Contains(statusErrValue.Error(), "316") || strings.Contains(statusErrValue.Error(), "NO_MORE_RETRY") {
		t.Fatalf("upstream hint leaked: %s", statusErrValue.Error())
	}
}

// The rewrite covers both wire layouts: a bare error event and a
// response.failed event, and creates the headers object when absent.
func TestRewriteCodexOverloadRetryHintLayouts(t *testing.T) {
	failed := `{"type":"response.failed","sequence_number":9,"response":{"status":"failed","error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"x-retry-metadata":"NO_MORE_RETRY","retry-after":"316","retry-after-ms":"316000"},"message":"Our servers are currently overloaded. Please try again later."}}}`
	for name, body := range map[string]string{"error": codexOverloadJournalEvent, "response.failed": failed} {
		t.Run(name, func(t *testing.T) {
			rewritten := rewriteCodexOverloadRetryHint([]byte(body), 5)
			doc := ojson.NewDoc(rewritten)
			prefix := "error.headers."
			if name == "response.failed" {
				prefix = "response.error.headers."
			}
			if got := doc.Get(prefix + "retry-after").String(); got != "5" {
				t.Fatalf("retry-after = %q, want \"5\" (%s)", got, rewritten)
			}
			if got := doc.Get(prefix + "retry-after-ms").String(); got != "5000" {
				t.Fatalf("retry-after-ms = %q, want \"5000\"", got)
			}
			if ojson.GetBytesNoCopy(rewritten, strings.TrimSuffix(prefix, ".")+".x-retry-metadata").Exists() {
				t.Fatalf("x-retry-metadata survived: %s", rewritten)
			}
		})
	}
	noHeaders := rewriteCodexOverloadRetryHint([]byte(`{"error":{"code":"server_is_overloaded","message":"overloaded"}}`), 5)
	if got := ojson.GetBytesNoCopy(noHeaders, "error.headers.retry-after").String(); got != "5" {
		t.Fatalf("missing headers object not created: %s", noHeaders)
	}
}

// With delegation enabled, bootstrap buffering must NOT fail a pure overload
// over to another credential: the buffered handshake is flushed and the
// rewritten error is delivered in-stream. The kill switch keeps today's
// attempt-failing failover.
func TestCodexOverloadBootstrapBufferingDeliversInStreamWhenDelegated(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexOverloadJournalEvent)
	defer server.Close()

	seconds := 5
	delegating := codexOverloadConfig(&seconds)
	delegating.Codex.StreamBootstrapBuffering = true
	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(delegating).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("delegated overload must not fail the attempt: %v", err)
	}
	payload, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected the in-stream error chunk")
	}
	if statusErrValue, ok := streamErr.(statusErr); !ok || !statusErrValue.IsCodexOverloadDelegated() {
		t.Fatalf("in-stream error must carry the delegation marker, got %#v", streamErr)
	}
	if !strings.Contains(payload, "response.created") {
		t.Fatalf("buffered handshake must be flushed in-stream, got %q", payload)
	}

	// Kill switch (explicit 0): today's failover behaviour returns.
	zero := 0
	kill := codexOverloadConfig(&zero)
	kill.Codex.StreamBootstrapBuffering = true
	resultKill, errKill := NewCodexExecutor(kill).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if errKill == nil {
		t.Fatal("kill switch: expected the attempt to fail for the failover")
	}
	if resultKill != nil {
		t.Fatal("kill switch: expected nil result so no buffered handshake leaks")
	}
	if got := statusCodeFromTestError(t, errKill); got != http.StatusServiceUnavailable {
		t.Fatalf("kill switch status = %d, want 503", got)
	}
	if !strings.Contains(errKill.Error(), "316") {
		t.Fatalf("kill switch must pass the upstream hint through, got %s", errKill.Error())
	}
}

// Bootstrap classification: pure overload stops failing over under the
// policy, every other overload-shaped class keeps failing over, and the kill
// switch restores today's classification for all of them.
func TestIsCodexOverloadBootstrapFailurePolicy(t *testing.T) {
	cases := []struct {
		body       string
		wantPolicy bool // classification with delegation on
		wantToday  bool // classification with the kill switch
	}{
		{codexOverloadJournalEvent, false, true},
		{`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded"}}`, false, true},
		{`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`, true, true},
		{`{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity"}}`, true, true},
		{`{"error":{"message":"Selected model is at capacity. Please try a different model."}}`, true, true},
		{`{"error":{"type":"server_error","code":"server_error","message":"The server had an error while processing your request. You can retry your request."}}`, true, true},
		{`{"error":{"type":"invalid_request_error","code":"invalid_value"}}`, false, false},
	}
	for _, tc := range cases {
		if got := isCodexOverloadBootstrapFailureForPolicy(overloadPolicyOn(5), []byte(tc.body)); got != tc.wantPolicy {
			t.Fatalf("policy classification for %s = %v, want %v", tc.body, got, tc.wantPolicy)
		}
		if got := isCodexOverloadBootstrapFailure([]byte(tc.body)); got != tc.wantToday {
			t.Fatalf("today classification for %s = %v, want %v", tc.body, got, tc.wantToday)
		}
	}
}

// Unbuffered in-stream delivery (the production path): the real journal
// sequence must end in a single delegated error chunk whose message carries
// the rewritten hint; no raw upstream error frame may escape as a payload.
func TestCodexOverloadUnbufferedStreamDelegatesToClient(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexOverloadJournalEvent)
	defer server.Close()

	seconds := 5
	req, opts := codexTestRequest()
	opts.SourceFormat = sdktranslator.FromString("openai-response")
	result, err := NewCodexExecutor(codexOverloadConfig(&seconds)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream returned error: %v", err)
	}
	payload, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatalf("expected in-stream error chunk, payload: %q", payload)
	}
	statusErrValue, ok := streamErr.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", streamErr)
	}
	if statusErrValue.StatusCode() != http.StatusTooManyRequests || !statusErrValue.IsCodexOverloadDelegated() {
		t.Fatalf("status = %d delegated = %v", statusErrValue.StatusCode(), statusErrValue.IsCodexOverloadDelegated())
	}
	if strings.Contains(payload, "server_is_overloaded") || strings.Contains(payload, "316") {
		t.Fatalf("raw upstream error frame leaked as payload: %q", payload)
	}
	if !strings.Contains(statusErrValue.Error(), `"retry-after":"5"`) {
		t.Fatalf("rewritten hint missing from the client-visible error: %s", statusErrValue.Error())
	}
	// The delegation marker survives the executor contract interface check.
	var delegated cliproxyexecutor.CodexOverloadDelegatedError
	if !errors.As(streamErr, &delegated) || !delegated.IsCodexOverloadDelegated() {
		t.Fatal("statusErr does not satisfy cliproxyexecutor.CodexOverloadDelegatedError")
	}
}
