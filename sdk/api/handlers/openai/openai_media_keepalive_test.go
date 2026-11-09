package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// keepAliveTicks installs a controlled media keep-alive ticker and reports how many tickers were created.
type keepAliveTicks struct {
	tick    chan time.Time
	created atomic.Int32
}

func installKeepAliveTicks(t *testing.T) *keepAliveTicks {
	t.Helper()
	k := &keepAliveTicks{tick: make(chan time.Time)}
	restore := apihandlers.SetNonStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
		k.created.Add(1)
		return k.tick, func() {}
	})
	t.Cleanup(restore)
	return k
}

// emit delivers n ticks. The tick channel is unbuffered, so the keep-alive goroutine has
// finished writing tick n-1 before tick n is accepted; the last write is awaited by stop().
func (k *keepAliveTicks) emit(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case k.tick <- time.Time{}:
		case <-time.After(5 * time.Second):
			t.Fatal("keep-alive goroutine did not accept tick")
		}
	}
}

// gatedExecutor runs before() inside Execute/ExecuteStream, then answers with payload or err.
type gatedExecutor struct {
	id      string
	before  func()
	payload []byte
	err     error
}

func (e *gatedExecutor) Identifier() string { return e.id }

func (e *gatedExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	if e.before != nil {
		e.before()
	}
	if e.err != nil {
		return coreexecutor.Response{}, e.err
	}
	return coreexecutor.Response{Payload: e.payload}, nil
}

func (e *gatedExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "not implemented"}
}

func (e *gatedExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *gatedExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "not implemented"}
}

func (e *gatedExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "not implemented"}
}

func newKeepAliveTestHandler(t *testing.T, executor *gatedExecutor, model string) *OpenAIAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: executor.id + "-auth", Provider: executor.id, Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{NonStreamKeepAliveInterval: 1}, manager)
	return NewOpenAIAPIHandler(base)
}

func doKeepAliveRequest(t *testing.T, method, route, target, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, route, handler)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

// Text endpoints must never start a keep-alive: a real upstream status survives even when
// the setting is enabled.
func TestChatCompletionsNonStreamKeepsUpstreamStatusWithKeepAliveEnabled(t *testing.T) {
	ticks := installKeepAliveTicks(t)
	executor := &gatedExecutor{
		id:  "keepalive-text-executor",
		err: &coreauth.Error{Code: "rate_limited", Message: "slow down", HTTPStatus: http.StatusTooManyRequests},
	}
	handler := newKeepAliveTestHandler(t, executor, "keepalive-text-model")

	resp := doKeepAliveRequest(t, http.MethodPost, "/v1/chat/completions", "/v1/chat/completions",
		`{"model":"keepalive-text-model","messages":[{"role":"user","content":"hi"}]}`, handler.ChatCompletions)

	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d body=%q", resp.Code, http.StatusTooManyRequests, resp.Body.String())
	}
	if got := ticks.created.Load(); got != 0 {
		t.Fatalf("keep-alive tickers created = %d, want 0 for text endpoint", got)
	}
	if strings.HasPrefix(resp.Body.String(), "\n") {
		t.Fatalf("text error body has keep-alive prefix: %q", resp.Body.String())
	}
}

// Media JSON endpoints keep the intentional keep-alive: whitespace precedes a valid JSON result.
func TestVideosRetrieveEmitsKeepAliveBeforeJSONResult(t *testing.T) {
	resetVideoAuthBindingsForTest(t)
	ticks := installKeepAliveTicks(t)
	executor := &gatedExecutor{
		id:      "xai",
		payload: []byte(`{"request_id":"video_ka","status":"completed","progress":100,"video":{"url":"https://example.invalid/v.mp4","duration":4}}`),
	}
	executor.before = func() { ticks.emit(t, 2) }
	handler := newKeepAliveTestHandler(t, executor, defaultXAIVideosModel)

	resp := doKeepAliveRequest(t, http.MethodGet, openAIVideosPath+"/:video_id", openAIVideosPath+"/video_ka", "", handler.VideosRetrieve)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	body := resp.Body.String()
	if !strings.HasPrefix(body, "\n") {
		t.Fatalf("body = %q, want leading keep-alive whitespace", body)
	}
	if !json.Valid([]byte(body)) {
		t.Fatalf("keep-alive prefixed body is not valid JSON: %q", body)
	}
}

// Before the first tick, a media error still carries its real status.
func TestVideosRetrieveErrorBeforeFirstTickKeepsStatus(t *testing.T) {
	resetVideoAuthBindingsForTest(t)
	ticks := installKeepAliveTicks(t)
	executor := &gatedExecutor{
		id:  "xai",
		err: &coreauth.Error{Code: "rate_limited", Message: "slow down", HTTPStatus: http.StatusTooManyRequests},
	}
	handler := newKeepAliveTestHandler(t, executor, defaultXAIVideosModel)

	resp := doKeepAliveRequest(t, http.MethodGet, openAIVideosPath+"/:video_id", openAIVideosPath+"/video_ka", "", handler.VideosRetrieve)

	if ticks.created.Load() != 1 {
		t.Fatalf("keep-alive tickers created = %d, want 1", ticks.created.Load())
	}
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d body=%q", resp.Code, http.StatusTooManyRequests, resp.Body.String())
	}
}

// After a tick the status is committed as 200; the real error is only delivered in the body.
func TestVideosRetrieveErrorAfterTickIsCommittedAs200(t *testing.T) {
	resetVideoAuthBindingsForTest(t)
	ticks := installKeepAliveTicks(t)
	executor := &gatedExecutor{
		id:  "xai",
		err: &coreauth.Error{Code: "rate_limited", Message: "slow down", HTTPStatus: http.StatusTooManyRequests},
	}
	executor.before = func() { ticks.emit(t, 2) }
	handler := newKeepAliveTestHandler(t, executor, defaultXAIVideosModel)

	resp := doKeepAliveRequest(t, http.MethodGet, openAIVideosPath+"/:video_id", openAIVideosPath+"/video_ka", "", handler.VideosRetrieve)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (committed by keep-alive)", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "slow down") {
		t.Fatalf("error not delivered in body: %q", resp.Body.String())
	}
}

// Binary downloads must not be prefixed with keep-alive bytes or have their headers pre-committed.
func TestVideosContentBinaryDownloadIsNotPrefixedByKeepAlive(t *testing.T) {
	resetVideoAuthBindingsForTest(t)
	ticks := installKeepAliveTicks(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "11")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer upstream.Close()

	executor := &gatedExecutor{
		id:      "xai",
		payload: []byte(`{"request_id":"video_ka","status":"completed","progress":100,"video":{"url":"` + upstream.URL + `/video.mp4","duration":4}}`),
	}
	// If a keep-alive exists for this request, drive ticks while the metadata lookup runs.
	executor.before = func() {
		if ticks.created.Load() > 0 {
			ticks.emit(t, 2)
		}
	}
	handler := newKeepAliveTestHandler(t, executor, defaultXAIVideosModel)

	resp := doKeepAliveRequest(t, http.MethodGet, openAIVideosPath+"/:video_id/content", openAIVideosPath+"/video_ka/content", "", handler.VideosContent)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	if got := resp.Body.String(); got != "video-bytes" {
		t.Fatalf("body = %q, want exact video bytes", got)
	}
	if got := resp.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := resp.Header().Get("Content-Length"); got != "11" {
		t.Fatalf("Content-Length = %q, want 11", got)
	}
	if got := ticks.created.Load(); got != 0 {
		t.Fatalf("keep-alive tickers created = %d, want 0 for binary download", got)
	}
}
