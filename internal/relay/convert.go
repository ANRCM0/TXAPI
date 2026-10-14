package relay

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
	"net/http"
)

func convertRequestBody(a adaptor.Adaptor, b []byte, m *meta.Meta) ([]byte, error) {
	return protocol.ConvertRequestBody(a, b, m)
}
func convertResponseBody(a adaptor.Adaptor, b []byte, m *meta.Meta) ([]byte, *model.Usage, error) {
	return protocol.ConvertResponseBody(a, b, m)
}
func convertStreamLine(a adaptor.Adaptor, line []byte, s adaptor.StreamSession, m *meta.Meta) ([][]byte, error) {
	return protocol.ConvertStreamLine(a, line, s, m)
}

// Temporary bridge for legacy tests; normal production attempts use core.Execute.
type responseTransport struct{ response *http.Response }

func (t responseTransport) RoundTrip(*http.Request) (*http.Response, error) { return t.response, nil }
func relayStream(c *gin.Context, a adaptor.Adaptor, resp *http.Response, m *meta.Meta) (*model.Usage, error) {
	e := core.New(core.Options{Client: &http.Client{Transport: responseTransport{response: resp}}})
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	result := e.Execute(ctx, c.Writer, core.Input{URL: "http://legacy-response.test", Stream: true, Decoder: protocol.NewDecoder(a, m)})
	if result.Error != nil {
		return result.Usage, errors.New(result.Error.Error.Message)
	}
	return result.Usage, nil
}
