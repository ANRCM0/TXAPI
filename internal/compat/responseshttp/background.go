package responseshttp

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	app "minigateway/internal/foundation/contracts/responses/v1"
	appv2 "minigateway/internal/foundation/contracts/responses/v2"
)

type handlerV2 struct {
	service appv2.Service
	output  func(http.ResponseWriter) execution.DeadlineOutput
}

func NewV2(service appv2.Service, output func(http.ResponseWriter) execution.DeadlineOutput) http.Handler {
	if missing(service) || output == nil {
		panic("Responses v2 application and output factory must be supplied")
	}
	return &handlerV2{service, output}
}
func (h *handlerV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	command := parseV2(r)
	out := &delivery{DeadlineOutput: h.output(w), writer: w}
	call, p := h.service.Begin(r.Context(), command.Base.Action)
	if p != nil {
		out.Problem(*p)
		return
	}
	defer call.Close()
	command.Base.Payload = &bodySource{reader: r.Body}
	call.Handle(command, out)
}
func parseV2(r *http.Request) appv2.Command {
	c := appv2.Command{Base: parse(r)}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) == 4 && p[0] == "v1" && p[1] == "responses" && p[3] == "cancel" && r.Method == http.MethodPost {
		c.Base.Action = appv2.CancelResponse
	}
	if c.Base.Action != app.GetResponse {
		return c
	}
	query, e := url.ParseQuery(r.URL.RawQuery)
	c.Base.Page.Invalid = e != nil
	for key, values := range query {
		if len(values) != 1 {
			c.Base.Page.Invalid = true
			continue
		}
		switch key {
		case "stream":
			if values[0] != "true" && values[0] != "false" {
				c.Base.Page.Invalid = true
			}
			c.Stream = values[0] == "true"
		case "starting_after":
			n, e := strconv.ParseUint(values[0], 10, 64)
			if e != nil || n > math.MaxInt64 || strconv.FormatUint(n, 10) != values[0] {
				c.Base.Page.Invalid = true
			} else {
				c.StartingAfter = &n
			}
		default:
			c.Base.Page.Invalid = true
		}
	}
	if c.StartingAfter != nil && !c.Stream {
		c.Base.Page.Invalid = true
	}
	return c
}
