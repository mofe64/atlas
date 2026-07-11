package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sunnyside/atlas/atlas-backend/internal/config"
)

func NewRouter(cfg config.Config) (*gin.Engine, error) {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery(), cors(cfg.AllowedOrigins))

	// The service is local by default, so no reverse proxy is trusted implicitly.
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, err
	}

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "atlas-backend",
			"status":  "ok",
		})
	})

	v1 := router.Group("/api/v1")
	v1.GET("/version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "atlas-backend",
			"version": "0.1.0-dev",
		})
	})

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
