package auth

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// QuotaWindow is one normalized quota window of a fresh quota observation.
type QuotaWindow struct {
	Name string
	// RemainingFraction is the remaining share in 0..1; nil means unknown.
	RemainingFraction *float64
	// ResetAt is the upstream reset time; the zero value means unknown.
	ResetAt time.Time
}

// QuotaObservationResult is the routing outcome of a quota observation.
type QuotaObservationResult string

const (
	QuotaObservationBlocked   QuotaObservationResult = "blocked"
	QuotaObservationReleased  QuotaObservationResult = "released"
	QuotaObservationUnchanged QuotaObservationResult = "unchanged"
)

// classifyQuotaWindow reports whether a window carries a usable reading and
// whether it is exhausted: remaining <= 0 and not already reset. It is the single
// definition used by the routing decision and by the observation log.
func classifyQuotaWindow(window QuotaWindow, now time.Time) (known, exhausted bool) {
	fraction := window.RemainingFraction
	if fraction == nil || math.IsNaN(*fraction) {
		return false, false
	}
	if *fraction > 0 {
		return true, false
	}
	if !window.ResetAt.IsZero() && !window.ResetAt.After(now) {
		return true, false
	}
	return true, true
}

// EvaluateQuotaWindows applies the quota observation semantics to a set of
// windows. A window is exhausted when its remaining fraction is <= 0 and it
// has not already reset. Any exhausted window blocks the credential until the
// latest reset among the exhausted windows only; windows that still have
// capacity never contribute a recovery time. Exhaustion without a usable
// future reset time is "unchanged" so existing 429 backoff keeps working.
func EvaluateQuotaWindows(windows []QuotaWindow, now time.Time) (QuotaObservationResult, time.Time) {
	known := false
	exhausted := false
	var recoverAt time.Time
	for _, window := range windows {
		windowKnown, windowExhausted := classifyQuotaWindow(window, now)
		if !windowKnown {
			continue
		}
		known = true
		if !windowExhausted {
			continue
		}
		exhausted = true
		if window.ResetAt.After(recoverAt) {
			recoverAt = window.ResetAt
		}
	}
	switch {
	case !known:
		return QuotaObservationUnchanged, time.Time{}
	case !exhausted:
		return QuotaObservationReleased, time.Time{}
	case recoverAt.IsZero():
		return QuotaObservationUnchanged, time.Time{}
	default:
		return QuotaObservationBlocked, recoverAt
	}
}

// ReconcileQuota applies a fresh quota observation to the auth's routing
// state: it sets the credential quota block when a window is exhausted,
// releases an active quota block when none is, and otherwise changes nothing.
// The returned time is the recovery time when the result is blocked.
func (m *Manager) ReconcileQuota(ctx context.Context, authID string, windows []QuotaWindow) (QuotaObservationResult, time.Time, error) {
	result, recoverAt, err := m.reconcileQuota(ctx, authID, windows)
	if err == nil && m != nil {
		authID = strings.TrimSpace(authID)
		provider := ""
		m.mu.RLock()
		if auth := m.auths[authID]; auth != nil {
			provider = auth.Provider
		}
		m.mu.RUnlock()
		logQuotaObservation(authID, provider, windows, result, recoverAt, time.Now())
	}
	return result, recoverAt, err
}

func (m *Manager) reconcileQuota(ctx context.Context, authID string, windows []QuotaWindow) (QuotaObservationResult, time.Time, error) {
	if m == nil {
		return QuotaObservationUnchanged, time.Time{}, nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return QuotaObservationUnchanged, time.Time{}, fmt.Errorf("auth id is required")
	}
	result, recoverAt := EvaluateQuotaWindows(windows, time.Now())
	switch result {
	case QuotaObservationBlocked:
		return m.blockCredentialQuota(ctx, authID, recoverAt)
	case QuotaObservationReleased:
		m.mu.RLock()
		blocked := hasReleasableQuotaBlock(m.auths[authID], time.Now())
		m.mu.RUnlock()
		if !blocked {
			return QuotaObservationUnchanged, time.Time{}, nil
		}
		updated, _, errReset := m.ResetQuota(ctx, authID)
		if errReset != nil || updated == nil {
			return QuotaObservationUnchanged, time.Time{}, errReset
		}
		return QuotaObservationReleased, time.Time{}, nil
	default:
		return QuotaObservationUnchanged, time.Time{}, nil
	}
}

// hasReleasableQuotaBlock reports whether the auth, or any of its models,
// carries a live quota block that a fresh observation of capacity may release.
func hasReleasableQuotaBlock(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	if hasActiveQuotaBlock(auth, now) {
		return true
	}
	for _, state := range auth.ModelStates {
		if state == nil || !state.Quota.Exceeded || !state.Quota.NextRecoverAt.After(now) {
			continue
		}
		if state.Quota.Reason == "credential_quota" || state.Quota.Reason == "quota" {
			return true
		}
	}
	return false
}

// blockCredentialQuota sets the auth-level credential quota block to recoverAt,
// overriding any shorter or longer existing quota block.
func (m *Manager) blockCredentialQuota(ctx context.Context, authID string, recoverAt time.Time) (QuotaObservationResult, time.Time, error) {
	now := time.Now()
	releaseMutation := m.lockAuthMutation(authID)
	defer releaseMutation()
	m.mu.Lock()
	auth := m.auths[authID]
	if auth == nil {
		m.mu.Unlock()
		return QuotaObservationUnchanged, time.Time{}, nil
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.Equal(recoverAt) &&
		auth.NextRetryAfter.Equal(recoverAt) {
		m.mu.Unlock()
		return QuotaObservationBlocked, recoverAt, nil
	}

	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
	}
	previous := auth.Quota.NextRecoverAt
	auth.Quota.Exceeded = true
	auth.Quota.Reason = "credential_quota"
	auth.Quota.NextRecoverAt = recoverAt
	auth.NextRetryAfter = recoverAt
	auth.Unavailable = true
	auth.Status = StatusError
	auth.StatusMessage = "quota exhausted"
	// Model states derived from the previous block follow the new deadline; an
	// independent cooldown with its own deadline is left alone.
	for _, state := range auth.ModelStates {
		if state == nil || !state.Quota.Exceeded {
			continue
		}
		derived := state.credentialQuotaPropagated || state.Quota.Reason == "credential_quota" ||
			(!previous.IsZero() && state.Quota.NextRecoverAt.Equal(previous))
		if !derived {
			continue
		}
		state.Quota.NextRecoverAt = recoverAt
		state.NextRetryAfter = recoverAt
		state.UpdatedAt = now
	}
	auth.Generation++
	auth.UpdatedAt = now
	snapshot := auth.Clone()
	cooldownStateChanged := false
	if trackCooldownState {
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, m.cooldownStateRecordsForAuthLocked(auth, now))
	}
	errPersist := m.persistLocked(ctx, auth)
	m.mu.Unlock()
	releaseMutation()

	m.publishQuotaChange(authID, snapshot, now, cooldownStateChanged)
	if errPersist != nil {
		return QuotaObservationUnchanged, time.Time{}, errPersist
	}
	return QuotaObservationBlocked, recoverAt, nil
}

// logQuotaObservation records what a quota observation did, so an operator can
// see from the log alone why a credential was blocked, released or left alone
// after a quota refresh. It carries only window names, fractions and reset times.
func logQuotaObservation(authID, provider string, windows []QuotaWindow, result QuotaObservationResult, recoverAt time.Time, now time.Time) {
	knownCount := 0
	var exhausted []string
	parts := make([]string, 0, len(windows))
	for _, window := range windows {
		known, isExhausted := classifyQuotaWindow(window, now)
		if known {
			knownCount++
		}
		if isExhausted {
			exhausted = append(exhausted, window.Name)
		}
		remaining := "?"
		if window.RemainingFraction != nil && !math.IsNaN(*window.RemainingFraction) {
			remaining = fmt.Sprintf("%.2f", *window.RemainingFraction)
		}
		reset := "-"
		if !window.ResetAt.IsZero() {
			reset = window.ResetAt.UTC().Format(time.RFC3339)
		}
		parts = append(parts, window.Name+"="+remaining+"@"+reset)
	}

	reason := ""
	switch result {
	case QuotaObservationBlocked:
		reason = "exhausted window with a future reset"
	case QuotaObservationReleased:
		reason = "capacity available; active quota block released"
	default:
		switch {
		case knownCount == 0:
			reason = "no readings"
		case len(exhausted) > 0:
			reason = "exhausted without a usable reset time"
		default:
			reason = "capacity available; no quota block to release"
		}
	}
	recover := "-"
	if !recoverAt.IsZero() {
		recover = recoverAt.UTC().Format(time.RFC3339)
	}
	exhaustedText := "-"
	if len(exhausted) > 0 {
		exhaustedText = strings.Join(exhausted, ",")
	}
	// The text log formatter prints only an allow-listed set of fields, so the
	// outcome itself lives in the message; auth_id and provider stay fields
	// (both are printed) for correlation with other lines of the same credential.
	log.WithFields(log.Fields{
		"auth_id":  authID,
		"provider": provider,
	}).Infof("quota observation: result=%s reason=%q recover_at=%s exhausted=%s windows=%s",
		result, reason, recover, exhaustedText, strings.Join(parts, ","))
}
