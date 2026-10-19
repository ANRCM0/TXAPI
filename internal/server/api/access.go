package api

import (
	"context"
	"errors"
	control "minigateway/internal/foundation/contracts/control/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type sessionUserContextKey struct{}

func bearer(c *gin.Context) string {
	header := c.GetHeader("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}
func sessionUser(c *gin.Context) *control.User {
	u, _ := c.Request.Context().Value(sessionUserContextKey{}).(*control.User)
	return u
}
func (h *Handler) session(admin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := h.control.SessionUser(bearer(c))
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"error": gin.H{"message": "valid login session required", "type": "unauthorized"}})
			return
		}
		if admin && user.Role != control.RoleAdmin {
			c.AbortWithStatusJSON(403, gin.H{"error": gin.H{"message": "administrator required", "type": "forbidden"}})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), sessionUserContextKey{}, user))
		c.Next()
	}
}
func (h *Handler) RegisterAccess(r *gin.RouterGroup) {
	auth := r.Group("/api/auth")
	auth.POST("/login", h.Login)
	auth.POST("/logout", h.session(false), h.Logout)
	me := r.Group("/api/me", h.session(false))
	me.GET("", func(c *gin.Context) { c.JSON(200, gin.H{"data": sessionUser(c)}) })
	me.GET("/keys", h.MyKeys)
	me.POST("/keys", h.CreateMyKey)
	me.GET("/keys/:id", h.MyKey)
	me.PUT("/keys/:id", h.UpdateMyKey)
	me.DELETE("/keys/:id", h.DeleteMyKey)
	me.GET("/models", h.MyModels)
	me.GET("/logs", h.MyLogs)
	me.GET("/usage", h.MyUsage)
	me.GET("/background", h.taskList(false))
	me.GET("/routing", h.routeList(false))
	me.GET("/routing/:id", h.routeRead(false))
	me.GET("/background/:id", h.taskRead(false))
	me.POST("/background/:id/cancel", h.taskCancel)
	admin := r.Group("/api/admin", h.session(true))
	admin.GET("/background", h.taskList(true))
	admin.GET("/routing", h.routeList(true))
	admin.GET("/routing/:id", h.routeRead(true))
	admin.GET("/background/:id", h.taskRead(true))
	admin.GET("/config-status", h.ConfigurationStatus)
	admin.POST("/config-status/apply", h.ApplyConfiguration)
	admin.GET("/users", h.Users)
	admin.POST("/users", h.CreateUser)
	admin.GET("/users/:id", h.User)
	admin.PUT("/users/:id", h.UpdateUser)
	admin.GET("/keys", h.ListTokens)
	admin.POST("/keys", h.CreateToken)
	admin.PUT("/keys/:id", h.UpdateToken)
	admin.DELETE("/keys/:id", h.DeleteToken)
	admin.POST("/keys/:id/claim", h.ClaimKey)
	admin.GET("/model-policy", h.SystemPolicy)
	admin.PUT("/model-policy", h.SetSystemPolicy)
	admin.GET("/billing/reservations", h.BillingReservations)
	admin.GET("/billing/reservations/:id", h.BillingReservation)
	admin.POST("/billing/reservations/:id/resolve", h.ResolveBillingReservation)
	h.registerResources(admin)
}
func (h *Handler) Login(c *gin.Context) {
	// RemoteAddr is used, not untrusted X-Forwarded-For, for this bounded gate.
	ip := c.Request.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if !h.control.AllowLogin(ip) {
		c.Header("Retry-After", "60")
		c.JSON(429, gin.H{"error": gin.H{"message": "too many login attempts"}})
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bindJSON(c, &in) {
		return
	}
	secret, user, err := h.control.Login(in.Username, in.Password)
	if err != nil {
		c.JSON(401, gin.H{"error": gin.H{"message": "invalid username or password"}})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"session_token": secret, "token_type": "Bearer", "expires_in": 86400, "user": user})
}
func (h *Handler) Logout(c *gin.Context) {
	if err := h.control.Logout(bearer(c)); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *Handler) Users(c *gin.Context) {
	users, err := h.control.ListUsers()
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": users})
}
func (h *Handler) User(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	user, err := h.control.GetUser(id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": user})
}
func (h *Handler) CreateUser(c *gin.Context) {
	var in control.UserInput
	if !bindJSON(c, &in) {
		return
	}
	user, err := h.control.CreateUser(&in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(201, gin.H{"data": user})
}
func (h *Handler) UpdateUser(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in control.UserInput
	if !bindJSON(c, &in) {
		return
	}
	user, err := h.control.UpdateUser(id, &in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": user})
}
func (h *Handler) ClaimKey(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in struct {
		UserID uint `json:"user_id"`
	}
	if !bindJSON(c, &in) {
		return
	}
	token, err := h.control.ClaimToken(id, in.UserID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": token})
}
func (h *Handler) MyKeys(c *gin.Context) {
	tokens, err := h.control.OwnerTokens(sessionUser(c).ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": tokens})
}
func (h *Handler) MyKey(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	token, err := h.control.OwnedToken(sessionUser(c).ID, id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": token})
}
func (h *Handler) CreateMyKey(c *gin.Context) {
	var in control.TokenInput
	if !bindJSON(c, &in) {
		return
	}
	owner := sessionUser(c).ID
	token, err := h.control.CreateOwnedKey(owner, &in)
	if errors.Is(err, control.ErrForbidden) {
		c.JSON(403, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(201, gin.H{"data": token})
}
func (h *Handler) UpdateMyKey(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in control.TokenInput
	if !bindJSON(c, &in) {
		return
	}
	token, err := h.control.UpdateOwnedToken(sessionUser(c).ID, id, &in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": token})
}
func (h *Handler) DeleteMyKey(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.control.DeleteOwnedToken(sessionUser(c).ID, id); err != nil {
		fail(c, err)
		return
	}
	c.Status(204)
}
func (h *Handler) MyModels(c *gin.Context) {
	result, err := h.control.ModelsForPolicy(sessionUser(c).Policy())
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": result.Models, "policy": result.Policy})
}
func (h *Handler) MyLogs(c *gin.Context) {
	logs, err := h.control.ListLogs(control.LogQuery{UserID: sessionUser(c).ID, OwnerOnly: true, Limit: queryInt(c, "limit", 100), Offset: queryInt(c, "offset", 0)})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": logs})
}
func (h *Handler) MyUsage(c *gin.Context) {
	user := sessionUser(c)
	usage, err := h.control.UsageForUser(user.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": gin.H{"user_id": user.ID, "quota": user.Quota, "used_quota": user.UsedQuota, "usage": usage}})
}
func (h *Handler) SystemPolicy(c *gin.Context) {
	p, err := h.control.SystemPolicy()
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": p})
}
func (h *Handler) SetSystemPolicy(c *gin.Context) {
	var p identity.Policy
	if !bindJSON(c, &p) {
		return
	}
	if err := h.control.SetSystemPolicy(p); err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": p})
}

func queryInt(c *gin.Context, name string, fallback int) int {
	v, err := strconv.Atoi(c.Query(name))
	if err != nil || v < 0 {
		return fallback
	}
	return v
}
