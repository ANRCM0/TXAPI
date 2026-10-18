package architecture

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const module = "minigateway/"

type packageRule struct {
	Layer   string `json:"layer"`
	Owner   string `json:"owner"`
	Public  bool   `json:"public,omitempty"`
	Version string `json:"version,omitempty"`
}

func repositoryRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func packageRules(t *testing.T) map[string]packageRule {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(), "internal", "architecture", "layers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rules map[string]packageRule
	if err := json.Unmarshal(data, &rules); err != nil {
		t.Fatal(err)
	}
	return rules
}

func standard(path string) bool {
	if strings.HasPrefix(path, module) || strings.Contains(strings.Split(path, "/")[0], ".") {
		return false
	}
	info, err := os.Stat(filepath.Join(runtime.GOROOT(), "src", filepath.FromSlash(path)))
	return err == nil && info.IsDir()
}

func dependencyError(from, to string, rules map[string]packageRule) error {
	r, ok := rules[from]
	if !ok {
		return fmt.Errorf("unclassified package %s", from)
	}
	if standard(to) {
		if r.Layer == "L3" && (to == "net" || strings.HasPrefix(to, "net/") || to == "os" || strings.HasPrefix(to, "os/") ||
			to == "database/sql" || strings.HasPrefix(to, "database/sql/") || to == "crypto/tls" || to == "io/ioutil" ||
			to == "syscall" || to == "unsafe" || to == "plugin") {
			return fmt.Errorf("feature %s must use injected ports instead of %s", from, to)
		}
		return nil
	}
	if r.Layer == "assembly" {
		return nil
	}
	if !strings.HasPrefix(to, module) {
		if r.Layer == "L0" || r.Layer == "L3" || strings.Contains(from, "/foundation/contracts/") {
			return fmt.Errorf("%s cannot import external implementation %s", from, to)
		}
		return nil
	}
	target, ok := rules[to]
	if !ok {
		return fmt.Errorf("%s imports unclassified package %s", from, to)
	}
	allowed := false
	switch r.Layer {
	case "L0":
		// The mechanism has no project dependencies, including other layers.
	case "L1":
		allowed = target.Layer == "L0" || target.Layer == "L1"
		if strings.Contains(from, "/foundation/contracts/") {
			allowed = strings.Contains(to, "/foundation/contracts/") && target.Public
		}
	case "L2":
		allowed = target.Layer == "L2" || target.Layer == "L1" && target.Public
	case "L3":
		allowed = (target.Layer == "L1" || target.Layer == "L2") && target.Public
		// This facade constructs an HTTP writer and is reserved for L2 assembly.
		allowed = allowed && to != module+"internal/foundation/httpoutput/v1"
	}
	if !allowed {
		return fmt.Errorf("%s (%s/%s) must not import %s (%s/%s)", from, r.Layer, r.Owner, to, target.Layer, target.Owner)
	}
	return nil
}

// Codec callbacks can build wire values, but network dispatch belongs to L1.
func codecIOError(path string, file *ast.File) error {
	pure := strings.HasPrefix(path, module+"internal/protocol") || path == module+"internal/compat/responses/v1" ||
		path == module+"internal/compat/summarycodec" || path == module+"internal/compat/responsesws"
	if !pure {
		return nil
	}
	aliases := map[string]string{}
	for _, spec := range file.Imports {
		p, _ := strconv.Unquote(spec.Path.Value)
		alias := filepath.Base(p)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if p == "net" || strings.HasPrefix(p, "net/") && p != "net/http" && p != "net/url" || p == "crypto/tls" || p == "io/ioutil" ||
			p == "os" || strings.HasPrefix(p, "os/") || p == "syscall" || p == "unsafe" {
			return fmt.Errorf("codec %s cannot own external I/O through %s", path, p)
		}
		if alias == "." {
			return fmt.Errorf("codec %s requires named imports", path)
		}
		aliases[alias] = p
	}
	var failure error
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name, ok := selector.X.(*ast.Ident)
		if !ok || aliases[name.Name] != "net/http" {
			return true
		}
		switch selector.Sel.Name {
		case "Client", "Transport", "RoundTripper", "DefaultClient", "DefaultTransport", "Get", "Head", "Post", "PostForm":
			failure = fmt.Errorf("codec %s must not dispatch using http.%s", path, selector.Sel.Name)
		}
		return true
	})
	return failure
}

func TestEveryProductionPackageHasFourLayerOwnership(t *testing.T) {
	rules := packageRules(t)
	seen := map[string]bool{}
	err := filepath.WalkDir(repositoryRoot(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".local" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(repositoryRoot(), filepath.Dir(path))
		pkg := module + filepath.ToSlash(rel)
		seen[pkg] = true
		if _, ok := rules[pkg]; !ok {
			t.Errorf("register exact layer and owner before adding %s", pkg)
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			p, _ := strconv.Unquote(spec.Path.Value)
			if err := dependencyError(pkg, p, rules); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
		if err := codecIOError(pkg, file); err != nil {
			t.Error(err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, r := range rules {
		if !seen[path] || r.Owner == "" || (r.Layer != "L0" && r.Layer != "L1" && r.Layer != "L2" && r.Layer != "L3" && r.Layer != "assembly") {
			t.Errorf("stale or invalid layer registration: %s %+v", path, r)
		}
		if r.Layer == "assembly" && path != module+"cmd/gateway" {
			t.Errorf("only cmd/gateway may assemble production layers: %s", path)
		}
		version := "1"
		if path == module+"internal/foundation/contracts/responses/v2" || path == module+"internal/foundation/contracts/execution/v2" {
			version = "2"
		}
		if r.Public && (r.Version != version || !strings.HasSuffix(path, "/v"+version)) {
			t.Errorf("public package requires versioned registration: %s", path)
		}
		if r.Layer == "L0" && r.Version != "1" {
			t.Errorf("mechanism boundary requires an explicit version: %s", path)
		}
		public := strings.HasPrefix(path, module+"internal/foundation/contracts/") || path == module+"internal/foundation/httpoutput/v1" || path == module+"internal/compat/responses/v1"
		if r.Public != public {
			t.Errorf("public entry must match the reviewed contract inventory: %s", path)
		}
	}
}

func TestFourLayerGuardRejectsForbiddenConsumers(t *testing.T) {
	rules := packageRules(t)
	for _, tc := range []struct{ from, to string }{
		{"internal/core", "internal/foundation/contracts/execution/v1"},
		{"internal/gateway", "internal/protocol"},
		{"internal/gateway", "internal/responses"},
		{"internal/compat/responseshttp", "internal/responses"},
		{"internal/server/api", "internal/store"},
		{"internal/responses", "internal/core"},
		{"internal/responses", "internal/foundation/persistence"},
		{"internal/responses", "internal/controlplane"},
		{"internal/responses", "internal/compat/responseshttp"},
		{"internal/responses", "internal/foundation/httpoutput/v1"},
		{"internal/responses", "internal/generation/backgroundstate"},
		{"internal/compat/responseshttp", "internal/generation/backgroundstate"},
		{"internal/foundation/contracts/state/v1", "internal/model"},
	} {
		t.Run(tc.from+"->"+tc.to, func(t *testing.T) {
			if dependencyError(module+tc.from, module+tc.to, rules) == nil {
				t.Fatal("forbidden production dependency accepted")
			}
		})
	}
	for _, external := range []string{"net/http", "net/url", "os", "os/exec", "database/sql", "gorm.io/gorm", "github.com/coder/websocket"} {
		if dependencyError(module+"internal/responses", external, rules) == nil {
			t.Errorf("feature external I/O accepted: %s", external)
		}
	}
	for _, source := range []string{
		`package codec; import h "net/http"; var client = h.DefaultClient`,
		`package codec; import "net"; var dial = net.Dial`,
		`package codec; import "net/http"; var create = func() *http.Client { return &http.Client{} }`,
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "bad.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if codecIOError(module+"internal/protocol", file) == nil {
			t.Fatal("codec dispatch escape accepted")
		}
	}
}
