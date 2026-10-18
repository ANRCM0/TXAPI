package architecture

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var recordFoundationAPI = flag.String("record-foundation-api", "", "export the reviewed foundation API snapshot to this file")

type typeBoundary struct {
	allowed func(string) bool
	seen    map[types.Type]bool
}

func (b *typeBoundary) check(t types.Type) error {
	if t == nil || b.seen[t] {
		return nil
	}
	b.seen[t] = true
	checkObject := func(obj *types.TypeName) (bool, error) {
		if obj.Pkg() == nil {
			return false, nil
		}
		path := obj.Pkg().Path()
		if standard(path) {
			return true, nil
		}
		if !b.allowed(path) {
			return true, fmt.Errorf("implementation type %s.%s is reachable", path, obj.Name())
		}
		return false, nil
	}
	checkParams := func(list *types.TypeParamList) error {
		for i := 0; i < list.Len(); i++ {
			if err := b.check(list.At(i)); err != nil {
				return err
			}
		}
		return nil
	}
	checkArgs := func(list *types.TypeList) error {
		for i := 0; i < list.Len(); i++ {
			if err := b.check(list.At(i)); err != nil {
				return err
			}
		}
		return nil
	}
	switch t := t.(type) {
	case *types.Alias:
		if err := checkArgs(t.TypeArgs()); err != nil {
			return err
		}
		if stop, err := checkObject(t.Obj()); stop || err != nil {
			return err
		}
		if err := checkParams(t.TypeParams()); err != nil {
			return err
		}
		return b.check(types.Unalias(t))
	case *types.Named:
		if err := checkArgs(t.TypeArgs()); err != nil {
			return err
		}
		if stop, err := checkObject(t.Obj()); stop || err != nil {
			return err
		}
		if err := checkParams(t.TypeParams()); err != nil {
			return err
		}
		for i := 0; i < t.NumMethods(); i++ {
			if err := b.check(t.Method(i).Type()); err != nil {
				return err
			}
		}
		return b.check(t.Underlying())
	case *types.Pointer:
		return b.check(t.Elem())
	case *types.Slice:
		return b.check(t.Elem())
	case *types.Array:
		return b.check(t.Elem())
	case *types.Map:
		if err := b.check(t.Key()); err != nil {
			return err
		}
		return b.check(t.Elem())
	case *types.Chan:
		return b.check(t.Elem())
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if err := b.check(t.Field(i).Type()); err != nil {
				return fmt.Errorf("field %s: %w", t.Field(i).Name(), err)
			}
		}
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if err := b.check(t.At(i).Type()); err != nil {
				return err
			}
		}
	case *types.Signature:
		if err := checkParams(t.RecvTypeParams()); err != nil {
			return err
		}
		if err := checkParams(t.TypeParams()); err != nil {
			return err
		}
		if t.Recv() != nil {
			if err := b.check(t.Recv().Type()); err != nil {
				return err
			}
		}
		if err := b.check(t.Params()); err != nil {
			return err
		}
		return b.check(t.Results())
	case *types.Interface:
		t.Complete()
		if t.NumMethods() == 0 && t.NumEmbeddeds() == 0 {
			return fmt.Errorf("untyped interface escape")
		}
		for i := 0; i < t.NumMethods(); i++ {
			if err := b.check(t.Method(i).Type()); err != nil {
				return err
			}
		}
		for i := 0; i < t.NumEmbeddeds(); i++ {
			if err := b.check(t.EmbeddedType(i)); err != nil {
				return err
			}
		}
	case *types.TypeParam:
		return b.check(t.Constraint())
	case *types.Union:
		for i := 0; i < t.Len(); i++ {
			if err := b.check(t.Term(i).Type()); err != nil {
				return err
			}
		}
	case *types.Basic:
		if t.Kind() == types.UnsafePointer {
			return fmt.Errorf("unsafe pointer escape")
		}
	}
	return nil
}

func visibleTypeError(t types.Type, allowed func(string) bool) error {
	return (&typeBoundary{allowed: allowed, seen: map[types.Type]bool{}}).check(t)
}

func TestVersionedPublicSignaturesHideImplementationTypes(t *testing.T) {
	rules := packageRules(t)
	cmd := exec.Command("go", "list", "-deps", "-export", "-json", "./...")
	cmd.Dir = repositoryRoot()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("load compiler export data: %v\n%s", err, &stderr)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	exports := map[string]string{}
	for dec.More() {
		var pkg struct{ ImportPath, Export string }
		if err := dec.Decode(&pkg); err != nil {
			t.Fatal(err)
		}
		exports[pkg.ImportPath] = pkg.Export
	}
	imp := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})
	allowed := func(path string) bool { return rules[path].Public }
	paths := []string{}
	for path, r := range rules {
		if r.Public || r.Layer == "L0" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	api := map[string][]string{}
	qualifier := func(pkg *types.Package) string { return pkg.Path() }
	for _, path := range paths {
		t.Run(strings.TrimPrefix(path, module), func(t *testing.T) {
			pkg, err := imp.Import(path)
			if err != nil {
				t.Fatal(err)
			}
			version, ok := pkg.Scope().Lookup("Version").(*types.Const)
			if !ok || version.Val().Kind() != constant.String || constant.StringVal(version.Val()) != rules[path].Version {
				t.Fatal("package Version differs from registered contract version")
			}
			for _, name := range pkg.Scope().Names() {
				obj := pkg.Scope().Lookup(name)
				if obj.Exported() {
					if rules[path].Public {
						if err := visibleTypeError(obj.Type(), allowed); err != nil {
							t.Errorf("%s: %v", name, err)
						}
					}
					declaration := types.ObjectString(obj, qualifier)
					if c, ok := obj.(*types.Const); ok {
						declaration += " = " + c.Val().ExactString()
					}
					api[path] = append(api[path], declaration)
					if obj, ok := obj.(*types.TypeName); ok && !obj.IsAlias() {
						if named, ok := obj.Type().(*types.Named); ok {
							for i := 0; i < named.NumMethods(); i++ {
								if method := named.Method(i); method.Exported() {
									api[path] = append(api[path], types.ObjectString(method, qualifier))
								}
							}
						}
					}
				}
			}
		})
		sort.Strings(api[path])
	}
	if t.Failed() {
		return
	}
	snapshot, err := json.MarshalIndent(api, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	snapshot = append(snapshot, '\n')
	if *recordFoundationAPI != "" {
		if err := os.WriteFile(*recordFoundationAPI, snapshot, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	recorded, err := os.ReadFile(filepath.Join(repositoryRoot(), "internal", "architecture", "public-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(recorded, []byte("\r\n"), []byte("\n")), snapshot) {
		t.Fatal("foundation API differs from reviewed public-v1.json; follow the named proposal and consumer migration process before recording a new snapshot")
	}
}

type fixtureImporter map[string]*types.Package

func (i fixtureImporter) Import(path string) (*types.Package, error) {
	if pkg := i[path]; pkg != nil {
		return pkg, nil
	}
	return nil, fmt.Errorf("unknown fixture import %s", path)
}

func checkedFixture(t *testing.T, path, source string, imp types.Importer) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{Importer: imp}).Check(path, fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestRecursiveVisibilityGuardRejectsTypeCheckedEscapes(t *testing.T) {
	for _, private := range []string{module + "internal/core", "gorm.io/gorm"} {
		pkg := checkedFixture(t, private, "package hidden; type Hidden struct { Value int }", nil)
		wrapper := checkedFixture(t, "sync/atomic", "package atomic; type Pointer[T any] struct{}", nil)
		for _, decl := range []string{
			"type Public = hidden.Hidden",
			"type Public struct { Nested map[string][]*hidden.Hidden }",
			"type Public struct { private func() (chan *hidden.Hidden) }",
			"type inner struct { secret hidden.Hidden }; type Public struct { Inner inner }",
			"type Public interface { Read() hidden.Hidden }",
			"type Box[T interface{ ~int }] struct { Value T; Secret hidden.Hidden }; type Public = Box[int]",
			"type Public struct{}; func (*Public) Leak() hidden.Hidden { panic(0) }",
			"type Public struct { Value atomic.Pointer[hidden.Hidden] }",
		} {
			t.Run(private+"/"+decl, func(t *testing.T) {
				path := module + "internal/foundation/contracts/fixture/v1"
				imports := "import hidden " + strconv.Quote(private) + "; "
				if strings.Contains(decl, "atomic.") {
					imports += "import \"sync/atomic\"; "
				}
				candidate := checkedFixture(t, path, "package fixture; "+imports+decl, fixtureImporter{private: pkg, "sync/atomic": wrapper})
				if visibleTypeError(candidate.Scope().Lookup("Public").Type(), func(p string) bool { return p == path }) == nil {
					t.Fatal("recursive implementation leak accepted")
				}
			})
		}
	}
	for _, decl := range []string{"type Public any", "type Public struct { Callback func(any) }", "type Box[T any] struct { Value T }; type Public = Box[int]"} {
		pkg := checkedFixture(t, module+"internal/foundation/contracts/fixture/v1", "package fixture; "+decl, nil)
		if visibleTypeError(pkg.Scope().Lookup("Public").Type(), func(string) bool { return true }) == nil {
			t.Fatalf("untyped escape accepted: %s", decl)
		}
	}
	path := module + "internal/foundation/contracts/fixture/v1"
	pkg := checkedFixture(t, path, "package fixture; type Box[T interface{ ~int | ~string }] struct { Value T }; type Public = Box[int]", nil)
	if err := visibleTypeError(pkg.Scope().Lookup("Public").Type(), func(p string) bool { return p == path }); err != nil {
		t.Fatalf("typed generic public value rejected: %v", err)
	}
}
