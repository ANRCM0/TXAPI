package gateway

import (
	"net/http"
	"strconv"
	"strings"
)

// This slot is owned by one foreground attempt. It contains validated strings,
// never the borrowed HTTP header map or any credential/account header.
type upstreamResponseMetadata struct {
	status     int
	requestID  string
	retryAfter string
}

func (o *executionOutput) observeUpstreamHeaders(status int, headers http.Header) {
	if o.State().Committed {
		return
	}
	o.upstream = upstreamResponseMetadata{status: status}
	blocked, ok := upstreamConnectionOptions(headers)
	if !ok {
		return
	}
	// Accept exactly one ID source; choosing one of two conflicting sources
	// would conceal ambiguity. Header keys from injected transports may have
	// noncanonical casing, so lookup is explicitly case insensitive.
	xid, xcount := upstreamHeaderValue(headers, "X-Request-Id")
	rid, rcount := upstreamHeaderValue(headers, "Request-Id")
	if xcount+rcount == 1 {
		name, value := "x-request-id", xid
		if rcount == 1 {
			name, value = "request-id", rid
		}
		value = strings.Trim(value, " \t")
		if !blocked[name] && validUpstreamRequestID(value) {
			o.upstream.requestID = strings.Clone(value)
		}
	}
	if (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && !blocked["retry-after"] {
		value, count := upstreamHeaderValue(headers, "Retry-After")
		value = strings.Trim(value, " \t")
		if count == 1 && validUpstreamRetryAfter(value) {
			o.upstream.retryAfter = strings.Clone(value)
		}
	}
}

func (m upstreamResponseMetadata) merge(values map[string]string, status int) {
	if m.requestID != "" {
		values["X-TXAPI-Upstream-Request-ID"] = m.requestID
	}
	if m.retryAfter != "" && status == m.status {
		values["Retry-After"] = m.retryAfter
	}
}

func upstreamHeaderValue(headers http.Header, name string) (string, int) {
	var value string
	count := 0
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			count += len(values)
			if len(values) != 0 {
				value = values[0]
			}
		}
	}
	return value, count
}

func upstreamConnectionOptions(headers http.Header) (map[string]bool, bool) {
	blocked := make(map[string]bool)
	values, size, options := 0, 0, 0
	for name, fields := range headers {
		if !strings.EqualFold(name, "Connection") {
			continue
		}
		for _, field := range fields {
			values++
			size += len(field)
			if values > 16 || size > 4096 {
				return nil, false
			}
			for _, option := range strings.Split(field, ",") {
				options++
				if options > 64 {
					return nil, false
				}
				option = strings.Trim(option, " \t")
				// Invalid nominations cannot be used to bypass exclusion.
				if option == "" || !validHTTPToken(option) {
					return nil, false
				}
				blocked[strings.ToLower(option)] = true
			}
		}
	}
	return blocked, true
}

func validHTTPToken(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return value != ""
}

func validUpstreamRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e || value[i] == ',' {
			return false
		}
	}
	return true
}

func validUpstreamRetryAfter(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	digits := true
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
		if value[i] < '0' || value[i] > '9' {
			digits = false
		}
	}
	if digits {
		_, err := strconv.ParseUint(value, 10, 31)
		return err == nil
	}
	_, err := http.ParseTime(value)
	return err == nil
}
