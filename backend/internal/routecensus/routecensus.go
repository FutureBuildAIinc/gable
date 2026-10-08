// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package routecensus lists every HTTP route the Go sources of this module
// register. It walks every non-test file with go/ast and collects each call
// to Router.Handle or Router.HandleFunc whose pattern argument resolves to a
// constant string: plain literals, concatenated constants, and net/http
// method constants included. A call whose pattern it cannot resolve
// statically is reported as unresolved, and a census with unresolved calls is
// an error, so a registration style the tool does not understand fails the
// census loudly instead of being missed silently.
//
// The census reads the sources rather than recording a live router because
// the real router is assembled in cmd/server/main.go behind a database
// connection, and part of the surface is conditional on configuration (the
// category pricing routes and the A2A receiver): a runtime recording would
// capture one boot's configuration rather than the declared surface, and
// could not run without a database. The sources are the single truth for
// what the server registers.
package routecensus

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Route is one registered route.
type Route struct {
	// Method is the upper-case HTTP method the pattern is scoped to, or the
	// empty string when the pattern carries no method prefix and answers
	// every method.
	Method string
	// Pattern is the ServeMux pattern exactly as registered.
	Pattern string
	// Module is the directory of the registering package, relative to the
	// Go module root.
	Module string
	// Handler describes the handler expression at the call site (for
	// example "h.ListQuotes"). A function literal becomes "func literal";
	// a one-argument wrapper call such as guard(h.ListQuotes) names the
	// wrapped handler.
	Handler string
}

// Unresolved is a Handle or HandleFunc call whose pattern argument is not a
// statically resolvable ServeMux pattern. The registration may be a route
// the census cannot name, so a result carrying unresolved calls is an error.
type Unresolved struct {
	File   string // relative to the module root
	Line   int
	Callee string // "Handle" or "HandleFunc"
	Detail string
}

func (u Unresolved) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", u.File, u.Line, u.Callee, u.Detail)
}

// Result is the census of one source tree.
type Result struct {
	Routes []Route
	// Duplicates lists (method, pattern) pairs the sources register with
	// two different registrations (different handler or module). Registering
	// the same pair twice panics the ServeMux at boot when both runs happen,
	// and two different registrations of one pattern cannot both be honoured,
	// so a pair like that is an error. Two registrations that are identical
	// in every column (a conditional if/else around one handler, as the
	// portal login limiter does) are one route and appear once in Routes.
	Duplicates []string
	Unresolved []Unresolved
}

// Validate reports duplicates and unresolved registrations as an error.
func (r *Result) Validate() error {
	var problems []string
	for _, u := range r.Unresolved {
		problems = append(problems, "unresolved registration: "+u.String())
	}
	for _, d := range r.Duplicates {
		problems = append(problems, "duplicate registration: "+d)
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("route census found %d problem(s):\n\t%s",
		len(problems), strings.Join(problems, "\n\t"))
}

// FindModuleRoot walks up from start until it finds a directory holding
// go.mod, and returns that directory. It returns an error when no ancestor
// of start holds a go.mod.
func FindModuleRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found in %s or any of its ancestors", start)
		}
		dir = parent
	}
}

// modulePathRe reads the module directive out of go.mod.
var modulePathRe = regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`)

// methodPatternRe splits a ServeMux pattern into its method and path.
var methodPatternRe = regexp.MustCompile(`^([A-Za-z]+)[ \t]+(/.*)$`)

// fileUnit is one parsed source file plus what the resolver needs about it.
type fileUnit struct {
	relPath string            // relative to the module root, slash separated
	relDir  string            // directory of the file, relative to the module root
	imports map[string]string // qualifier -> import path
	file    *ast.File
}

// Collect walks the Go sources under moduleRoot and returns the census.
// Test files (files named *_test.go) are skipped: they register fixtures,
// not server surface.
func Collect(moduleRoot string) (Result, error) {
	var result Result

	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return result, fmt.Errorf("read go.mod: %w", err)
	}
	m := modulePathRe.FindSubmatch(data)
	if m == nil {
		return result, fmt.Errorf("no module directive in %s", filepath.Join(moduleRoot, "go.mod"))
	}
	modulePath := string(m[1])

	fset := token.NewFileSet()
	type constDecl struct {
		fu   *fileUnit
		spec *ast.ValueSpec
	}
	var (
		fileUnits []*fileUnit
		// pkgConsts holds the package-level string constants of each
		// package directory, by constant name.
		pkgConsts = map[string][]constDecl{} // package dir -> decls
	)

	// Walk and parse every non-test file.
	err = filepath.WalkDir(moduleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			switch {
			case name == ".":
				return nil
			case strings.HasPrefix(name, "."), name == "testdata", name == "node_modules":
				return filepath.SkipDir
			// A vendor directory is only a dependency checkout at the
			// module root; deeper "vendor" directories are workspace
			// packages like internal/vendor.
			case name == "vendor" && filepath.Clean(path) == filepath.Join(moduleRoot, "vendor"):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		fu := &fileUnit{
			relPath: rel,
			relDir:  filepath.ToSlash(filepath.Dir(rel)),
			imports: importsOf(f),
			file:    f,
		}
		fileUnits = append(fileUnits, fu)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				pkgConsts[fu.relDir] = append(pkgConsts[fu.relDir], constDecl{fu: fu, spec: vs})
			}
		}
		return nil
	})
	if err != nil {
		return result, err
	}

	// importPathByDir maps a package directory to its import path.
	importPathByDir := make(map[string]string, len(pkgConsts))
	for dir := range pkgConsts {
		if dir == "." {
			importPathByDir[dir] = modulePath
		} else {
			importPathByDir[dir] = modulePath + "/" + dir
		}
	}

	// constMemo memoises constant evaluation per (package dir, name).
	constMemo := map[string]string{}
	constInProgress := map[string]bool{}

	// The constant resolver is mutually recursive (an expression names a
	// constant, a constant's value is an expression), so the closures are
	// declared first and bound after.
	var (
		eval         func(fu *fileUnit, fn *ast.FuncDecl, e ast.Expr) (string, bool)
		lookupConst  func(fu *fileUnit, fn *ast.FuncDecl, qualifier, name string) (string, bool)
		evalPkgConst func(dir, name string) (string, bool)
	)

	// dirOfImport maps an import path of this module back to its directory.
	dirOfImport := func(path string) (string, bool) {
		if path == modulePath {
			return ".", true
		}
		if !strings.HasPrefix(path, modulePath+"/") {
			return "", false
		}
		dir := strings.TrimPrefix(path, modulePath+"/")
		if _, ok := importPathByDir[dir]; !ok {
			return "", false
		}
		return dir, true
	}

	// evalPkgConst evaluates one package-level constant of a directory.
	evalPkgConst = func(dir, name string) (string, bool) {
		key := dir + "|" + name
		if v, ok := constMemo[key]; ok {
			return v, true
		}
		if constInProgress[key] {
			return "", false // constant cycle
		}
		for _, cd := range pkgConsts[dir] {
			if cd.spec.Names[0].Name != name {
				continue
			}
			constInProgress[key] = true
			v, ok := eval(cd.fu, nil, cd.spec.Values[0])
			delete(constInProgress, key)
			if !ok {
				return "", false
			}
			constMemo[key] = v
			return v, true
		}
		return "", false
	}

	// eval resolves e as a constant string in the scope of fu (inside fn,
	// when the call sits in a function).
	eval = func(fu *fileUnit, fn *ast.FuncDecl, e ast.Expr) (string, bool) {
		switch t := e.(type) {
		case *ast.BasicLit:
			if t.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(t.Value)
			if err != nil {
				return "", false
			}
			return s, true
		case *ast.ParenExpr:
			return eval(fu, fn, t.X)
		case *ast.BinaryExpr:
			if t.Op != token.ADD {
				return "", false
			}
			a, ok1 := eval(fu, fn, t.X)
			b, ok2 := eval(fu, fn, t.Y)
			if !ok1 || !ok2 {
				return "", false
			}
			return a + b, true
		case *ast.Ident:
			return lookupConst(fu, fn, "", t.Name)
		case *ast.SelectorExpr:
			pkg, ok := t.X.(*ast.Ident)
			if !ok {
				return "", false
			}
			return lookupConst(fu, fn, pkg.Name, t.Sel.Name)
		}
		return "", false
	}

	// lookupConst resolves name, either qualified by a package qualifier or
	// unqualified in the file's package or the enclosing function.
	lookupConst = func(fu *fileUnit, fn *ast.FuncDecl, qualifier, name string) (string, bool) {
		if qualifier != "" {
			path, ok := fu.imports[qualifier]
			if !ok {
				return "", false
			}
			// net/http method constants, the one stdlib source of route
			// patterns worth resolving.
			if path == "net/http" {
				if v, ok := httpMethodConsts[name]; ok {
					return v, true
				}
			}
			// A module package: resolve the import path to its directory.
			dir, ok := dirOfImport(path)
			if !ok {
				return "", false
			}
			return evalPkgConst(dir, name)
		}
		// Unqualified: a constant of the enclosing function, else of the
		// file's own package.
		if fn != nil {
			for _, stmt := range fn.Body.List {
				ds, ok := stmt.(*ast.DeclStmt)
				if !ok {
					continue
				}
				gd, ok := ds.Decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 || vs.Names[0].Name != name {
						continue
					}
					return eval(fu, fn, vs.Values[0])
				}
			}
		}
		return evalPkgConst(fu.relDir, name)
	}

	// Calls inside gatedRouter forwarding methods are skipped: they forward
	// a caller-supplied pattern to the real router (pkg/apps/apps.go), and
	// are delegation, not registration. Every other Handle/HandleFunc call
	// whose pattern does not resolve becomes an Unresolved.
	gatedCalls := map[ast.Node]bool{}
	for _, fu := range fileUnits {
		for _, decl := range fu.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !isGatedRouterMethod(fd) {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					gatedCalls[call] = true
				}
				return true
			})
		}
	}

	// Second pass: collect registrations.
	for _, fu := range fileUnits {
		// Collect each function's local const declarations once, so an
		// Ident lookup inside a function can find them.
		ast.Inspect(fu.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if gatedCalls[call] {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			callee := sel.Sel.Name
			if callee != "Handle" && callee != "HandleFunc" {
				return true
			}
			if len(call.Args) < 2 {
				result.Unresolved = append(result.Unresolved, Unresolved{
					File:   fu.relPath,
					Line:   fset.Position(call.Pos()).Line,
					Callee: callee,
					Detail: fmt.Sprintf("call has %d arguments, want pattern and handler", len(call.Args)),
				})
				return true
			}
			enclosing := enclosingFunc(fu.file, call)
			pattern, ok := eval(fu, enclosing, call.Args[0])
			if !ok {
				result.Unresolved = append(result.Unresolved, Unresolved{
					File:   fu.relPath,
					Line:   fset.Position(call.Pos()).Line,
					Callee: callee,
					Detail: "pattern argument is not a constant string the census can resolve",
				})
				return true
			}
			route := Route{
				Module:  fu.relDir,
				Pattern: pattern,
				Handler: describeHandler(fset, call.Args[1]),
			}
			if m := methodPatternRe.FindStringSubmatch(pattern); m != nil {
				route.Method = strings.ToUpper(m[1])
				route.Pattern = m[2]
			} else if !strings.HasPrefix(pattern, "/") {
				result.Unresolved = append(result.Unresolved, Unresolved{
					File:   fu.relPath,
					Line:   fset.Position(call.Pos()).Line,
					Callee: callee,
					Detail: fmt.Sprintf("pattern %q is not a [METHOD ]/path ServeMux pattern", pattern),
				})
				return true
			}
			result.Routes = append(result.Routes, route)
			return true
		})
	}

	result.Routes, result.Duplicates = dedupe(result.Routes)
	return result, nil
}

// httpMethodConsts resolves the net/http method constants a pattern might
// be built from.
var httpMethodConsts = map[string]string{
	"MethodGet":     "GET",
	"MethodHead":    "HEAD",
	"MethodPost":    "POST",
	"MethodPut":     "PUT",
	"MethodPatch":   "PATCH",
	"MethodDelete":  "DELETE",
	"MethodConnect": "CONNECT",
	"MethodOptions": "OPTIONS",
	"MethodTrace":   "TRACE",
}

// importsOf maps each import qualifier of f to its import path.
func importsOf(f *ast.File) map[string]string {
	imports := make(map[string]string)
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		qualifier := ""
		if imp.Name != nil {
			qualifier = imp.Name.Name
		} else {
			qualifier = path[strings.LastIndex(path, "/")+1:]
		}
		if qualifier == "." || qualifier == "_" {
			continue
		}
		imports[qualifier] = path
	}
	return imports
}

// isGatedRouterMethod reports whether fd is a method on the gatedRouter type
// in pkg/apps (the enablement-gating forwarder).
func isGatedRouterMethod(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return false
	}
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	ident, ok := t.(*ast.Ident)
	return ok && ident.Name == "gatedRouter"
}

// enclosingFunc finds the function declaration holding n, or nil.
func enclosingFunc(f *ast.File, n ast.Node) *ast.FuncDecl {
	var best *ast.FuncDecl
	ast.Inspect(f, func(node ast.Node) bool {
		if fd, ok := node.(*ast.FuncDecl); ok {
			if fd.Pos() <= n.Pos() && n.End() <= fd.End() {
				best = fd
			}
		}
		return true
	})
	return best
}

// describeHandler renders the handler expression as written at the call
// site. A function literal becomes "func literal"; a one-argument wrapper
// call such as guard(h.ListQuotes) names the wrapped handler.
func describeHandler(fset *token.FileSet, e ast.Expr) string {
	switch t := e.(type) {
	case *ast.FuncLit:
		return "func literal"
	case *ast.ParenExpr:
		return describeHandler(fset, t.X)
	case *ast.CallExpr:
		if len(t.Args) == 1 {
			return describeHandler(fset, t.Args[0])
		}
	}
	return exprText(fset, e)
}

// exprText renders an expression's source, whitespace collapsed.
func exprText(fset *token.FileSet, e ast.Expr) string {
	from := fset.Position(e.Pos())
	to := fset.Position(e.End())
	if from.Filename != to.Filename || to.Offset < from.Offset {
		return "<expr>"
	}
	src, err := os.ReadFile(from.Filename)
	if err != nil {
		return "<expr>"
	}
	text := string(src[from.Offset:to.Offset])
	return strings.Join(strings.Fields(text), " ")
}

// SortRoutes orders routes by pattern, then method, then module, then
// handler, so the census is byte-stable across runs.
func SortRoutes(routes []Route) {
	sort.Slice(routes, func(i, j int) bool {
		a, b := routes[i], routes[j]
		if a.Pattern != b.Pattern {
			return a.Pattern < b.Pattern
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		if a.Module != b.Module {
			return a.Module < b.Module
		}
		return a.Handler < b.Handler
	})
}

// dedupe collapses routes registered identically more than once (a
// conditional if/else around one handler registers the same route through
// two call sites) and reports (method, pattern) pairs registered with two
// different registrations as duplicates.
func dedupe(routes []Route) ([]Route, []string) {
	SortRoutes(routes)
	out := make([]Route, 0, len(routes))
	for i, r := range routes {
		if i > 0 && routes[i-1] == r {
			continue
		}
		out = append(out, r)
	}

	type reg struct {
		line     Route
		conflict bool
	}
	seen := map[string]*reg{}
	var dups []string
	for _, r := range routes {
		k := r.Method + " " + r.Pattern
		s, ok := seen[k]
		if !ok {
			seen[k] = &reg{line: r}
			continue
		}
		if s.line != r && !s.conflict {
			s.conflict = true
			dups = append(dups, fmt.Sprintf("%s registered as %q in %s and as %q in %s",
				k, s.line.Handler, s.line.Module, r.Handler, r.Module))
		}
	}
	sort.Strings(dups)
	return out, dups
}
