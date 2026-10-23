package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/tidwall/sjson"
)

// ModelPolicyFromGin returns the model policy attached to the authenticated request, or nil
// when the caller is unrestricted.
func ModelPolicyFromGin(c *gin.Context) *sdkaccess.ModelPolicy {
	if c == nil {
		return nil
	}
	policy, _ := c.Get(sdkaccess.ModelPolicyContextKey)
	typed, _ := policy.(*sdkaccess.ModelPolicy)
	return typed
}

// FilterModelList drops the entries of a registry model list that the request's key may not use.
// Entries are identified by their "id" (OpenAI/Claude) or "name" (Gemini) field.
func FilterModelList(c *gin.Context, models []map[string]any) []map[string]any {
	policy := ModelPolicyFromGin(c)
	if policy == nil {
		return models
	}
	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		if policy.Allows(listedModelName(model)) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func listedModelName(model map[string]any) string {
	for _, field := range []string{"id", "name"} {
		if name, _ := model[field].(string); name != "" {
			return strings.TrimPrefix(name, "models/")
		}
	}
	return ""
}

// modelAccessError rejects a request for a model the authenticated key may not use.
// The thinking suffix is ignored, and "auto" is checked against the model it resolves to.
func modelAccessError(ctx context.Context, modelName string) *interfaces.ErrorMessage {
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	policy := ModelPolicyFromGin(ginCtx)
	if policy == nil {
		return nil
	}
	base := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if !policy.Allows(base) || (base == "auto" && !policy.Allows(util.ResolveAutoModel(base))) {
		body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_allowed","param":"model"}}`
		body, _ = sjson.Set(body, "error.message", "this API key is not allowed to use model "+modelName)
		return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New(body)}
	}
	return nil
}
