package generationhttp

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"minigateway/internal/compat/httpoutput"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

type Service interface {
	execution.Executor
	execution.ModelCatalog
}

type Handler struct{ service Service }

func New(service Service) *Handler                      { return &Handler{service: service} }
func (h *Handler) HandleChatCompletions(c *gin.Context) { h.execute(c, execution.Chat) }
func (h *Handler) HandleEmbeddings(c *gin.Context)      { h.execute(c, execution.Embeddings) }
func (h *Handler) execute(c *gin.Context, operation execution.Operation) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		code, kind, message := 400, "invalid_request_error", "could not read request body"
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			code, kind, message = 413, "request_too_large", "request body exceeds the size limit"
		}
		c.JSON(code, gin.H{"error": gin.H{"message": message, "type": kind}})
		return
	}
	h.service.Execute(c.Request.Context(), httpoutput.New(c.Writer), execution.Invocation{Operation: operation,
		Body: body, SessionID: c.GetHeader("X-Session-Id")})
}
func (h *Handler) ListModels(c *gin.Context) {
	c.JSON(http.StatusOK, struct {
		Object string            `json:"object"`
		Data   []execution.Model `json:"data"`
	}{Object: "list", Data: h.service.Models(c.Request.Context())})
}
