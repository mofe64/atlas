package httpapi

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type rateWindow struct {
	startedAt time.Time
	requests  int
}

// fixedWindowRateLimit is deliberately small and process-local. It protects a
// single-instance deployment from basic password spraying; a multi-replica
// production deployment should enforce a shared limit at its gateway or Redis.
func fixedWindowRateLimit(limit int, window time.Duration) gin.HandlerFunc {
	var mutex sync.Mutex
	clients := make(map[string]rateWindow)
	requestCount := 0

	return func(c *gin.Context) {
		now := time.Now()
		key := c.ClientIP()

		mutex.Lock()
		requestCount++
		// Remove inactive IPs periodically so untrusted clients cannot grow this
		// process-local map forever by continually presenting new addresses.
		if requestCount%100 == 0 {
			for client, existing := range clients {
				if now.Sub(existing.startedAt) >= window {
					delete(clients, client)
				}
			}
		}
		current := clients[key]
		if current.startedAt.IsZero() || now.Sub(current.startedAt) >= window {
			current = rateWindow{startedAt: now}
		}
		if current.requests >= limit {
			mutex.Unlock()
			c.Header("Retry-After", strconv.Itoa(max(1, int(window.Seconds()))))
			writeError(c, http.StatusTooManyRequests, "rate_limited", "Too many authentication attempts. Try again later.", "")
			return
		}
		current.requests++
		clients[key] = current
		mutex.Unlock()

		c.Next()
	}
}
