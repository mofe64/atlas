package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sunnyside/atlas/atlas-backend/internal/config"
	authservice "github.com/sunnyside/atlas/atlas-backend/internal/services/auth"
)

type readinessChecker interface {
	Ping(context.Context) error
}

func NewRouter(cfg config.Config, authService *authservice.Service, database readinessChecker) (*gin.Engine, error) {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery(), cors(cfg.AllowedOrigins))

	// Trust no proxies by default. A deployed environment may provide only its
	// known ingress/load-balancer addresses through ATLAS_TRUSTED_PROXIES.
	if err := router.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, err
	}

	// Liveness says the process is running. It intentionally does not depend on
	// PostgreSQL, so an orchestrator can distinguish process failure from DB failure.
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "atlas-backend", "status": "ok"})
	})

	// Readiness says this instance can serve real traffic, including database work.
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if database == nil || database.Ping(ctx) != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"service": "atlas-backend", "status": "not_ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"service": "atlas-backend", "status": "ready"})
	})

	v1 := router.Group("/api/v1")
	v1.GET("/version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "atlas-backend", "version": "0.1.0-dev"})
	})

	authHandler := newAuthHandler(authService)
	authRoutes := v1.Group("/auth")
	authRoutes.POST("/register", fixedWindowRateLimit(5, time.Hour), authHandler.register)
	authRoutes.POST("/login", fixedWindowRateLimit(10, time.Minute), authHandler.login)
	authRoutes.GET("/me", authenticate(authService), authHandler.me)
	authRoutes.POST("/logout", authenticate(authService), authHandler.logout)

	return router, nil
}

func cors(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if _, ok := allowed[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Accept,Authorization,Content-Type")
			c.Header("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
