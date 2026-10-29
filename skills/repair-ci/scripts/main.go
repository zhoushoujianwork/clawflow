// Command scripts reconciles ClawFlow's migration registry during conflict repair.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const registryPath = "internal/store/migrations.go"

type entry struct {
	name                     string
	version                  int
	source                   string
	versionStart, versionEnd int
}

type registry struct {
	entries []entry
}

// Parse Go rather than matching lines: Up functions can contain nested literals.
func parseRegistry(src []byte) (registry, error) {
	var r registry
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, registryPath, src, 0)
	if err != nil {
		return r, err
	}
	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, spec := range g.Specs {
			v := spec.(*ast.ValueSpec)
			if len(v.Names) != 1 || v.Names[0].Name != "Migrations" || len(v.Values) != 1 {
				continue
			}
			list, ok := v.Values[0].(*ast.CompositeLit)
			if !ok {
				return r, fmt.Errorf("Migrations must be a literal registry")
			}
			offset := func(p token.Pos) int { return fs.Position(p).Offset }
			names := map[string]bool{}
			prev := 0
			for _, elem := range list.Elts {
				lit, ok := elem.(*ast.CompositeLit)
				if !ok {
					return r, fmt.Errorf("migration must be a keyed literal")
				}
				e := entry{source: string(src[offset(lit.Pos()):offset(lit.End())])}
				hasUp := false
				for _, field := range lit.Elts {
					kv, ok := field.(*ast.KeyValueExpr)
					if !ok {
						return r, fmt.Errorf("migration must use named fields")
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						return r, fmt.Errorf("invalid migration field")
					}
					switch key.Name {
					case "Version":
						n, ok := kv.Value.(*ast.BasicLit)
						if !ok || n.Kind != token.INT {
							return r, fmt.Errorf("migration version must be an integer literal")
						}
						e.version, err = strconv.Atoi(n.Value)
						if err != nil {
							return r, err
						}
						e.versionStart, e.versionEnd = offset(n.Pos())-offset(lit.Pos()), offset(n.End())-offset(lit.Pos())
					case "Name":
						n, ok := kv.Value.(*ast.BasicLit)
						if !ok || n.Kind != token.STRING {
							return r, fmt.Errorf("migration name must be a string literal")
						}
						e.name, err = strconv.Unquote(n.Value)
						if err != nil {
							return r, err
						}
					case "Up":
						hasUp = true
					}
				}
				if e.name == "" || names[e.name] || e.version <= prev || !hasUp {
					return r, fmt.Errorf("registry requires unique names and ascending positive versions with Up functions")
				}
				names[e.name], prev = true, e.version
				r.entries = append(r.entries, e)
			}
			return r, nil
		}
	}
	return r, fmt.Errorf("Migrations registry not found")
}

// Only unpublished additions may be renumbered. The base registry is authoritative.
func reconcile(ancestor, base, pr, working []byte) ([]byte, []string, error) {
	old, err := parseRegistry(ancestor)
	if err != nil {
		return nil, nil, fmt.Errorf("ancestor: %w", err)
	}
	current, err := parseRegistry(base)
	if err != nil {
		return nil, nil, fmt.Errorf("base: %w", err)
	}
	incoming, err := parseRegistry(pr)
	if err != nil {
		return nil, nil, fmt.Errorf("PR: %w", err)
	}
	// The working registry may contain duplicate versions after a manual merge.
	// Locate its literal using the parser only; validation happens on the result.
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, registryPath, working, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve syntax conflicts first: %w", err)
	}
	start, end := -1, -1
	ast.Inspect(f, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if !ok || len(v.Names) != 1 || v.Names[0].Name != "Migrations" || len(v.Values) != 1 {
			return true
		}
		if lit, ok := v.Values[0].(*ast.CompositeLit); ok {
			start, end = fs.Position(lit.Lbrace).Offset+1, fs.Position(lit.Rbrace).Offset
		}
		return false
	})
	if start < 0 {
		return nil, nil, fmt.Errorf("working Migrations registry not found")
	}
	canonical := func(source string) string {
		formatted, err := format.Source([]byte("package store\nvar registry = []Migration{" + source + "}\n"))
		if err != nil {
			return source
		}
		return string(formatted)
	}
	oldNames, baseNames := map[string]entry{}, map[string]entry{}
	for _, e := range old.entries {
		oldNames[e.name] = e
	}
	max := 0
	var body strings.Builder
	body.WriteString("\n")
	for _, e := range current.entries {
		baseNames[e.name] = e
		max = e.version
		fmt.Fprintf(&body, "%s,\n", e.source)
	}
	var mapping []string
	for _, e := range incoming.entries {
		if previous, ok := oldNames[e.name]; ok {
			if canonical(previous.source) != canonical(e.source) {
				return nil, nil, fmt.Errorf("PR modifies existing migration %q; resolve manually", e.name)
			}
			continue
		}
		if existing, ok := baseNames[e.name]; ok {
			// A matching name with different code is ambiguous, never silently discard it.
			if canonical(existing.source[:existing.versionStart]+"0"+existing.source[existing.versionEnd:]) != canonical(e.source[:e.versionStart]+"0"+e.source[e.versionEnd:]) {
				return nil, nil, fmt.Errorf("different migrations share name %q", e.name)
			}
			continue
		}
		max++
		fmt.Fprintf(&body, "%s%d%s,\n", e.source[:e.versionStart], max, e.source[e.versionEnd:])
		mapping = append(mapping, fmt.Sprintf("%s: %d -> %d", e.name, e.version, max))
	}
	result := bytes.Join([][]byte{working[:start], []byte(body.String()), working[end:]}, nil)
	result, err = format.Source(result)
	if err != nil {
		return nil, nil, err
	}
	if _, err = parseRegistry(result); err != nil {
		return nil, nil, err
	}
	return result, mapping, nil
}

func run(baseRef, prRef string) error {
	git := func(args ...string) ([]byte, error) {
		out, err := exec.Command("git", args...).Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
	ancestorRef, err := git("merge-base", baseRef, prRef)
	if err != nil {
		return err
	}
	var sources [][]byte
	for _, ref := range []string{strings.TrimSpace(string(ancestorRef)), baseRef, prRef} {
		src, err := git("show", ref+":"+registryPath)
		if err != nil {
			return err
		}
		sources = append(sources, src)
	}
	working, err := os.ReadFile(registryPath)
	if err != nil {
		return err
	}
	result, mapping, err := reconcile(sources[0], sources[1], sources[2], working)
	if err != nil {
		return err
	}
	info, err := os.Stat(registryPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(registryPath, result, info.Mode().Perm()); err != nil {
		return err
	}
	for _, line := range mapping {
		fmt.Println(line)
	}
	return nil
}

func main() {
	base := flag.String("base-ref", "origin/main", "fetched base ref")
	pr := flag.String("pr-ref", "ORIG_HEAD", "pre-merge PR ref")
	flag.Parse()
	if err := run(*base, *pr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
