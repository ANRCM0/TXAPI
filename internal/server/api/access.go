package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type sessionUserContextKey struct{}

type loginBucket struct {
	count int
	until time.Time
}
type loginGate struct {
	mu      sync.Mutex
	buckets map[string]loginBucket
	sweep   time.Time
}

func (g *loginGate) allow(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.buckets == nil {
		g.buckets = map[string]loginBucket{}
	}
	if now.After(g.sweep) {
		for k, b := range g.buckets {
			if now.After(b.until) {
				delete(g.buckets, k)
			}
		}
		g.sweep = now.Add(time.Minute)
	}
	b, exists := g.buckets[ip]
	if !exists && len(g.buckets) >= 4096 {
		return false
	}
	if now.After(b.until) {
		b = loginBucket{until: now.Add(time.Minute)}
	}
	if b.count >= 5 {
		return false
	}
	b.count++
	g.buckets[ip] = b
	return true
}
func bearer(c *gin.Context) string {
	header := c.GetHeader("Authorization")
	if len(header) < 7 || !strings.EqualFold(header[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}
func sessionUser(c *gin.Context) *model.User {
	u, _ := c.Request.Context().Value(sessionUserContextKey{}).(*model.User)
	return u
}
func (h *Handler) session(admin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := h.store.SessionUser(bearer(c))
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"error": gin.H{"message": "valid login session required", "type": "unauthorized"}})
			return
		}
		if admin && user.Role != model.RoleAdmin {
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
	admin := r.Group("/api/admin", h.session(true))
	admin.GET("/config-status", h.ConfigurationStatus)
	admin.POST("/config-status/apply", h.configurationWrite(), h.ApplyConfiguration)
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
	h.registerResources(admin)
}
func (h *Handler) Login(c *gin.Context) {
	// RemoteAddr is used, not untrusted X-Forwarded-For, for this bounded gate.
	ip := c.Request.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if !h.loginGate.allow(ip) {
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
	if len(in.Username) > 64 || len(in.Password) > 72 {
		c.JSON(401, gin.H{"error": gin.H{"message": "invalid username or password"}})
		return
	}
	secret, user, err := h.store.Login(in.Username, in.Password)
	if err != nil {
		c.JSON(401, gin.H{"error": gin.H{"message": "invalid username or password"}})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"session_token": secret, "token_type": "Bearer", "expires_in": 86400, "user": user})
}
func (h *Handler) Logout(c *gin.Context) {
	if err := h.store.Logout(bearer(c)); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *Handler) Users(c *gin.Context) {
	users, err := h.store.ListUsers()
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
	user, err := h.store.GetUser(id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": user})
}
func (h *Handler) CreateUser(c *gin.Context) {
	var in store.UserInput
	if !bindJSON(c, &in) {
		return
	}
	user, err := h.store.CreateUser(&in)
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
	var in store.UserInput
	if !bindJSON(c, &in) {
		return
	}
	user, err := h.store.UpdateUser(id, &in)
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
	token, err := h.store.ClaimToken(id, in.UserID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": token})
}
func (h *Handler) MyKeys(c *gin.Context) {
	tokens, err := h.store.OwnerTokens(sessionUser(c).ID)
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
	token, err := h.store.OwnedToken(sessionUser(c).ID, id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": token})
}
func (h *Handler) CreateMyKey(c *gin.Context) {
	var in store.TokenInput
	if !bindJSON(c, &in) {
		return
	}
	owner := sessionUser(c).ID
	if in.UserID != 0 && in.UserID != owner {
		c.JSON(403, gin.H{"error": gin.H{"message": "cannot create a key for another user"}})
		return
	}
	token, err := h.store.IssueToken(owner, &in)
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
	var in store.TokenInput
	if !bindJSON(c, &in) {
		return
	}
	token, err := h.store.UpdateOwnedToken(sessionUser(c).ID, id, &in)
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
	if err := h.store.DeleteOwnedToken(sessionUser(c).ID, id); err != nil {
		fail(c, err)
		return
	}
	c.Status(204)
}
func (h *Handler) MyModels(c *gin.Context) {
	system, err := h.store.SystemPolicy()
	if err != nil {
		fail(c, err)
		return
	}
	policy := access.Intersect(system, sessionUser(c).Policy())
	seen := map[string]bool{}
	models := []string{}
	for _, ch := range h.store.Channels() {
		if !ch.IsEnabled() {
			continue
		}
		candidates := append([]string(nil), ch.Models...)
		for _, name := range ch.Models {
			if name == "*" {
				candidates = append(candidates, policy.Models...)
				break
			}
		}
		for _, m := range candidates {
			if m != "*" && !seen[m] && policy.Allows(m) {
				models = append(models, m)
				seen[m] = true
			}
		}
	}
	c.JSON(200, gin.H{"data": models, "policy": policy})
}
func (h *Handler) MyLogs(c *gin.Context) {
	logs, err := h.store.ListLogs(store.LogQuery{UserID: sessionUser(c).ID, OwnerOnly: true, Limit: queryInt(c, "limit", 100), Offset: queryInt(c, "offset", 0)})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": logs})
}
func (h *Handler) MyUsage(c *gin.Context) {
	user := sessionUser(c)
	usage, err := h.store.UsageForUser(user.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": gin.H{"user_id": user.ID, "quota": user.Quota, "used_quota": user.UsedQuota, "usage": usage}})
}
func (h *Handler) SystemPolicy(c *gin.Context) {
	p, err := h.store.SystemPolicy()
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(200, gin.H{"data": p})
}
func (h *Handler) SetSystemPolicy(c *gin.Context) {
	var p access.Policy
	if !bindJSON(c, &p) {
		return
	}
	if err := h.store.SetSystemPolicy(p); err != nil {
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
