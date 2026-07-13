package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	authservice "github.com/sunnyside/atlas/atlas-backend/internal/services/auth"
)

const (
	principalContextKey = "atlas.principal"
	tokenContextKey     = "atlas.session-token"
)

func authenticate(service *authservice.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawToken, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			writeError(c, http.StatusUnauthorized, "unauthorized", "Authentication is required.", "")
			return
		}

		principal, err := service.Authenticate(c.Request.Context(), rawToken)
		if errors.Is(err, authservice.ErrUnauthorized) {
			writeError(c, http.StatusUnauthorized, "unauthorized", "The session is invalid or expired.", "")
			return
		}
		if err != nil {
			_ = c.Error(err)
			writeError(c, http.StatusInternalServerError, "internal_error", "The server could not complete the request.", "")
			return
		}

		c.Set(principalContextKey, principal)
		c.Set(tokenContextKey, rawToken)
		c.Next()
	}
}

// requireRole is ready for future admin-only routes such as operator invitations.
// Authentication must run before this middleware so the role is server-derived.
func requireRole(allowed ...models.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := currentPrincipal(c)
		if !ok {
			writeError(c, http.StatusUnauthorized, "unauthorized", "Authentication is required.", "")
			return
		}
		for _, role := range allowed {
			if principal.User.Role == role {
				c.Next()
				return
			}
		}
		writeError(c, http.StatusForbidden, "forbidden", "Your role does not permit this action.", "")
	}
}

func bearerToken(header string) (string, bool) {
	scheme, value, ok := strings.Cut(strings.TrimSpace(header), " ")
	return value, ok && strings.EqualFold(scheme, "Bearer") && value != ""
}

func currentPrincipal(c *gin.Context) (models.Principal, bool) {
	value, ok := c.Get(principalContextKey)
	if !ok {
		return models.Principal{}, false
	}
	principal, ok := value.(models.Principal)
	return principal, ok
}

func currentSessionToken(c *gin.Context) (string, bool) {
	value, ok := c.Get(tokenContextKey)
	if !ok {
		return "", false
	}
	token, ok := value.(string)
	return token, ok
}
