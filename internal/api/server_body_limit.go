package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func (s *Server) maxRequestBodyBytes() int64 {
	if s == nil {
		return 0
	}
	cfg := s.getConfig()
	if cfg == nil || cfg.MaxRequestBodyBytes <= 0 {
		return 0
	}
	return cfg.MaxRequestBodyBytes
}

func (s *Server) maxRequestBodyBytesMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		maxBytes := s.maxRequestBodyBytes()
		if maxBytes <= 0 || c == nil || c.Request == nil {
			c.Next()
			return
		}
		c.Set(handlers.MaxRequestBodyBytesContextKey, maxBytes)
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "http: request body too large",
			})
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
