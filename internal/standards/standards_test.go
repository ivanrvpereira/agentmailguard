// Package standards_test checks repository-wide rules that no linter enforces.
package standards_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/client"
)

const (
	root   = "../.."
	module = "github.com/ivanrvpereira/agentmailguard/"
)

type listedPackage struct {
	ImportPath  string
	Imports     []string
	TestGoFiles []string
}

func listPackages(t *testing.T) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []listedPackage
	for d := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listedPackage
		if err := d.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		p.ImportPath = strings.TrimPrefix(p.ImportPath, module)
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list found no packages")
	}
	return pkgs
}

type sourceFile struct {
	path string // relative to the repository root
	file *ast.File
}

// parseSources parses every Go file in the repository on every platform, not only the files the current GOOS builds.
func parseSources(t *testing.T, tests bool) []sourceFile {
	t.Helper()
	var files []sourceFile
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") != tests {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files = append(files, sourceFile{filepath.ToSlash(rel), f})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// importName returns the local name of an import in f, or "" when f does not import it.
func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == path {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return path[strings.LastIndex(path, "/")+1:]
		}
	}
	return ""
}

func isSelector(e ast.Expr, pkg, name string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := s.X.(*ast.Ident)
	return ok && x.Name == pkg && s.Sel.Name == name
}

func TestImportBoundaries(t *testing.T) {
	for _, p := range listPackages(t) {
		for _, imp := range p.Imports {
			dep, internal := strings.CutPrefix(imp, module)
			if !internal {
				continue
			}
			switch {
			case p.ImportPath == "internal/model" || p.ImportPath == "internal/client":
				t.Errorf("%s imports %s: model and client import no other internal package", p.ImportPath, dep)
			case strings.HasPrefix(dep, "internal/providers/") && p.ImportPath != "internal/server" && p.ImportPath != "internal/cli":
				t.Errorf("%s imports %s: only server and cli import provider packages", p.ImportPath, dep)
			}
		}
	}
}

func TestInternalTestFilesOnlyExport(t *testing.T) {
	for _, p := range listPackages(t) {
		for _, f := range p.TestGoFiles {
			if f != "export_test.go" {
				t.Errorf("%s/%s uses the internal test package; use package %s_test, or export_test.go for test-cost hooks", p.ImportPath, f, filepath.Base(p.ImportPath))
			}
		}
	}
}

// Logs could carry mail content, so the standard loggers exist only to discard library output.
func TestLoggersOnlyDiscard(t *testing.T) {
	for _, src := range parseSources(t, false) {
		for _, path := range []string{"log", "log/slog"} {
			name := importName(src.file, path)
			if name == "" {
				continue
			}
			allowed := map[*ast.SelectorExpr]bool{}
			ast.Inspect(src.file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSelector(call.Fun, name, "New") || len(call.Args) == 0 {
					return true
				}
				if (path == "log" && isSelector(call.Args[0], "io", "Discard")) || (path == "log/slog" && isSelector(call.Args[0], name, "DiscardHandler")) {
					allowed[call.Fun.(*ast.SelectorExpr)] = true
					if s, ok := call.Args[0].(*ast.SelectorExpr); ok {
						allowed[s] = true
					}
				}
				return true
			})
			ast.Inspect(src.file, func(n ast.Node) bool {
				if s, ok := n.(*ast.SelectorExpr); ok && isSelector(s, name, s.Sel.Name) && !allowed[s] {
					t.Errorf("%s uses %s.%s; %s appears only to build a discarding logger", src.path, name, s.Sel.Name, path)
				}
				return true
			})
		}
	}
}

// A write must reach the provider at most once. Go's Transport replays a request after a stale keep-alive
// failure when it can rewind the body (GetBody) or when the request carries an Idempotency-Key header.
func TestOutboundRequestsAreSentOnce(t *testing.T) {
	sends := map[string]bool{}
	refusesRedirects := map[string]bool{}
	for _, src := range parseSources(t, false) {
		dir := filepath.Dir(src.path)
		ast.Inspect(src.file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(strings.ToLower(lit.Value), "idempotency-key") {
				t.Errorf("%s mentions an Idempotency-Key header, which makes Go's Transport replay the request", src.path)
			}
			return true
		})
		name := importName(src.file, "net/http")
		if name == "" {
			continue
		}
		ast.Inspect(src.file, func(n ast.Node) bool {
			if a, ok := n.(*ast.AssignStmt); ok && len(a.Lhs) == 1 && len(a.Rhs) == 1 {
				if s, ok := a.Lhs[0].(*ast.SelectorExpr); ok && s.Sel.Name == "CheckRedirect" {
					ast.Inspect(a.Rhs[0], func(n ast.Node) bool {
						if e, ok := n.(ast.Expr); ok && isSelector(e, name, "ErrUseLastResponse") {
							refusesRedirects[dir] = true
						}
						return true
					})
				}
			}
			return true
		})
		for _, decl := range src.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			builds, clears := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if isSelector(n.Fun, name, "NewRequest") || isSelector(n.Fun, name, "NewRequestWithContext") {
						builds = true
					}
				case *ast.AssignStmt:
					if len(n.Lhs) == 1 && len(n.Rhs) == 1 {
						s, ok := n.Lhs[0].(*ast.SelectorExpr)
						nilValue, isIdent := n.Rhs[0].(*ast.Ident)
						if ok && s.Sel.Name == "GetBody" && isIdent && nilValue.Name == "nil" {
							clears = true
						}
					}
				}
				return true
			})
			if builds {
				sends[dir] = true
				if !clears {
					t.Errorf("%s: %s builds a request without setting GetBody = nil", src.path, fn.Name.Name)
				}
			}
		}
	}
	for dir := range sends {
		if !refusesRedirects[dir] {
			t.Errorf("%s sends requests but sets no CheckRedirect that returns ErrUseLastResponse", dir)
		}
	}
}

var addressPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,})`)

// Test data uses reserved domains (RFC 2606, RFC 6761) so that no fixture names a real mailbox.
func TestTestAddressesUseReservedDomains(t *testing.T) {
	reserved := func(domain string) bool {
		domain = strings.ToLower(domain)
		for _, d := range []string{"example.com", "example.net", "example.org"} {
			if domain == d || strings.HasSuffix(domain, "."+d) {
				return true
			}
		}
		for _, tld := range []string{".test", ".example", ".invalid", ".localhost"} {
			if strings.HasSuffix(domain, tld) {
				return true
			}
		}
		return false
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") && !strings.Contains(filepath.ToSlash(path), "/testdata/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range addressPattern.FindAllSubmatchIndex(data, -1) {
			if bytes.HasSuffix(data[:m[0]], []byte("://")) {
				continue // URL userinfo, not a mailbox
			}
			if domain := string(data[m[2]:m[3]]); !reserved(domain) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s: address domain %q is not reserved; use example.test", filepath.ToSlash(rel), domain)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A refusal reaches the user only when every allowlist on its path lists the exact text:
// Guard.audited (mail mutations), server.publicError, then the client's display allowlist.
func TestPublicRefusalsReachTheUser(t *testing.T) {
	guardSrc := packageFiles(t, "internal/guard")
	serverSrc := packageFiles(t, "internal/server")
	guardConsts := stringValues(guardSrc, token.CONST, "GuardError")
	serverVars := stringValues(serverSrc, token.VAR, "errors.New")
	for name, text := range stringValues(serverSrc, token.CONST, "") {
		serverVars[name] = text
	}
	audited := refusals(t, findFunc(t, guardSrc, "audited"), func(e ast.Expr) (string, bool) {
		return guardText(e, guardConsts)
	})
	public := refusals(t, findFunc(t, serverSrc, "publicError"), func(e ast.Expr) (string, bool) {
		if id, ok := e.(*ast.Ident); ok {
			v, ok := serverVars[id.Name]
			return v, ok
		}
		if s, ok := e.(*ast.SelectorExpr); ok && isSelector(s, "guard", s.Sel.Name) {
			v, ok := guardConsts[s.Sel.Name]
			return v, ok
		}
		return guardText(e, nil)
	})
	for _, text := range audited {
		if !slices.Contains(public, text) {
			t.Errorf("Guard.audited passes %q but server.publicError does not", text)
		}
	}
	for _, text := range public {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": text})
		}))
		c, err := client.New(h.URL, client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Call(context.Background(), "mail.list", "work", nil)
		h.Close()
		if err == nil || err.Error() != text {
			t.Errorf("server.publicError passes %q but client.Call shows %v", text, err)
		}
	}
}

func packageFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	var files []*ast.File
	for _, src := range parseSources(t, false) {
		if filepath.Dir(src.path) == dir {
			files = append(files, src.file)
		}
	}
	if len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	return files
}

func findFunc(t *testing.T, files []*ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
				return fn
			}
		}
	}
	t.Fatalf("func %s not found", name)
	return nil
}

// stringValues maps package-level names to the string literal passed to wrap, as in `X = GuardError("text")`.
// An empty wrap maps plain string literals, as in `const X = "text"`.
func stringValues(files []*ast.File, tok token.Token, wrap string) map[string]string {
	values := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != tok {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for i, name := range v.Names {
					if i >= len(v.Values) {
						continue
					}
					if lit, ok := v.Values[i].(*ast.BasicLit); ok && wrap == "" && lit.Kind == token.STRING {
						values[name.Name], _ = strconv.Unquote(lit.Value)
						continue
					}
					call, ok := v.Values[i].(*ast.CallExpr)
					if !ok || len(call.Args) != 1 || exprName(call.Fun) != wrap {
						continue
					}
					if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						values[name.Name], _ = strconv.Unquote(lit.Value)
					}
				}
			}
		}
	}
	return values
}

// exprName renders an identifier or selector chain, such as errors.New.
func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel.Name
	}
	return ""
}

// guardText resolves a string literal, GuardError("text"), or a named GuardError constant.
func guardText(e ast.Expr, consts map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			v, err := strconv.Unquote(e.Value)
			return v, err == nil
		}
	case *ast.CallExpr:
		if exprName(e.Fun) == "GuardError" && len(e.Args) == 1 {
			return guardText(e.Args[0], nil)
		}
	case *ast.Ident:
		v, ok := consts[e.Name]
		return v, ok
	}
	return "", false
}

// refusals collects the texts that a function lets through: every case value, every element of an error list,
// and every named or literal text that a `return text, true` passes on.
// An expression it cannot resolve fails the test, so a new allowlist shape gets a matching resolver.
func refusals(t *testing.T, fn *ast.FuncDecl, resolve func(ast.Expr) (string, bool)) []string {
	t.Helper()
	var out []string
	add := func(e ast.Expr) {
		text, ok := resolve(e)
		if !ok {
			t.Fatalf("%s: cannot resolve allowlist entry %s", fn.Name.Name, exprName(e))
		}
		out = append(out, text)
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CaseClause:
			for _, e := range n.List {
				add(e)
			}
		case *ast.ReturnStmt:
			if len(n.Results) == 2 && exprName(n.Results[1]) == "true" {
				switch n.Results[0].(type) {
				case *ast.Ident, *ast.BasicLit:
					add(n.Results[0])
				}
			}
		case *ast.CompositeLit:
			if a, ok := n.Type.(*ast.ArrayType); ok && exprName(a.Elt) == "error" {
				for _, e := range n.Elts {
					add(e)
				}
			}
		}
		return true
	})
	if len(out) == 0 {
		t.Fatalf("%s: no allowlist entries found", fn.Name.Name)
	}
	return out
}
