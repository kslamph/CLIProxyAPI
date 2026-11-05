package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A delegated Codex capacity overload (server_is_overloaded) is capacity
// noise, not a credential fault. The conductor must keep the credential
// immediately selectable, never rebind session affinity, and never replay the
// request on another credential, while rate-limit/quota failures keep their
// existing cooldown handling byte-for-byte.

type overloadDelegatedError struct{ msg string }

func (e overloadDelegatedError) Error() string                  { return e.msg }
func (e overloadDelegatedError) StatusCode() int                { return http.StatusTooManyRequests }
func (e overloadDelegatedError) IsCodexOverloadDelegated() bool { return true }

const overloadDelegatedBody = `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","headers":{"x-retry-metadata":"NO_MORE_RETRY","retry-after":"5","retry-after-ms":"5000"},"message":"Our servers are currently overloaded. Please try again later.","param":null},"sequence_number":3}`

func overloadDelegatedRegister(t *testing.T, manager *Manager, auth *Auth) {
	t.Helper()
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
}

func TestOverloadDelegatedResultErrorFromErrorKeepsClassification(t *testing.T) {
	resultErr := resultErrorFromError(overloadDelegatedError{msg: overloadDelegatedBody})
	if resultErr == nil || resultErr.Code != ErrorCodeOverloadDelegated {
		t.Fatalf("result error code = %v, want %q", resultErr, ErrorCodeOverloadDelegated)
	}
	if resultErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("result error status = %d, want 429", resultErr.HTTPStatus)
	}
	if !isCodexOverloadDelegatedResultError(resultErr) {
		t.Fatal("isCodexOverloadDelegatedResultError must be true for the classified result error")
	}
	if !IsCodexOverloadDelegated(overloadDelegatedError{msg: overloadDelegatedBody}) {
		t.Fatal("exported IsCodexOverloadDelegated must detect the marker error")
	}
	// The classification round-trips through the auth Error code as well.
	if !isCodexOverloadDelegatedError(resultErr) {
		t.Fatal("marker must survive once recorded on an auth Error")
	}
}

func TestOverloadDelegatedMarkResultWritesNoCooldownState(t *testing.T) {
	withQuotaCooldownEnabled(t)

	hint := 5 * time.Second
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-overload",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
		ModelStates: map[string]*ModelState{
			"gpt-6.1-sol": {Status: StatusActive},
		},
	}
	overloadDelegatedRegister(t, manager, auth)

	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-6.1-sol",
		Success:    false,
		RetryAfter: &hint,
		Error: &Error{
			Code:       ErrorCodeOverloadDelegated,
			Message:    overloadDelegatedBody,
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatal("auth not found after MarkResult")
	}
	if got.Unavailable {
		t.Fatal("credential must stay immediately selectable after a delegated overload")
	}
	if !got.NextRetryAfter.IsZero() {
		t.Fatalf("credential NextRetryAfter = %v, want zero", got.NextRetryAfter)
	}
	if got.Quota.Exceeded {
		t.Fatalf("no quota state may be written, got %+v", got.Quota)
	}
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil {
		t.Fatal("expected model state to exist")
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("model must stay selectable, got unavailable=%v next=%v", state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.BackoffLevel != 0 {
		t.Fatalf("quota backoff must not move, got %+v", state.Quota)
	}

	// Regression guard: a rate-limit 429 keeps today's cooldown behaviour.
	rateHint := 42 * time.Second
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-6.1-sol",
		Success:    false,
		RetryAfter: &rateHint,
		Error: &Error{
			Message:    `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"rate limited"}}`,
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})
	got, _ = manager.GetByID(auth.ID)
	state = got.ModelStates["gpt-6.1-sol"]
	if state == nil || !state.Unavailable || state.NextRetryAfter.Before(time.Now().Add(30*time.Second)) {
		t.Fatalf("rate limit must keep its cooldown, got %+v", state)
	}
	if !state.Quota.Exceeded {
		t.Fatalf("rate limit must keep its quota labelling, got %+v", state.Quota)
	}
}

func TestOverloadDelegatedStopsRetryRounds(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, time.Minute, 3)
	auth := &Auth{ID: "auth-retry", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex"}}
	overloadDelegatedRegister(t, manager, auth)
	err := overloadDelegatedError{msg: overloadDelegatedBody}
	wait, shouldRetry := manager.shouldRetryAfterError(err, 0, []string{"codex"}, "", time.Minute)
	if shouldRetry {
		t.Fatalf("delegated overload must not start a retry round (wait %v)", wait)
	}

	// Regression guard: a plain 502 stays retry-eligible, as today.
	wait, shouldRetry = manager.shouldRetryAfterError(&Error{Message: "upstream error", HTTPStatus: http.StatusBadGateway, Retryable: true}, 0, []string{"codex"}, "", time.Minute)
	if !shouldRetry {
		t.Fatalf("plain 502 must remain retry-eligible (wait %v)", wait)
	}
}

func TestOverloadDelegatedDoesNotRebindSessionAffinity(t *testing.T) {
	err := &Error{Code: ErrorCodeOverloadDelegated, Message: overloadDelegatedBody, HTTPStatus: http.StatusTooManyRequests}
	if shouldRebindSessionAffinityOnError(err) {
		t.Fatal("a delegated overload must keep the session bound to its credential")
	}
	if !shouldSkipCredentialCooldown(err) {
		t.Fatal("a delegated overload must skip credential cooldown bookkeeping")
	}
}
