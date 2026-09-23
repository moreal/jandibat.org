// This standalone AST gate intentionally uses only the Nix-pinned Go standard library.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var allowances = map[string]string{
	"integrations/cockroach/revocations.go": "virtual-catalog",
	"operations/cockroach/audit_outbox.go":  "virtual-catalog",
	"operations/cockroach/store.go":         "virtual-catalog",
	"operations/cockroach/retention.go":     "sealed-dataset-identifiers",
}

// Recognize executable SQL statement forms, including case and whitespace
// variants. The exact literal inventory below protects the four exceptions.
var sqlStatement = regexp.MustCompile(`(?is)\b(?:select\s+(?:/\*.*?\*/\s*)*(?:\*|[\w"'($?])|(?:insert|upsert)\s+into\b|update\s+[\w".]+\s+set\b|delete\s+from\b|with\s+[\w"]+\s+as\s*\(|(?:create|alter|drop|truncate)\s+(?:table|index|database|view|schema)\b|(?:grant|revoke)\s+\w+\s+on\b|show\s+(?:tables|columns|databases|indexes)\b)`)
var sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
var sqlLineComment = regexp.MustCompile(`--[^\n]*(?:\n|$)`)

func main() {
	write := flag.Bool("write-manifest", false, "write reviewed literal inventory")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("unexpected arguments: %v", flag.Args())
	}
	root := os.Getenv("ADAPTER_SQL_ROOT")
	if root == "" {
		root = "apps/api/internal/adapters"
	}
	manifest := os.Getenv("ADAPTER_SQL_MANIFEST")
	if manifest == "" {
		manifest = "scripts/adapter-sql-allowlist.manifest"
	}
	observed, err := inventory(root)
	if err != nil {
		fatal("adapter SQL inventory: %v", err)
	}
	if *write {
		if err := os.WriteFile(manifest, []byte(observed), 0644); err != nil {
			fatal("write adapter SQL manifest: %v", err)
		}
		return
	}
	expected, err := os.ReadFile(manifest)
	if err != nil {
		fatal("read adapter SQL manifest: %v", err)
	}
	if string(expected) != observed {
		fatal("adapter SQL literal inventory differs from the checked-in allowlist; inspect the SQL and update the manifest only after review")
	}
	fmt.Println("adapter SQL AST literal inventory satisfies the reviewed allowlist")
}

func inventory(root string) (string, error) {
	seen := make(map[string]bool)
	lines := make([]string, 0, len(allowances))
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && entry.Name() == "generated" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		literalValues := make([]string, 0)
		candidateValues := make([]string, 0)
		constants := fileConstants(file)
		ast.Inspect(file, func(node ast.Node) bool {
			if expression, ok := node.(*ast.BinaryExpr); ok && expression.Op == token.ADD {
				if value, ok := staticString(expression, constants, make(map[*ast.Object]bool)); ok {
					candidateValues = append(candidateValues, value)
				}
			}
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true // The Go parser will report malformed string syntax.
			}
			literalValues = append(literalValues, value)
			candidateValues = append(candidateValues, value)
			return true
		})
		marker, allowed := allowances[rel]
		if !allowed {
			for _, value := range candidateValues {
				if looksLikeSQL(value) {
					return fmt.Errorf("unlisted handwritten SQL literal in %s", rel)
				}
			}
			return nil
		}
		seen[rel] = true
		if !hasMarker(file, marker) {
			return fmt.Errorf("missing %s SQL allowlist marker: %s", marker, rel)
		}
		// Length-prefixed decoded values avoid ambiguous concatenations and
		// make whitespace, case, and same-line replacements visible.
		hash := sha256.New()
		for _, value := range literalValues {
			fmt.Fprintf(hash, "%d:", len(value))
			hash.Write([]byte(value))
		}
		lines = append(lines, rel+"\tsha256:"+hex.EncodeToString(hash.Sum(nil)))
		return nil
	})
	if err != nil {
		return "", err
	}
	for rel := range allowances {
		if !seen[rel] {
			return "", errors.New("allowlisted adapter SQL file is missing: " + rel)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n", nil
}

func looksLikeSQL(value string) bool {
	if sqlStatement.MatchString(value) {
		return true
	}
	value = sqlBlockComment.ReplaceAllString(value, " ")
	value = sqlLineComment.ReplaceAllString(value, " ")
	return sqlStatement.MatchString(value)
}

func fileConstants(file *ast.File) map[*ast.Object]ast.Expr {
	constants := make(map[*ast.Object]ast.Expr)
	ast.Inspect(file, func(node ast.Node) bool {
		declaration, ok := node.(*ast.GenDecl)
		if !ok || declaration.Tok != token.CONST {
			return true
		}
		var previous []ast.Expr
		for _, item := range declaration.Specs {
			spec := item.(*ast.ValueSpec)
			if len(spec.Values) != 0 {
				previous = spec.Values
			}
			if len(previous) != len(spec.Names) {
				continue
			}
			for index, name := range spec.Names {
				if name.Obj != nil && name.Obj.Kind == ast.Con {
					constants[name.Obj] = previous[index]
				}
			}
		}
		return false
	})
	return constants
}

func staticString(expression ast.Expr, constants map[*ast.Object]ast.Expr, resolving map[*ast.Object]bool) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(value.Value)
		return text, err == nil
	case *ast.ParenExpr:
		return staticString(value.X, constants, resolving)
	case *ast.Ident:
		if value.Obj == nil || resolving[value.Obj] {
			return "", false
		}
		named, ok := constants[value.Obj]
		if !ok {
			return "", false
		}
		resolving[value.Obj] = true
		result, ok := staticString(named, constants, resolving)
		delete(resolving, value.Obj)
		return result, ok
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, leftOK := staticString(value.X, constants, resolving)
		right, rightOK := staticString(value.Y, constants, resolving)
		return left + right, leftOK && rightOK
	default:
		return "", false
	}
}

func hasMarker(file *ast.File, marker string) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.Contains(comment.Text, "adapter-sql-allowlist: "+marker) {
				return true
			}
		}
	}
	return false
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
