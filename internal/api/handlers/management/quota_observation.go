package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type quotaObservationWindow struct {
	Name              string   `json:"name"`
	RemainingFraction *float64 `json:"remaining_fraction"`
	ResetAt           *string  `json:"reset_at"`
}

// ObserveQuota handles POST /v8/management/routing/quota/observation. It applies
// the latest fresh quota windows of one credential to its routing quota state.
func (h *Handler) ObserveQuota(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	var req struct {
		AuthIndex string                   `json:"auth_index"`
		Windows   []quotaObservationWindow `json:"windows"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errBindJSON, http.StatusBadRequest), gin.H{"error": "invalid request body"})
		return
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	windows := make([]coreauth.QuotaWindow, 0, len(req.Windows))
	for _, in := range req.Windows {
		window := coreauth.QuotaWindow{Name: in.Name, RemainingFraction: in.RemainingFraction}
		if in.ResetAt != nil && strings.TrimSpace(*in.ResetAt) != "" {
			resetAt, errParse := time.Parse(time.RFC3339, strings.TrimSpace(*in.ResetAt))
			if errParse != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reset_at"})
				return
			}
			window.ResetAt = resetAt
		}
		windows = append(windows, window)
	}
	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	result, recoverAt, errReconcile := h.authManager.ReconcileQuota(c.Request.Context(), auth.ID, windows)
	if errReconcile != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to apply quota observation"})
		return
	}
	var recoverAtOut any
	if result == coreauth.QuotaObservationBlocked {
		recoverAtOut = recoverAt.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, gin.H{"result": result, "recover_at": recoverAtOut})
}
