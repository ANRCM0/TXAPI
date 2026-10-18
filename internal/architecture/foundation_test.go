package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func foundationImportError(path, imported string) error {
	const base = "minigateway/internal/"
	const contracts = base + "foundation/contracts/"
	if strings.HasPrefix(path, "foundation/contracts/") {
		if strings.HasPrefix(imported, contracts) {
			return nil
		}
		if strings.HasPrefix(imported, base) || strings.Contains(strings.Split(imported, "/")[0], ".") {
			return fmt.Errorf("contract %s must not expose implementation dependency %s", path, imported)
		}
	}
	owner := strings.Split(path, "/")[0]
	if (owner == "gateway" || owner == "billing" || owner == "generation") &&
		(strings.HasPrefix(imported, base+"protocol") || strings.HasPrefix(imported, "github.com/gin-gonic/")) {
		return fmt.Errorf("framework %s must use registered protocol ports: %s", path, imported)
	}
	if owner == "protocol" && imported == base+"core" {
		return fmt.Errorf("protocol %s must use public execution values", path)
	}
	if owner == "compat" && strings.HasPrefix(imported, base+"foundation/") && !strings.HasPrefix(imported, contracts) && imported != base+"foundation/httpoutput/v1" {
		return fmt.Errorf("compatibility adapter %s must use a versioned public facade: %s", path, imported)
	}
	if owner == "responses" || owner == "management" || owner == "generation" || owner == "access" || owner == "gateway" || owner == "protocol" || owner == "billing" {
		if strings.HasPrefix(imported, base+"foundation/") && !strings.HasPrefix(imported, contracts) {
			return fmt.Errorf("%s must use public foundation contracts, not %s", path, imported)
		}
	}
	return nil
}

func TestFoundationImportGuardRejectsImplementationLeaks(t *testing.T) {
	for _, tc := range []struct {
		file, imported string
		denied         bool
	}{
		{"gateway/relay.go", "minigateway/internal/protocol", true},
		{"gateway/handler.go", "github.com/gin-gonic/gin", true},
		{"billing/request.go", "minigateway/internal/protocol/jsonutil", true},
		{"generation/runner.go", "minigateway/internal/protocol", true},
		{"protocol/decoder.go", "minigateway/internal/core", true},
		{"compat/generationhttp/handler.go", "github.com/gin-gonic/gin", false},
		{"foundation/contracts/execution/v1/api.go", "minigateway/internal/core", true},
		{"foundation/contracts/execution/v1/api.go", "gorm.io/gorm", true},
		{"foundation/contracts/execution/v1/api.go", "minigateway/internal/foundation/runtime", true},
		{"foundation/contracts/execution/v1/api.go", "minigateway/internal/foundation/contracts/identity/v1", false},
		{"foundation/contracts/execution/v1/api.go", "context", false},
		{"responses/service.go", "minigateway/internal/foundation/runtime", true},
		{"management/configuration.go", "minigateway/internal/foundation/persistence", true},
		{"management/configuration.go", "minigateway/internal/foundation/contracts/configuration/v1", false},
		{"responses/service.go", "minigateway/internal/foundation/contracts/execution/v1", false},
		{"compat/httpoutput/output.go", "minigateway/internal/foundation/runtime", true},
		{"compat/httpoutput/output.go", "minigateway/internal/foundation/httpoutput/v1", false},
	} {
		t.Run(tc.file+"/"+tc.imported, func(t *testing.T) {
			if got := foundationImportError(tc.file, tc.imported) != nil; got != tc.denied {
				t.Fatalf("denied=%v, want %v", got, tc.denied)
			}
		})
	}
}

func contractUntyped(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.Ident:
			found = found || n.Name == "any"
		case *ast.InterfaceType:
			found = found || len(n.Methods.List) == 0
		}
		return true
	})
	return found
}

func TestFoundationContractsRejectUntypedEscape(t *testing.T) {
	for _, source := range []string{"package example; type Result struct { Value any }", "package example; type Result interface{}"} {
		file, err := parser.ParseFile(token.NewFileSet(), "bad.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !contractUntyped(file) {
			t.Fatal("untyped public escape was accepted")
		}
	}
}
