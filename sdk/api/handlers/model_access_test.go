package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func ginWithPolicy(policy *sdkaccess.ModelPolicy) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(sdkaccess.ModelPolicyContextKey, policy)
	return c
}

func TestFilterModelList(t *testing.T) {
	models := []map[string]any{{"id": "gpt-5"}, {"id": "claude-opus"}, {"name": "models/gemini-3"}}
	policy := sdkaccess.NewModelPolicy([]string{"claude-*", "gemini-*"}, nil)

	got := FilterModelList(ginWithPolicy(policy), models)
	if len(got) != 2 || got[0]["id"] != "claude-opus" || got[1]["name"] != "models/gemini-3" {
		t.Fatalf("unexpected filtered list: %v", got)
	}
	if got := FilterModelList(ginWithPolicy(nil), models); len(got) != len(models) {
		t.Fatalf("nil policy must not filter, got %v", got)
	}
	if got := FilterModelList(nil, models); len(got) != len(models) {
		t.Fatalf("nil context must not filter, got %v", got)
	}
}

func TestModelAccessError(t *testing.T) {
	policy := sdkaccess.NewModelPolicy([]string{"claude-*"}, []string{"claude-3-*"})
	ctx := context.WithValue(context.Background(), "gin", ginWithPolicy(policy))

	for model, wantDenied := range map[string]bool{
		"claude-opus":         false,
		"claude-opus(8192)":   false, // thinking suffix is ignored
		"claude-3-haiku":      true,
		"claude-3-haiku(low)": true,
		"gpt-5":               true,
	} {
		errMsg := modelAccessError(ctx, model)
		if (errMsg != nil) != wantDenied {
			t.Fatalf("modelAccessError(%q) = %v, wantDenied=%v", model, errMsg, wantDenied)
		}
		if errMsg != nil && errMsg.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", errMsg.StatusCode)
		}
	}
	if modelAccessError(context.Background(), "gpt-5") != nil {
		t.Fatal("request without a policy must be unrestricted")
	}
}
