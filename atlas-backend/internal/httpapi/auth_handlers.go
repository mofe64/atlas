package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
	authservice "github.com/sunnyside/atlas/atlas-backend/internal/services/auth"
	"github.com/sunnyside/atlas/atlas-backend/internal/validation"
)

type authHandler struct {
	service *authservice.Service
}

func newAuthHandler(service *authservice.Service) *authHandler {
	return &authHandler{service: service}
}

type registerRequest struct {
	OrganizationName string `json:"organizationName"`
	OrganizationSlug string `json:"organizationSlug"`
	DisplayName      string `json:"displayName"`
	Email            string `json:"email"`
	Password         string `json:"password"`
	DeviceName       string `json:"deviceName"`
}

type loginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceName string `json:"deviceName"`
}

type organizationResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type userResponse struct {
	ID          string      `json:"id"`
	DisplayName string      `json:"displayName"`
	Email       string      `json:"email"`
	Role        models.Role `json:"role"`
}

type authResponse struct {
	SessionToken string               `json:"sessionToken,omitempty"`
	ExpiresAt    *time.Time           `json:"expiresAt,omitempty"`
	User         userResponse         `json:"user"`
	Organization organizationResponse `json:"organization"`
}

type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func (h *authHandler) register(c *gin.Context) {
	var request registerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON.", "")
		return
	}

	result, err := h.service.Register(c.Request.Context(), authservice.RegistrationInput{
		OrganizationName: request.OrganizationName,
		OrganizationSlug: request.OrganizationSlug,
		DisplayName:      request.DisplayName,
		Email:            request.Email,
		Password:         request.Password,
		DeviceName:       request.DeviceName,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusCreated, responseFromResult(result, true))
}

func (h *authHandler) login(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_json", "Request body must be valid JSON.", "")
		return
	}

	result, err := h.service.Login(c.Request.Context(), authservice.LoginInput{
		Email: request.Email, Password: request.Password, DeviceName: request.DeviceName,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, responseFromResult(result, true))
}

func (h *authHandler) me(c *gin.Context) {
	principal, ok := currentPrincipal(c)
	if !ok {
		writeError(c, http.StatusInternalServerError, "internal_error", "The server could not complete the request.", "")
		return
	}
	c.JSON(http.StatusOK, responseFromPrincipal(principal))
}

func (h *authHandler) logout(c *gin.Context) {
	rawToken, ok := currentSessionToken(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "Authentication is required.", "")
		return
	}
	if err := h.service.Logout(c.Request.Context(), rawToken); err != nil {
		_ = c.Error(err)
		writeError(c, http.StatusInternalServerError, "internal_error", "The server could not complete the request.", "")
		return
	}
	c.Status(http.StatusNoContent)
}

func responseFromResult(result authservice.Result, includeToken bool) authResponse {
	response := responseFromPrincipal(models.Principal{User: result.User, Organization: result.Organization})
	if includeToken {
		response.SessionToken = result.SessionToken
		response.ExpiresAt = &result.ExpiresAt
	}
	return response
}

func responseFromPrincipal(principal models.Principal) authResponse {
	return authResponse{
		User: userResponse{
			ID: principal.User.ID, DisplayName: principal.User.DisplayName,
			Email: principal.User.Email, Role: principal.User.Role,
		},
		Organization: organizationResponse{
			ID: principal.Organization.ID, Name: principal.Organization.Name, Slug: principal.Organization.Slug,
		},
	}
}

func writeAuthError(c *gin.Context, err error) {
	var validationError *validation.Error
	switch {
	case errors.As(err, &validationError):
		writeErrorWithReason(
			c,
			http.StatusBadRequest,
			"validation_failed",
			string(validationError.Code),
			validationError.Message,
			validationError.Field,
		)
	case errors.Is(err, repositories.ErrConflict):
		writeError(c, http.StatusConflict, "registration_conflict", "That organization slug or email is already registered.", "")
	case errors.Is(err, authservice.ErrInvalidCredentials):
		writeError(c, http.StatusUnauthorized, "invalid_credentials", "Invalid email or password.", "")
	default:
		_ = c.Error(err)
		writeError(c, http.StatusInternalServerError, "internal_error", "The server could not complete the request.", "")
	}
}

func writeError(c *gin.Context, status int, code, message, field string) {
	writeErrorWithReason(c, status, code, "", message, field)
}

func writeErrorWithReason(c *gin.Context, status int, code, reason, message, field string) {
	c.AbortWithStatusJSON(status, apiError{Error: apiErrorBody{
		Code: code, Reason: reason, Message: message, Field: field,
	}})
}
