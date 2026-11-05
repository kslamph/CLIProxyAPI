package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// customFillFirstSelector is deliberately not a built-in selector type.
type customFillFirstSelector struct{ *FillFirstSelector }

func frac(v float64) *float64 { return &v }

func registerCredentialQuotaAuth(t *testing.T, manager *Manager, id, provider string, recoverAt time.Time) {
	t.Helper()
	const model = "reconcile-model"
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:             id,
		Provider:       provider,
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: recoverAt,
		Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt, BackoffLevel: 1},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: recoverAt,
				Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt, BackoffLevel: 1},
				UpdatedAt:      time.Now(),

				credentialQuotaPropagated: true,
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
}

func TestEvaluateQuotaWindows(t *testing.T) {
	now := time.Date(2026, time.November, 4, 10, 0, 0, 0, time.UTC) // Wednesday
	today1400 := time.Date(2026, time.November, 4, 14, 0, 0, 0, time.UTC)
	monday := time.Date(2026, time.November, 9, 0, 0, 0, 0, time.UTC)
	later5h := time.Date(2026, time.November, 4, 15, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)

	tests := []struct {
		name    string
		windows []QuotaWindow
		want    QuotaObservationResult
		wantAt  time.Time
	}{
		// The three contract examples. The first one fails against a naive
		// "max reset over all windows" implementation (it would pick Monday).
		{
			name: "example1_5h_exhausted_weekly_has_quota_recovers_at_14:00_not_monday",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0), ResetAt: today1400},
				{Name: "weekly", RemainingFraction: frac(0.4), ResetAt: monday},
			},
			want: QuotaObservationBlocked, wantAt: today1400,
		},
		{
			name: "example2_both_exhausted_recovers_at_monday",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0), ResetAt: today1400},
				{Name: "weekly", RemainingFraction: frac(0), ResetAt: monday},
			},
			want: QuotaObservationBlocked, wantAt: monday,
		},
		{
			name: "example3_only_weekly_exhausted_recovers_at_weekly_even_if_5h_resets_later",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0.5), ResetAt: later5h},
				{Name: "weekly", RemainingFraction: frac(0), ResetAt: today1400},
			},
			want: QuotaObservationBlocked, wantAt: today1400,
		},
		{
			name:    "no_windows_unchanged",
			windows: nil,
			want:    QuotaObservationUnchanged,
		},
		{
			name:    "no_known_windows_unchanged",
			windows: []QuotaWindow{{Name: "5h", ResetAt: today1400}},
			want:    QuotaObservationUnchanged,
		},
		{
			name: "exhausted_without_reset_unchanged",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0)},
				{Name: "weekly", RemainingFraction: frac(0.4), ResetAt: monday},
			},
			want: QuotaObservationUnchanged,
		},
		{
			name: "exhausted_unknown_reset_ignored_when_other_exhausted_has_reset",
			windows: []QuotaWindow{
				{Name: "a", RemainingFraction: frac(0)},
				{Name: "b", RemainingFraction: frac(0), ResetAt: today1400},
			},
			want: QuotaObservationBlocked, wantAt: today1400,
		},
		{
			name: "exhausted_window_already_reset_is_not_exhausted_released",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0), ResetAt: past},
				{Name: "weekly", RemainingFraction: frac(0.4), ResetAt: monday},
			},
			want: QuotaObservationReleased,
		},
		{
			name: "already_reset_exhausted_window_does_not_contribute_recover_at",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0), ResetAt: past},
				{Name: "weekly", RemainingFraction: frac(0), ResetAt: today1400},
			},
			want: QuotaObservationBlocked, wantAt: today1400,
		},
		{
			name: "negative_fraction_is_exhausted",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(-0.1), ResetAt: today1400},
			},
			want: QuotaObservationBlocked, wantAt: today1400,
		},
		{
			name: "all_windows_have_quota_released",
			windows: []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0.2), ResetAt: today1400},
				{Name: "weekly", RemainingFraction: frac(1), ResetAt: monday},
			},
			want: QuotaObservationReleased,
		},
		{
			name: "unknown_window_alongside_known_with_quota_released",
			windows: []QuotaWindow{
				{Name: "x"},
				{Name: "5h", RemainingFraction: frac(0.2)},
			},
			want: QuotaObservationReleased,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotAt := EvaluateQuotaWindows(tc.windows, now)
			if got != tc.want || !gotAt.Equal(tc.wantAt) {
				t.Fatalf("EvaluateQuotaWindows() = %s %v, want %s %v", got, gotAt, tc.want, tc.wantAt)
			}
		})
	}
}

func TestManagerReconcileQuota_BlocksCredentialWithoutPriorBlock(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	store := &strictContextCheckingStore{}
	manager.SetCooldownStateStore(store)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "reconcile-fresh", Provider: "opencode-go"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	resetAt := time.Now().Add(3 * time.Hour).Round(0)

	result, recoverAt, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-fresh", []QuotaWindow{
		{Name: "5h", RemainingFraction: frac(0), ResetAt: resetAt},
	})
	if errReconcile != nil || result != QuotaObservationBlocked || !recoverAt.Equal(resetAt) {
		t.Fatalf("ReconcileQuota() = %s %v %v, want blocked %v", result, recoverAt, errReconcile, resetAt)
	}
	current, _ := manager.GetByID("reconcile-fresh")
	if !current.Quota.Exceeded || current.Quota.Reason != "credential_quota" ||
		!current.Quota.NextRecoverAt.Equal(resetAt) || !current.NextRetryAfter.Equal(resetAt) {
		t.Fatalf("auth not blocked: quota=%+v next=%v", current.Quota, current.NextRetryAfter)
	}
	records, _ := store.Load(context.Background())
	if len(records) == 0 || !records[0].Quota.NextRecoverAt.Equal(resetAt) {
		t.Fatalf("block not persisted through cooldown store: %+v", records)
	}
	// Idempotent re-observation keeps the block without bumping state.
	generation := current.Generation
	if result, _, _ := manager.ReconcileQuota(context.Background(), "reconcile-fresh", []QuotaWindow{
		{Name: "5h", RemainingFraction: frac(0), ResetAt: resetAt},
	}); result != QuotaObservationBlocked {
		t.Fatalf("repeat result = %s, want blocked", result)
	}
	if again, _ := manager.GetByID("reconcile-fresh"); again.Generation != generation {
		t.Fatalf("repeat observation mutated auth: generation %d -> %d", generation, again.Generation)
	}
}

func TestManagerReconcileQuota_OverridesExistingBlockBothDirections(t *testing.T) {
	for name, existing := range map[string]time.Duration{
		"shorter_existing_extended": time.Hour,
		"longer_existing_shortened": 10 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			registerCredentialQuotaAuth(t, manager, "reconcile-override", "codex", time.Now().Add(existing).Round(0))
			resetAt := time.Now().Add(5 * time.Hour).Round(0)

			result, recoverAt, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-override", []QuotaWindow{
				{Name: "weekly", RemainingFraction: frac(0), ResetAt: resetAt},
			})
			if errReconcile != nil || result != QuotaObservationBlocked || !recoverAt.Equal(resetAt) {
				t.Fatalf("ReconcileQuota() = %s %v %v", result, recoverAt, errReconcile)
			}
			current, _ := manager.GetByID("reconcile-override")
			if !current.Quota.NextRecoverAt.Equal(resetAt) || !current.NextRetryAfter.Equal(resetAt) {
				t.Fatalf("auth recovery = %v / %v, want %v", current.Quota.NextRecoverAt, current.NextRetryAfter, resetAt)
			}
			state := current.ModelStates["reconcile-model"]
			if !state.Quota.NextRecoverAt.Equal(resetAt) || !state.NextRetryAfter.Equal(resetAt) {
				t.Fatalf("propagated model state recovery = %v / %v, want %v", state.Quota.NextRecoverAt, state.NextRetryAfter, resetAt)
			}
		})
	}
}

func TestManagerReconcileQuota_ReleasesStaleBlock(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	store := &strictContextCheckingStore{}
	manager.SetCooldownStateStore(store)
	registerCredentialQuotaAuth(t, manager, "reconcile-available", "codex", time.Now().Add(72*time.Hour))
	manager.persistCooldownStates(context.Background())

	result, recoverAt, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-available", []QuotaWindow{
		{Name: "5h", RemainingFraction: frac(0.9), ResetAt: time.Now().Add(time.Hour)},
		{Name: "weekly", RemainingFraction: frac(0.3), ResetAt: time.Now().Add(72 * time.Hour)},
	})
	if errReconcile != nil || result != QuotaObservationReleased || !recoverAt.IsZero() {
		t.Fatalf("ReconcileQuota() = %s %v %v, want released", result, recoverAt, errReconcile)
	}
	current, _ := manager.GetByID("reconcile-available")
	if current.Quota.Exceeded || current.Unavailable || !current.NextRetryAfter.IsZero() {
		t.Fatalf("auth still blocked: %+v", current)
	}
	if records, _ := store.Load(context.Background()); len(records) != 0 {
		t.Fatalf("persisted cooldown records = %+v, want none", records)
	}
}

func TestManagerReconcileQuota_UnchangedCases(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	retryAt := time.Now().Add(time.Hour).Round(0)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "reconcile-transient", Provider: "codex", Status: StatusError, Unavailable: true, NextRetryAfter: retryAt,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	before, _ := manager.GetByID("reconcile-transient")

	for name, windows := range map[string][]QuotaWindow{
		"released_without_block":  {{Name: "5h", RemainingFraction: frac(0.5)}},
		"exhausted_without_reset": {{Name: "5h", RemainingFraction: frac(0)}},
		"no_known_windows":        {{Name: "5h"}},
		"empty":                   nil,
	} {
		result, recoverAt, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-transient", windows)
		if errReconcile != nil || result != QuotaObservationUnchanged || !recoverAt.IsZero() {
			t.Fatalf("%s: ReconcileQuota() = %s %v %v, want unchanged", name, result, recoverAt, errReconcile)
		}
	}
	after, _ := manager.GetByID("reconcile-transient")
	if after.Generation != before.Generation || !after.Unavailable || !after.NextRetryAfter.Equal(retryAt) {
		t.Fatalf("auth mutated: before=%+v after=%+v", before, after)
	}
	result, _, errReconcile := manager.ReconcileQuota(context.Background(), "missing-auth", []QuotaWindow{
		{Name: "5h", RemainingFraction: frac(0), ResetAt: time.Now().Add(time.Hour)},
	})
	if errReconcile != nil || result != QuotaObservationUnchanged {
		t.Fatalf("missing auth: %s %v, want unchanged", result, errReconcile)
	}
}

// TestManagerReconcileQuota_RoutingExcludesBlockedAuth proves through the real
// scheduler pick path that an observation-blocked auth is skipped while another
// auth is picked, and that a releasing observation makes it pickable at once.
func TestManagerReconcileQuota_RoutingExcludesBlockedAuth(t *testing.T) {
	// A custom selector is not built in, which sends pickNext down the legacy
	// path used when plugin schedulers or custom selection are active.
	type routingCase struct {
		provider string
		selector Selector
	}
	var cases []routingCase
	for _, provider := range []string{"opencode-go", "codex"} {
		cases = append(cases,
			routingCase{provider + "/scheduler", &FillFirstSelector{}},
			routingCase{provider + "/legacy", customFillFirstSelector{&FillFirstSelector{}}},
		)
	}
	for _, tc := range cases {
		provider := strings.Split(tc.provider, "/")[0]
		t.Run(tc.provider, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, tc.selector, nil)
			manager.RegisterExecutor(schedulerProviderTestExecutor{provider: provider})
			model := "reconcile-routing-" + tc.provider
			first := &Auth{ID: "routing-" + tc.provider + "-a", Provider: provider}
			second := &Auth{ID: "routing-" + tc.provider + "-b", Provider: provider}
			for _, a := range []*Auth{first, second} {
				if _, errRegister := manager.Register(ctx, a); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}
			}
			registerSchedulerModels(t, provider, model, first.ID, second.ID)
			manager.RefreshSchedulerEntry(first.ID)
			manager.RefreshSchedulerEntry(second.ID)
			pick := func() string {
				t.Helper()
				picked, _, errPick := manager.pickNext(ctx, provider, model, cliproxyexecutor.Options{}, nil)
				if errPick != nil || picked == nil {
					t.Fatalf("pickNext() = %v, %v", picked, errPick)
				}
				return picked.ID
			}
			if got := pick(); got != first.ID {
				t.Fatalf("baseline pick = %s, want %s", got, first.ID)
			}

			resetAt := time.Now().Add(2 * time.Hour)
			if result, _, errReconcile := manager.ReconcileQuota(ctx, first.ID, []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0), ResetAt: resetAt},
				{Name: "weekly", RemainingFraction: frac(0.4), ResetAt: resetAt.Add(72 * time.Hour)},
			}); errReconcile != nil || result != QuotaObservationBlocked {
				t.Fatalf("block observation = %s, %v", result, errReconcile)
			}
			for i := 0; i < 3; i++ {
				if got := pick(); got != second.ID {
					t.Fatalf("blocked auth picked: got %s, want %s", got, second.ID)
				}
			}

			// With every auth blocked nothing is pickable.
			if result, _, errReconcile := manager.ReconcileQuota(ctx, second.ID, []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0), ResetAt: resetAt},
			}); errReconcile != nil || result != QuotaObservationBlocked {
				t.Fatalf("block second observation = %s, %v", result, errReconcile)
			}
			if picked, _, errPick := manager.pickNext(ctx, provider, model, cliproxyexecutor.Options{}, nil); errPick == nil {
				t.Fatalf("pickNext() picked %s while all auths are quota blocked", picked.ID)
			}

			if result, _, errReconcile := manager.ReconcileQuota(ctx, first.ID, []QuotaWindow{
				{Name: "5h", RemainingFraction: frac(0.8), ResetAt: resetAt},
			}); errReconcile != nil || result != QuotaObservationReleased {
				t.Fatalf("release observation = %s, %v", result, errReconcile)
			}
			if got := pick(); got != first.ID {
				t.Fatalf("released auth not picked: got %s, want %s", got, first.ID)
			}
		})
	}
}

// TestManagerReconcileQuota_OverridesRealRateLimitBlock seeds the block through
// the real 429 path, then proves a shorter observed reset wins for the auth and
// leaves no stale longer model deadline once the observed reset has passed.
func TestManagerReconcileQuota_OverridesRealRateLimitBlock(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})
	const model = "reconcile-429-model"
	auth := &Auth{ID: "reconcile-429-auth", Provider: "codex"}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registerSchedulerModels(t, "codex", model, auth.ID)
	retryAfter := 10 * time.Hour
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "codex", Model: model, Success: false, CredentialScope: true,
		RetryAfter: &retryAfter,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})
	before, _ := manager.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(before, model, time.Now()); !blocked {
		t.Fatalf("429 did not block the auth: %+v", before)
	}

	resetAt := time.Now().Add(time.Hour).Round(0)
	result, _, errReconcile := manager.ReconcileQuota(ctx, auth.ID, []QuotaWindow{{Name: "5h", RemainingFraction: frac(0), ResetAt: resetAt}})
	if errReconcile != nil || result != QuotaObservationBlocked {
		t.Fatalf("ReconcileQuota() = %s, %v", result, errReconcile)
	}
	current, _ := manager.GetByID(auth.ID)
	if !current.Quota.NextRecoverAt.Equal(resetAt) || !current.NextRetryAfter.Equal(resetAt) {
		t.Fatalf("auth recovery = %v / %v, want %v", current.Quota.NextRecoverAt, current.NextRetryAfter, resetAt)
	}
	if blocked, _, _ := isAuthBlockedForModel(current, model, resetAt.Add(-time.Minute)); !blocked {
		t.Fatal("auth must stay blocked until the observed reset")
	}
	if blocked, _, next := isAuthBlockedForModel(current, model, resetAt.Add(time.Second)); blocked {
		t.Fatalf("stale longer model deadline survives the observed reset: next=%v state=%+v", next, current.ModelStates[model])
	}
}
