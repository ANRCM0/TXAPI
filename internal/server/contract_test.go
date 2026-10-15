package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"minigateway/internal/model"
	"minigateway/internal/server/api"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

type contractOperation struct {
	OperationID string                `json:"operationId"`
	Security    []map[string][]string `json:"security"`
	Responses   map[string]struct {
		Content map[string]struct {
			Schema map[string]any `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}
type apiContract struct {
	Version    string                                  `json:"openapi"`
	Paths      map[string]map[string]contractOperation `json:"paths"`
	Components struct {
		Schemas         map[string]map[string]any  `json:"schemas"`
		SecuritySchemes map[string]json.RawMessage `json:"securitySchemes"`
	} `json:"components"`
}

func loadContract(t *testing.T) *apiContract {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c apiContract
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Version != "3.0.3" {
		t.Fatalf("unexpected contract version %s", c.Version)
	}
	return &c
}

var pathParam = regexp.MustCompile(`:[^/]+`)
var documentedParam = regexp.MustCompile(`\{[^}]+\}`)

func TestOpenAPIContainsEveryRouteAndSecurityBoundary(t *testing.T) {
	contract := loadContract(t)
	engine := newEngine(t, middleware.AdminAuth(testSecret))
	remaining := map[string]bool{}
	seenIDs := map[string]bool{}
	for path, methods := range contract.Paths {
		for method, op := range methods {
			if op.OperationID == "" || seenIDs[op.OperationID] {
				t.Errorf("duplicate or empty operationId at %s %s", method, path)
			}
			seenIDs[op.OperationID] = true
			if len(op.Responses) == 0 {
				t.Errorf("missing responses: %s %s", method, path)
			}
			for _, requirement := range op.Security {
				for scheme := range requirement {
					if _, ok := contract.Components.SecuritySchemes[scheme]; !ok {
						t.Errorf("unknown security scheme %s", scheme)
					}
				}
			}
			remaining[strings.ToUpper(method)+" "+path] = true
		}
	}
	for _, route := range engine.Routes() {
		path := pathParam.ReplaceAllStringFunc(route.Path, func(p string) string { return "{" + p[1:] + "}" })
		key := route.Method + " " + path
		if !remaining[key] {
			t.Errorf("undocumented route %s", key)
			continue
		}
		delete(remaining, key)
		op := contract.Paths[path][strings.ToLower(route.Method)]
		expected := ""
		switch {
		case strings.HasPrefix(path, "/v1/"):
			expected = "CallBearer"
		case strings.HasPrefix(path, "/api/admin") || strings.HasPrefix(path, "/api/me") || path == "/api/auth/logout":
			expected = "SessionBearer"
		case path == "/api/auth/login":
		case strings.HasPrefix(path, "/api/"):
			expected = "AdminKey"
		}
		if expected == "" {
			if len(op.Security) != 0 {
				t.Errorf("public route has security declaration: %s", key)
			}
			continue
		}
		declared := false
		for _, requirement := range op.Security {
			if _, ok := requirement[expected]; ok {
				declared = true
			}
		}
		if !declared {
			t.Errorf("%s requires %s", key, expected)
		}
		// Check actual anonymous behavior rather than trusting documentation.
		url := documentedParam.ReplaceAllString(path, "1")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(route.Method, url, strings.NewReader(`{}`)))
		if rec.Code != 401 {
			t.Errorf("secured route %s allowed anonymous request: %d", key, rec.Code)
		}
	}
	for path := range remaining {
		t.Errorf("contract promises unimplemented route %s", path)
	}
	// Validate all local $refs without adding a runtime spec dependency.
	raw, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	_ = json.Unmarshal(raw, &tree)
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if reference, ok := v["$ref"].(string); ok {
				name := strings.TrimPrefix(reference, "#/components/schemas/")
				if _, exists := contract.Components.Schemas[name]; !exists {
					t.Errorf("unresolved schema %s", reference)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(tree)
}
func TestOpenAPIMetadataFieldsMatchPublicDTOs(t *testing.T) {
	c := loadContract(t)
	cases := map[string]any{"User": model.User{}, "APIKey": model.Token{}, "Channel": model.Channel{}, "Credential": model.Credential{}, "Log": model.RequestLog{}, "Price": model.ModelPrice{}, "Ability": model.Ability{}, "UsageSummary": store.LogSummary{}, "ModelStat": store.ModelStat{}, "ChannelStat": store.ChannelStat{}, "PublicationStatus": api.PublicationStatus{}, "ConfigStatus": api.ConfigStatus{}}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			typ := reflect.TypeOf(value)
			fields := map[string]bool{}
			for i := 0; i < typ.NumField(); i++ {
				tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
				if tag != "" && tag != "-" {
					fields[tag] = true
				}
			}
			if name == "APIKey" {
				delete(fields, "key")
			} // only APIKeyCreated may expose it
			properties, ok := c.Components.Schemas[name]["properties"].(map[string]any)
			if !ok {
				t.Fatal("missing properties")
			}
			for field := range fields {
				if _, ok := properties[field]; !ok {
					t.Errorf("new wire field %s not documented", field)
				}
			}
			for field := range properties {
				if !fields[field] {
					t.Errorf("contract field %s is not implemented", field)
				}
			}
		})
	}
}

// A frontend can consume actual success bodies against these schemas. This is
// deliberately a small OpenAPI subset validator, not a general schema engine.
func assertContractShape(t *testing.T, c *apiContract, schema map[string]any, value any) {
	t.Helper()
	if ref, ok := schema["$ref"].(string); ok {
		assertContractShape(t, c, c.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")], value)
		return
	}
	if all, ok := schema["allOf"].([]any); ok {
		for _, part := range all {
			assertContractShape(t, c, part.(map[string]any), value)
		}
		return
	}
	if value == nil {
		if nullable, _ := schema["nullable"].(bool); !nullable {
			t.Errorf("unexpected null for schema %v", schema)
		}
		return
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			t.Errorf("expected object, got %T", value)
			return
		}
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, exists := object[key.(string)]; !exists {
					t.Errorf("missing field %s", key)
				}
			}
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			for key, child := range props {
				if field, exists := object[key]; exists {
					assertContractShape(t, c, child.(map[string]any), field)
				}
			}
		}
	case "array":
		list, ok := value.([]any)
		if !ok {
			t.Errorf("expected array, got %T", value)
			return
		}
		for _, element := range list {
			assertContractShape(t, c, schema["items"].(map[string]any), element)
		}
	case "string":
		if _, ok := value.(string); !ok {
			t.Errorf("expected string, got %T", value)
		}
	case "integer", "number":
		number, ok := value.(float64)
		if !ok {
			t.Errorf("expected number, got %T", value)
		} else if schema["type"] == "integer" && number != float64(int64(number)) {
			t.Errorf("expected integer, got %f", number)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			t.Errorf("expected boolean, got %T", value)
		}
	}
}
func TestOpenAPIControlPlaneSuccessShapes(t *testing.T) {
	c := loadContract(t)
	f := newAccessFixture(t)
	admin := f.login(t, "admin")
	f.createUser(t, admin, "alice", []string{"m1"})
	session := f.login(t, "alice")
	cases := []struct{ method, path, body, token, status string }{
		{"GET", "/api/me", "", session, "200"},
		{"GET", "/api/me/models", "", session, "200"},
		{"POST", "/api/me/keys", `{"name":"frontend-contract"}`, session, "201"},
		{"GET", "/api/me/keys", "", session, "200"},
		{"GET", "/api/me/logs", "", session, "200"},
		{"GET", "/api/me/usage", "", session, "200"},
		{"GET", "/api/admin/users", "", admin, "200"},
		{"GET", "/api/admin/model-policy", "", admin, "200"},
		{"GET", "/api/admin/channels", "", admin, "200"},
		{"GET", "/api/admin/config-status", "", admin, "200"},
		{"POST", "/api/admin/config-status/apply", "", admin, "200"},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			code, _, raw := f.request(t, tc.method, tc.path, tc.body, tc.token)
			if code != 200 && code != 201 {
				t.Fatalf("status=%d body=%s", code, raw)
			}
			var value any
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				t.Fatal(err)
			}
			operation := c.Paths[tc.path][strings.ToLower(tc.method)]
			assertContractShape(t, c, operation.Responses[tc.status].Content["application/json"].Schema, value)
			if strings.Contains(raw, "key_hash") || strings.Contains(raw, "password_hash") {
				t.Fatal("secret hash escaped public DTO")
			}
		})
	}
}
