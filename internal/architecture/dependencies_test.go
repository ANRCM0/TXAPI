// Package architecture keeps module boundaries executable for independent teams.
package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestProductionDependencyBoundaries(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
	allowed := map[string]map[string]bool{
		"access": {"foundation": true}, "core": {}, "config": {}, "logging": {}, "metrics": {}, "secrets": {},
		"model":        {"access": true, "foundation": true},
		"protocol":     {"protocol": true, "foundation": true},
		"scheduler":    {"model": true, "scheduler": true},
		"billing":      {"model": true, "foundation": true, "billing": true},
		"store":        {"foundation": true, "model": true, "access": true, "config": true, "store": true, "secrets": true, "generation": true},
		"management":   {"foundation": true},
		"controlplane": {"foundation": true},
		"examples":     {"foundation": true},
		"generation":   {"access": true, "foundation": true, "secrets": true, "generation": true},
		"responses":    {"foundation": true, "compat": true},
		"responsesws":  {"foundation": true},
		"taskconsole":  {"foundation": true},
		"routeconsole": {"foundation": true},
		"quoteconsole": {"foundation": true},
		"transport":    {"transport": true},
		"compat":       {"foundation": true, "compat": true, "protocol": true, "transport": true},
		"foundation":   {"store": true, "foundation": true, "access": true, "core": true, "gateway": true, "model": true},
		"gateway":      {"access": true, "core": true, "model": true, "billing": true, "scheduler": true, "metrics": true, "gateway": true, "foundation": true, "generation": true},
		"server":       {"foundation": true, "compat": true, "protocol": true, "server": true},
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		owner := strings.Split(filepath.ToSlash(relative), "/")[0]
		rules, ok := allowed[owner]
		if !ok {
			t.Errorf("register module ownership before adding %s", relative)
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if err := foundationImportError(filepath.ToSlash(relative), imported); err != nil {
				t.Error(err)
			}
			if strings.HasPrefix(imported, "minigateway/internal/") {
				target := strings.Split(strings.TrimPrefix(imported, "minigateway/internal/"), "/")[0]
				if !rules[target] {
					t.Errorf("%s must not depend on internal/%s", relative, target)
				}
			}
			if (owner == "core" || owner == "secrets") && strings.Contains(strings.Split(imported, "/")[0], ".") {
				t.Errorf("%s must use only standard library: %s", owner, imported)
			}
			if strings.HasPrefix(imported, "github.com/coder/websocket") && !strings.HasPrefix(filepath.ToSlash(relative), "transport/websocket/") {
				t.Errorf("websocket library escaped transport boundary: %s", relative)
			}
			if owner != "server" && owner != "compat" && strings.HasPrefix(imported, "github.com/gin-gonic/") {
				t.Errorf("framework dependency escaped HTTP boundary: %s", relative)
			}
			if owner != "store" && owner != "billing" && owner != "model" && (strings.HasPrefix(imported, "gorm.io/") || strings.Contains(imported, "sqlite")) {
				t.Errorf("database dependency escaped persistence/billing: %s", relative)
			}
			if owner == "gateway" && (imported == "minigateway/internal/billing" || imported == "minigateway/internal/protocol/model") {
				alias := filepath.Base(imported)
				if spec.Name != nil {
					alias = spec.Name.Name
				}
				if alias == "." {
					t.Errorf("gateway consumer boundaries require named imports: %s", relative)
				}
				ast.Inspect(file, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, ok := selector.X.(*ast.Ident)
					if ok && pkg.Name == alias && (imported == "minigateway/internal/billing" && selector.Sel.Name == "Engine" || imported == "minigateway/internal/protocol/model" && selector.Sel.Name == "ModelRequest" && entry.Name() != "handler.go") {
						t.Errorf("gateway must consume operation envelopes and billing ports, not %s.%s: %s", alias, selector.Sel.Name, relative)
					}
					return true
				})
			}
		}
		if strings.HasPrefix(filepath.ToSlash(relative), "foundation/contracts/") && contractUntyped(file) {
			t.Errorf("foundation contract uses untyped escape: %s", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
