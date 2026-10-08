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
	"go/build"
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

	// site records where the registration call sits, for duplicate
	// detection. It is never rendered.
	site routeSite
}

// routeSite locates one registration call site. ifPos and inElse say
// whether the call sits inside an if statement's if or else branch, which
// is how mutually exclusive registrations are recognised.
type routeSite struct {
	file   string // module relative
	line   int
	ifPos  token.Pos // 0 when the call is not inside an if
	inElse bool
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
	// Duplicates lists (method, pattern) pairs the sources register more
	// than once: two different registrations (different handler or module)
	// of one pattern cannot both be honoured, and an identical pair
	// registered twice outside mutually exclusive branches panics the
	// ServeMux at boot. Only registrations in the if and else branches of
	// one if/else (the portal login limiter is one) are mutually exclusive;
	// they are one route and appear once in Routes.
	Duplicates []string
	Unresolved []Unresolved
	// Restricted lists registration constructs the census refuses: they
	// rewrite the paths routes are served under, so the census would list
	// the routes they carry under the wrong path. See allowMounts.
	Restricted []Unresolved
}

// Validate reports duplicates, unresolved and restricted registrations as
// an error.
func (r *Result) Validate() error {
	var problems []string
	for _, u := range r.Unresolved {
		problems = append(problems, "unresolved registration: "+u.String())
	}
	for _, u := range r.Restricted {
		problems = append(problems, "restricted registration: "+u.String())
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

// moduleAliases maps a package directory to the module label the census
// reports for it. R1-4 moved the body of cmd/server, the router assembly,
// into the importable package internal/app/serve so the one core binary
// (cmd/core) and the old entry point run the same code; the census reports
// that package under its historical cmd/server label so api/ROUTES.txt, a
// pinned artifact, does not churn on the move: the file being byte
// identical across the move is the evidence the move changed no route. The
// label also keys the router-assembly rules (the http.NewServeMux exemption
// and the allowMounts entries), so the serve package keeps the reviewed
// status the assembly always had.
var moduleAliases = map[string]string{
	"internal/app/serve": "cmd/server",
}

// moduleLabel returns the module label the census reports for dir: dir
// itself, or its alias when moduleAliases holds one.
func moduleLabel(dir string) string {
	if label, ok := moduleAliases[dir]; ok {
		return label
	}
	return dir
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
			// The go tool never builds a directory whose name begins with
			// a dot or an underscore, nor testdata or node_modules.
			case strings.HasPrefix(name, "."), strings.HasPrefix(name, "_"),
				name == "testdata", name == "node_modules":
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
		// Files the go tool would not build register nothing: a name
		// beginning with a dot or an underscore, or build constraints that
		// exclude the file under the default build tags.
		matched, err := build.Default.MatchFile(filepath.Dir(path), name)
		if err != nil {
			return fmt.Errorf("match build constraints of %s: %w", rel, err)
		}
		if !matched {
			return nil
		}
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
			// The name may be bound in this function as a receiver,
			// parameter, named result, variable or short variable
			// declaration, shadowing any package constant of the same name;
			// the call would use the shadowing binding, whose value the
			// census cannot know. Resolve only when no such binding exists.
			if shadows(fn, name) {
				return "", false
			}
		}
		return evalPkgConst(fu.relDir, name)
	}

	// Calls inside the gatedRouter forwarding methods of pkg/apps are
	// skipped when their pattern does not resolve: they forward a
	// caller-supplied pattern to the real router, so the registration
	// happened at the caller's site, not here. A call inside them whose
	// pattern does resolve is a real registration and is counted. The skip
	// is scoped to pkg/apps: a type named gatedRouter anywhere else gets no
	// such courtesy. Every other Handle/HandleFunc call whose pattern does
	// not resolve becomes an Unresolved.
	gatedCalls := map[ast.Node]bool{}
	for _, fu := range fileUnits {
		if fu.relDir != "pkg/apps" {
			continue
		}
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

	// callCallees holds every selector expression that is the callee of a
	// call, through parentheses. A Handle or HandleFunc selector anywhere
	// else is a method value, and a registration made through the value is
	// invisible to the second pass, so the binding itself is reported as
	// unresolved unless allowMethodValues holds it.
	callCallees := map[ast.Node]bool{}
	for _, fu := range fileUnits {
		ast.Inspect(fu.file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel := parenSelector(call.Fun); sel != nil {
					callCallees[sel] = true
				}
			}
			return true
		})
	}

	// boundSubMuxes holds, per function, the names bound to an
	// http.NewServeMux or http.StripPrefix result in that function. A
	// handler argument naming one of them mounts path rewriting.
	boundSubMuxes := map[*ast.FuncDecl]map[string]bool{}
	for _, fu := range fileUnits {
		ast.Inspect(fu.file, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			binds := false
			for _, rhs := range as.Rhs {
				if isNetHTTPCall(fu, rhs, "NewServeMux") || isNetHTTPCall(fu, rhs, "StripPrefix") {
					binds = true
					break
				}
			}
			if !binds {
				return true
			}
			fd := enclosingFunc(fu.file, as)
			if fd == nil {
				return true
			}
			names := boundSubMuxes[fd]
			if names == nil {
				names = map[string]bool{}
				boundSubMuxes[fd] = names
			}
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
			return true
		})
	}

	// Second pass: collect registrations.
	for _, fu := range fileUnits {
		// Collect each function's local const declarations once, so an
		// Ident lookup inside a function can find them.
		ast.Inspect(fu.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				// A Handle or HandleFunc selector that is not the callee of
				// a call is a method value, and a registration made through
				// the value is invisible to this walk. Every binding form
				// reaches here (assignment or var declaration, package
				// level var, return, struct literal field, argument), so
				// the binding itself is reported as unresolved unless
				// allowMethodValues holds it.
				if sel, ok := n.(*ast.SelectorExpr); ok && isRouterMethod(sel.Sel.Name) && !callCallees[sel] {
					key := fu.relPath + " " + exprText(fset, sel.X) + "." + sel.Sel.Name
					if !allowMethodValues[key] {
						result.Unresolved = append(result.Unresolved, Unresolved{
							File:   fu.relPath,
							Line:   fset.Position(sel.Pos()).Line,
							Callee: sel.Sel.Name,
							Detail: "method value not called here; a registration through the value is invisible to the census",
						})
					}
				}
				return true
			}
			// An http.NewServeMux outside the router assembly is where
			// wrong-path mounts begin: cmd/server is the one place the
			// router is assembled, and internal/app/serve (its moved body,
			// which reports under the same label) is the same place.
			if isNetHTTPCall(fu, call, "NewServeMux") && moduleLabel(fu.relDir) != "cmd/server" {
				result.Restricted = append(result.Restricted, Unresolved{
					File:   fu.relPath,
					Line:   fset.Position(call.Pos()).Line,
					Callee: "NewServeMux",
					Detail: "http.NewServeMux outside cmd/server; assemble sub muxes in cmd/server or extend the allow list in internal/routecensus",
				})
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			callee := sel.Sel.Name
			if !isRouterMethod(callee) {
				return true
			}
			enclosing := enclosingFunc(fu.file, call)
			if gatedCalls[call] {
				// Forwarder traffic from the pkg/apps gatedRouter methods:
				// the pattern comes from the caller and cannot resolve, so
				// the registration is not here. A call whose pattern does
				// resolve is a real registration and falls through.
				if len(call.Args) != 2 {
					return true
				}
				if _, resolvable := eval(fu, enclosing, call.Args[0]); !resolvable {
					return true
				}
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
			pos, inElse := ifBranch(fu.file, call)
			route := Route{
				Module:  moduleLabel(fu.relDir),
				Pattern: pattern,
				Handler: describeHandler(fset, call.Args[1]),
				site: routeSite{
					file:   fu.relPath,
					line:   fset.Position(call.Pos()).Line,
					ifPos:  pos,
					inElse: inElse,
				},
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
			// A handler that mounts an http.StripPrefix or a sub mux
			// rewrites the paths of everything under it, so this row would
			// carry the wrong path. The allow list names the mounts the
			// repo accepted; anything else fails the census.
			if reason := restrictedHandlerReason(fu, enclosing, call.Args[1], boundSubMuxes); reason != "" {
				if !allowMounts[moduleLabel(fu.relDir)+" "+pattern] {
					result.Restricted = append(result.Restricted, Unresolved{
						File:   fu.relPath,
						Line:   fset.Position(call.Pos()).Line,
						Callee: callee,
						Detail: reason + "; restructure the mount or extend the allow list in internal/routecensus",
					})
					return true
				}
			}
			result.Routes = append(result.Routes, route)
			return true
		})
	}

	result.Routes, result.Duplicates = dedupe(result.Routes)
	return result, nil
}

// allowMounts lists the mounts allowed to keep an http.StripPrefix or sub
// mux handler, keyed by the registering package's module label (its
// directory relative to the Go module root, after the moduleAliases label)
// and the registered pattern. Such a mount rewrites the
// paths of everything under it, so the census cannot name the routes the
// mount carries under their real paths; each entry here is a mount the
// repo has accepted as a whole, and the list is repeated in
// docs/refactor/ROUTE-CENSUS.md. A mount not on this list fails the
// census as restricted.
var allowMounts = map[string]bool{
	"cmd/server /uploads/": true, // the uploads file server
}

// allowMethodValues lists the Handle or HandleFunc method values the repo
// has accepted, keyed by the file relative to the Go module root and the
// selector text. A selector that is never the callee of a call binds a
// method value, and a registration made through the value happens where the
// census cannot see it, so every such binding fails the census as
// unresolved; each entry here is a binding the repo has reviewed as not a
// router. The list is repeated in docs/refactor/ROUTE-CENSUS.md. A binding
// not on this list fails the census.
var allowMethodValues = map[string]bool{
	"internal/app/worker/worker.go notifier.Handle": true, // event bus subscriber, not a mux
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

// shadows reports whether name is bound inside fn as a receiver,
// parameter, named result, variable or short variable declaration, so a
// use of name inside fn resolves to that binding instead of any package
// constant of the same name.
func shadows(fn *ast.FuncDecl, name string) bool {
	for _, fields := range []*ast.FieldList{fn.Recv, fn.Type.Params, fn.Type.Results} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			for _, id := range field.Names {
				if id.Name == name {
					return true
				}
			}
		}
	}
	bound := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.AssignStmt:
			if t.Tok == token.DEFINE {
				for _, e := range t.Lhs {
					if id, ok := e.(*ast.Ident); ok && id.Name == name {
						bound = true
						return false
					}
				}
			}
		case *ast.GenDecl:
			if t.Tok != token.VAR {
				return true
			}
			for _, spec := range t.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, id := range vs.Names {
					if id.Name == name {
						bound = true
						return false
					}
				}
			}
		}
		return true
	})
	return bound
}

// isRouterMethod reports whether name is one of the mux registration
// methods the census tracks.
func isRouterMethod(name string) bool {
	return name == "Handle" || name == "HandleFunc"
}

// parenSelector returns the selector expression e is, through any
// parentheses, or nil when e is not a selector.
func parenSelector(e ast.Expr) *ast.SelectorExpr {
	for {
		switch t := e.(type) {
		case *ast.ParenExpr:
			e = t.X
		case *ast.SelectorExpr:
			return t
		default:
			return nil
		}
	}
}

// isNetHTTPCall reports whether e is, through any parentheses, a call of
// the named net/http function.
func isNetHTTPCall(fu *fileUnit, e ast.Expr, fn string) bool {
	switch t := e.(type) {
	case *ast.ParenExpr:
		return isNetHTTPCall(fu, t.X, fn)
	case *ast.CallExpr:
		sel, ok := t.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return false
		}
		return sel.Sel.Name == fn && fu.imports[pkg.Name] == "net/http"
	}
	return false
}

// restrictedHandlerReason reports why a handler expression mounts a path
// rewriting construct, or the empty string when it does not. bound holds
// the sub mux and StripPrefix results bound in each function.
func restrictedHandlerReason(fu *fileUnit, fd *ast.FuncDecl, e ast.Expr, bound map[*ast.FuncDecl]map[string]bool) string {
	reason := ""
	ast.Inspect(e, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.CallExpr:
			if isNetHTTPCall(fu, t, "StripPrefix") {
				reason = "handler mounts through http.StripPrefix"
				return false
			}
		case *ast.Ident:
			if bound[fd][t.Name] {
				reason = "handler is a sub mux bound in this function"
				return false
			}
		}
		return true
	})
	return reason
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

// dedupe collapses the registrations of one (method, pattern) pair that
// sit in mutually exclusive branches of one if/else, and reports every
// other repeated pair as a duplicate: two different registrations of one
// pair cannot both be honoured, and an identical pair registered twice
// outside mutually exclusive branches panics the ServeMux at boot.
func dedupe(routes []Route) ([]Route, []string) {
	SortRoutes(routes)
	var out []Route
	var dups []string
	for i := 0; i < len(routes); {
		j := i + 1
		for j < len(routes) && routes[j].Method == routes[i].Method && routes[j].Pattern == routes[i].Pattern {
			j++
		}
		out = append(out, collapse(routes[i:j], &dups))
		i = j
	}
	sort.Strings(dups)
	return out, dups
}

// collapse reduces one (method, pattern) group to the route it declares,
// appending a duplicate line for every registration the group carries
// beyond the collapsed ones.
func collapse(group []Route, dups *[]string) Route {
	first := group[0]
	key := first.Method + " " + first.Pattern
	for _, r := range group[1:] {
		if r.Module != first.Module || r.Handler != first.Handler {
			*dups = append(*dups, fmt.Sprintf(
				"%s registered as %q in %s (%s:%d) and as %q in %s (%s:%d)",
				key, first.Handler, first.Module, first.site.file, first.site.line,
				r.Handler, r.Module, r.site.file, r.site.line))
			return first
		}
	}
	remaining := group
	for len(remaining) > 1 {
		paired := false
		for k := 1; k < len(remaining); k++ {
			a, b := remaining[0].site, remaining[k].site
			if a.ifPos != token.NoPos && a.ifPos == b.ifPos && a.inElse != b.inElse {
				remaining = append(remaining[:k], remaining[k+1:]...)
				remaining = remaining[1:]
				paired = true
				break
			}
		}
		if !paired {
			*dups = append(*dups, fmt.Sprintf(
				"%s registered identically %d times (%s:%d and %s:%d among them); registered twice outside mutually exclusive branches the ServeMux panics at boot",
				key, len(remaining),
				remaining[0].site.file, remaining[0].site.line,
				remaining[1].site.file, remaining[1].site.line))
			break
		}
	}
	return first
}

// ifBranch reports the position of the innermost if statement n sits in
// and whether n sits in that if's else branch. It reports 0 when n is not
// inside an if.
func ifBranch(f *ast.File, n ast.Node) (token.Pos, bool) {
	type side struct {
		from, to token.Pos
		inElse   bool
	}
	bestPos, bestElse, bestSize := token.NoPos, false, -1
	ast.Inspect(f, func(node ast.Node) bool {
		ifStmt, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		sides := []side{{ifStmt.Body.Pos(), ifStmt.Body.End(), false}}
		if ifStmt.Else != nil {
			sides = append(sides, side{ifStmt.Else.Pos(), ifStmt.Else.End(), true})
		}
		for _, s := range sides {
			if s.from <= n.Pos() && n.End() <= s.to {
				size := int(s.to - s.from)
				if bestSize == -1 || size < bestSize {
					bestPos, bestElse, bestSize = ifStmt.Pos(), s.inElse, size
				}
			}
		}
		return true
	})
	return bestPos, bestElse
}
