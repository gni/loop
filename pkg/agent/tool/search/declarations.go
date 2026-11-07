package search

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

type importReference struct{ alias, path string }
type callReference struct {
	name, expression, caller string
	line                     int
}
type goMetadata struct {
	symbols []codeSymbol
	imports []importReference
	calls   []callReference
	partial bool
}

// parseGoMetadata records syntax-level relationships only. Candidate callers
// cannot establish receiver types, dynamic dispatch, or cross-package identity.
func parseGoMetadata(filename string, data []byte) goMetadata {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, filename, data, parser.SkipObjectResolution)
	result := goMetadata{partial: err != nil}
	if parsed == nil {
		return result
	}
	for _, entry := range parsed.Imports {
		importPath, err := strconv.Unquote(entry.Path.Value)
		if err != nil {
			result.partial = true
			continue
		}
		alias := ""
		if entry.Name != nil {
			alias = entry.Name.Name
		}
		if len(result.imports) < 32 {
			result.imports = append(result.imports, importReference{alias, importPath})
		} else {
			result.partial = true
		}
	}
	add := func(name, kind string, pos, end token.Pos) {
		if name == "_" {
			return
		}
		if len(result.symbols) >= 256 {
			result.partial = true
			return
		}
		result.symbols = append(result.symbols, codeSymbol{name: name, kind: kind, line: fset.Position(pos).Line, endLine: fset.Position(end).Line})
	}
	calls := func(node ast.Node, caller string) {
		if node == nil {
			return
		}
		ast.Inspect(node, func(n ast.Node) bool {
			expression, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, display := calledName(expression.Fun)
			if name == "" {
				return true
			}
			if len(result.calls) >= 256 {
				result.partial = true
				return false
			}
			result.calls = append(result.calls, callReference{name: name, expression: display, caller: caller, line: fset.Position(expression.Pos()).Line})
			return true
		})
	}
	for _, declaration := range parsed.Decls {
		switch d := declaration.(type) {
		case *ast.FuncDecl:
			name, kind := d.Name.Name, "func"
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				name = receiverName(d.Recv.List[0].Type) + "." + name
			}
			add(name, kind, d.Pos(), d.End())
			if d.Body != nil {
				calls(d.Body, name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add(s.Name.Name, "type", s.Pos(), s.End())
				case *ast.ValueSpec:
					for _, name := range s.Names {
						add(name.Name, d.Tok.String(), name.Pos(), s.End())
					}
					calls(s, "package initialization")
				}
			}
		}
	}
	return result
}

func receiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return receiverName(e.X)
	case *ast.IndexExpr:
		return receiverName(e.X)
	case *ast.IndexListExpr:
		return receiverName(e.X)
	}
	return "receiver"
}

func calledName(expr ast.Expr) (string, string) {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name, e.Name
	case *ast.SelectorExpr:
		qualifier := receiverName(e.X)
		if qualifier == "receiver" {
			qualifier = "expression"
		}
		return e.Sel.Name, qualifier + "." + e.Sel.Name
	case *ast.IndexExpr:
		return calledName(e.X)
	case *ast.IndexListExpr:
		return calledName(e.X)
	case *ast.ParenExpr:
		return calledName(e.X)
	}
	return "", ""
}
