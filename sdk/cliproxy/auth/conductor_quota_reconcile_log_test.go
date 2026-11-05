package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// setupInfoLoggerHook captures standard-logger entries at info level and
// restores the logger configuration afterwards.
func setupInfoLoggerHook(t *testing.T) *logtest.Hook {
	t.Helper()
	_, hook := logtest.NewNullLogger()
	oldLevel := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	savedHooks := make(log.LevelHooks)
	for lvl, hs := range log.StandardLogger().Hooks {
		savedHooks[lvl] = append([]log.Hook(nil), hs...)
	}
	log.AddHook(hook)
	t.Cleanup(func() {
		log.SetLevel(oldLevel)
		log.StandardLogger().ReplaceHooks(savedHooks)
	})
	return hook
}

// quotaObservationEntries returns the info entries written for quota observations.
func quotaObservationEntries(hook *logtest.Hook) []*log.Entry {
	var out []*log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.InfoLevel && strings.HasPrefix(entry.Message, "quota observation") {
			out = append(out, entry)
		}
	}
	return out
}

func TestReconcileQuotaLogsObservationOutcome(t *testing.T) {
	now := time.Now().Round(0)
	weeklyReset := now.Add(72 * time.Hour).UTC()
	tests := []struct {
		name         string
		blockedFirst bool
		windows      []QuotaWindow
		wantResult   QuotaObservationResult
		wantReason   string
		wantRecover  string
		wantExhaust  string
		wantWindows  []string
	}{
		{
			name: "blocked: exhausted weekly, later monthly reset ignored",
			windows: []QuotaWindow{
				{Name: "rolling", RemainingFraction: frac(1), ResetAt: now.Add(time.Hour)},
				{Name: "weekly", RemainingFraction: frac(0), ResetAt: weeklyReset},
				{Name: "monthly", RemainingFraction: frac(0.25), ResetAt: now.Add(400 * time.Hour)},
			},
			wantResult:  QuotaObservationBlocked,
			wantReason:  "exhausted window with a future reset",
			wantRecover: weeklyReset.Format(time.RFC3339),
			wantExhaust: "weekly",
			wantWindows: []string{"rolling=1.00@", "weekly=0.00@" + weeklyReset.Format(time.RFC3339), "monthly=0.25@"},
		},
		{
			name:         "released: capacity everywhere clears a stale block",
			blockedFirst: true,
			windows: []QuotaWindow{
				{Name: "rolling", RemainingFraction: frac(1), ResetAt: now.Add(time.Hour)},
				{Name: "weekly", RemainingFraction: frac(0.6), ResetAt: weeklyReset},
			},
			wantResult:  QuotaObservationReleased,
			wantReason:  "capacity available; active quota block released",
			wantRecover: "-",
			wantExhaust: "-",
			wantWindows: []string{"rolling=1.00@", "weekly=0.60@"},
		},
		{
			name: "unchanged: no readings",
			windows: []QuotaWindow{
				{Name: "weekly"},
			},
			wantResult:  QuotaObservationUnchanged,
			wantReason:  "no readings",
			wantRecover: "-",
			wantExhaust: "-",
			wantWindows: []string{"weekly=?@-"},
		},
		{
			name: "unchanged: exhausted without a usable reset time",
			windows: []QuotaWindow{
				{Name: "weekly", RemainingFraction: frac(0)},
			},
			wantResult:  QuotaObservationUnchanged,
			wantReason:  "exhausted without a usable reset time",
			wantRecover: "-",
			wantExhaust: "weekly",
			wantWindows: []string{"weekly=0.00@-"},
		},
		{
			name: "unchanged: capacity available and nothing to release",
			windows: []QuotaWindow{
				{Name: "rolling", RemainingFraction: frac(0.9), ResetAt: now.Add(time.Hour)},
			},
			wantResult:  QuotaObservationUnchanged,
			wantReason:  "capacity available; no quota block to release",
			wantRecover: "-",
			wantExhaust: "-",
			wantWindows: []string{"rolling=0.90@"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hook := setupInfoLoggerHook(t)
			manager := NewManager(nil, nil, nil)
			const id = "obs-log-auth"
			if tc.blockedFirst {
				registerCredentialQuotaAuth(t, manager, id, "opencode-go", now.Add(48*time.Hour))
			} else if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "opencode-go"}); err != nil {
				t.Fatalf("register: %v", err)
			}
			hook.Reset()

			result, _, err := manager.ReconcileQuota(context.Background(), id, tc.windows)
			if err != nil || result != tc.wantResult {
				t.Fatalf("ReconcileQuota() = %s, %v; want %s", result, err, tc.wantResult)
			}
			entries := quotaObservationEntries(hook)
			if len(entries) != 1 {
				t.Fatalf("got %d quota observation info entries, want exactly 1 (all entries: %v)", len(entries), hook.AllEntries())
			}
			// The text formatter only prints fields in its allow-list, so the outcome
			// must be readable in the FORMATTED line, not just in entry.Data.
			formatted, errFormat := (&internallogging.LogFormatter{}).Format(entries[0])
			if errFormat != nil {
				t.Fatalf("format: %v", errFormat)
			}
			line := string(formatted)
			wantParts := []string{
				"quota observation",
				`auth_id="` + id + `"`,
				"provider=opencode-go",
				"result=" + string(tc.wantResult),
				"reason=\"" + tc.wantReason + "\"",
				"recover_at=" + tc.wantRecover,
				"exhausted=" + tc.wantExhaust,
			}
			wantParts = append(wantParts, tc.wantWindows...)
			for _, want := range wantParts {
				if !strings.Contains(line, want) {
					t.Errorf("formatted log line missing %q:\n%s", want, strings.TrimSpace(line))
				}
			}
		})
	}
}

// A failed reconcile is reported by its callers; the observation log stays quiet
// so one failure is not logged twice.
func TestReconcileQuotaDoesNotLogOnError(t *testing.T) {
	hook := setupInfoLoggerHook(t)
	manager := NewManager(nil, nil, nil)
	if _, _, err := manager.ReconcileQuota(context.Background(), "  ", []QuotaWindow{{Name: "5h", RemainingFraction: frac(0)}}); err == nil {
		t.Fatal("expected an error for an empty auth id")
	}
	if entries := quotaObservationEntries(hook); len(entries) != 0 {
		t.Fatalf("logged %d entries on error, want 0", len(entries))
	}
}
