package auth

import (
	"context"
	"testing"
	"time"
)

func registerCredentialQuotaAuth(t *testing.T, manager *Manager, id string, recoverAt time.Time) {
	t.Helper()
	const model = "reconcile-model"
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:             id,
		Provider:       "codex",
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

func TestManagerReconcileQuota_AvailableReleasesCredentialQuota(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	store := &strictContextCheckingStore{}
	manager.SetCooldownStateStore(store)
	recoverAt := time.Now().Add(3 * time.Hour)
	registerCredentialQuotaAuth(t, manager, "reconcile-available", recoverAt)
	manager.persistCooldownStates(context.Background())
	if records, _ := store.Load(context.Background()); len(records) == 0 {
		t.Fatal("expected persisted cooldown record before reconcile")
	}

	updated, changed, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-available", QuotaReconcile{Available: true})
	if errReconcile != nil {
		t.Fatalf("ReconcileQuota() error = %v", errReconcile)
	}
	if !changed || updated == nil {
		t.Fatalf("ReconcileQuota() changed = %v updated = %v, want change", changed, updated)
	}
	if updated.Quota.Exceeded || !updated.Quota.NextRecoverAt.IsZero() || !updated.NextRetryAfter.IsZero() || updated.Unavailable {
		t.Fatalf("auth still blocked after reconcile: quota=%+v next=%v unavailable=%v", updated.Quota, updated.NextRetryAfter, updated.Unavailable)
	}
	current, _ := manager.GetByID("reconcile-available")
	if blocked, _, _ := isAuthBlockedForModel(current, "reconcile-model", time.Now()); blocked {
		t.Fatalf("auth must be schedulable after reconcile: %+v", current)
	}
	if records, _ := store.Load(context.Background()); len(records) != 0 {
		t.Fatalf("persisted cooldown records = %+v, want none", records)
	}
}

func TestManagerReconcileQuota_EarlierResetMovesRecovery(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	store := &strictContextCheckingStore{}
	manager.SetCooldownStateStore(store)
	recoverAt := time.Now().Add(5 * time.Hour).Round(0)
	earlier := time.Now().Add(time.Hour).Round(0)
	registerCredentialQuotaAuth(t, manager, "reconcile-earlier", recoverAt)

	updated, changed, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-earlier", QuotaReconcile{ResetAt: earlier})
	if errReconcile != nil {
		t.Fatalf("ReconcileQuota() error = %v", errReconcile)
	}
	if !changed || updated == nil {
		t.Fatalf("ReconcileQuota() changed = %v, want change", changed)
	}
	if !updated.Quota.Exceeded || !updated.Quota.NextRecoverAt.Equal(earlier) || !updated.NextRetryAfter.Equal(earlier) {
		t.Fatalf("auth recovery = quota %+v next %v, want %v", updated.Quota, updated.NextRetryAfter, earlier)
	}
	state := updated.ModelStates["reconcile-model"]
	if state == nil || !state.Quota.NextRecoverAt.Equal(earlier) || !state.NextRetryAfter.Equal(earlier) {
		t.Fatalf("model state recovery not moved: %+v", state)
	}
	if blocked, _, next := isAuthBlockedForModel(updated, "reconcile-model", time.Now()); !blocked || !next.Equal(earlier) {
		t.Fatalf("blocked = %v next = %v, want blocked until %v", blocked, next, earlier)
	}
	records, _ := store.Load(context.Background())
	if len(records) == 0 {
		t.Fatal("expected persisted cooldown records after reconcile")
	}
	for _, record := range records {
		if !record.Quota.NextRecoverAt.Equal(earlier) {
			t.Fatalf("persisted record recovery = %v, want %v", record.Quota.NextRecoverAt, earlier)
		}
	}
}

func TestManagerReconcileQuota_LaterOrPastResetKeepsRecovery(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	recoverAt := time.Now().Add(2 * time.Hour).Round(0)
	registerCredentialQuotaAuth(t, manager, "reconcile-later", recoverAt)

	for name, resetAt := range map[string]time.Time{
		"later": recoverAt.Add(time.Hour),
		"past":  time.Now().Add(-time.Minute),
		"zero":  {},
	} {
		_, changed, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-later", QuotaReconcile{ResetAt: resetAt})
		if errReconcile != nil || changed {
			t.Fatalf("%s: ReconcileQuota() changed = %v err = %v, want no change", name, changed, errReconcile)
		}
	}
	current, _ := manager.GetByID("reconcile-later")
	if !current.Quota.NextRecoverAt.Equal(recoverAt) || !current.NextRetryAfter.Equal(recoverAt) {
		t.Fatalf("recovery changed: quota %+v next %v", current.Quota, current.NextRetryAfter)
	}
}

func TestManagerReconcileQuota_NoQuotaBlockChangesNothing(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	retryAt := time.Now().Add(time.Hour).Round(0)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:             "reconcile-transient",
		Provider:       "codex",
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: retryAt,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	before, _ := manager.GetByID("reconcile-transient")

	for _, fresh := range []QuotaReconcile{{Available: true}, {ResetAt: time.Now().Add(time.Minute)}} {
		updated, changed, errReconcile := manager.ReconcileQuota(context.Background(), "reconcile-transient", fresh)
		if errReconcile != nil || changed || updated != nil {
			t.Fatalf("ReconcileQuota(%+v) = %v, %v, %v, want no-op", fresh, updated, changed, errReconcile)
		}
	}
	after, _ := manager.GetByID("reconcile-transient")
	if after.Generation != before.Generation || !after.Unavailable || !after.NextRetryAfter.Equal(retryAt) {
		t.Fatalf("auth mutated: before=%+v after=%+v", before, after)
	}
	if updated, changed, errReconcile := manager.ReconcileQuota(context.Background(), "missing-auth", QuotaReconcile{Available: true}); updated != nil || changed || errReconcile != nil {
		t.Fatalf("missing auth must be a no-op, got %v %v %v", updated, changed, errReconcile)
	}
}
