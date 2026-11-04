package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
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

// FilterModelListResponseBody re-filters a serialized model-list response after
// plugin response interceptors have had an opportunity to replace its body.
func FilterModelListResponseBody(c *gin.Context, body []byte) []byte {
	if ModelPolicyFromGin(c) == nil {
		return body
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body
	}
	changed := false
	for _, field := range []string{"data", "models"} {
		raw, ok := envelope[field]
		if !ok {
			continue
		}
		var models []map[string]any
		if err := json.Unmarshal(raw, &models); err != nil {
			continue
		}
		filtered, err := json.Marshal(FilterModelList(c, models))
		if err != nil {
			return body
		}
		envelope[field] = filtered
		changed = true
	}
	if !changed {
		return body
	}
	filtered, err := json.Marshal(envelope)
	if err != nil {
		return body
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
	checkModel := base
	if checkModel == "auto" {
		checkModel = util.ResolveAutoModel(checkModel)
	}
	if !policy.Allows(checkModel) {
		body := ojson.NewDocString(`{"error":{"message":"","type":"invalid_request_error","code":"model_not_allowed","param":"model"}}`)
		_ = body.Set("error.message", "this API key is not allowed to use model "+modelName)
		return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New(body.String())}
	}
	return nil
}
