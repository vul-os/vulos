// Command wiregen generates TypeScript interfaces from a small, explicit list
// of Go wire types (see `targets` below) and prints them to stdout.
//
// # Why go/ast instead of go/types, and instead of regex
//
// Regexing Go source to find `type Foo struct { ... }` blocks is exactly the
// kind of hand-maintained parallel definition this generator exists to
// replace with something that cannot silently drift — a regex has no idea
// what a struct tag, a slice type, or a multi-line comment actually are, and
// it breaks the moment someone reformats a line it "worked" against.
//
// go/types would give fully resolved, cross-package type information, but
// getting there means loading and type-checking the ENTIRE import graph of
// whatever package declares a target type before wiregen can look at a
// single struct tag: slow, and it means a target type in a package that
// (transitively) imports something that doesn't build in wiregen's
// environment takes wiregen down with it — for a job that only needs to read
// a handful of literal `type … struct { … }` declarations and const blocks.
//
// go/parser + go/ast parse each target FILE in isolation: no import
// resolution, no build tags, no module loading. Struct fields, their type
// expressions, and their raw string tags are syntax, not something that
// needs a type checker to resolve. The one place this would otherwise paper
// over a gap is a field whose type isn't one of the basic Go kinds, a known
// stdlib selector (time.Time), or another type in `targets` — resolveType
// treats that as a hard error rather than guessing (see below), so adding a
// target with an out-of-scope dependency fails the generator loudly instead
// of emitting a wrong or approximate TypeScript type.
//
// # Scope
//
// wiregen has no knowledge of any Go type outside `targets`. It is a small,
// hand-picked vertical slice (see the comment above `targets`), not a
// general Go->TS converter — extending coverage means adding entries below,
// each of which must be a real named type in one of the listed files.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// target names one Go type wiregen must emit, plus the file it is declared
// in (repo-root-relative — wiregen must be run with the repo root as its
// working directory, which `npm run gen:wire-types` does by passing -root).
//
// Order matters for the emitted file: a struct/enum must appear after every
// other target it references (Endpoint before Status; LinkState before
// LinkStatus), since wiregen does not topologically sort — it just walks
// this list top to bottom.
type target struct {
	file string
	name string
}

// The vertical slice this generator covers: five real, exported Go types
// with `json:"..."` tags that are actually serialised onto the wire by
// backend/cmd/server, picked because they are small, self-contained (no
// dependency outside this list once resolved), and genuinely consumed by the
// frontend — not a broad, unverified sweep of ~145k lines of Go.
//
//   - clusterHealthResponse — backend/cmd/server/health.go
//     the exact type `writeJSON`'d by GET /api/health.
//   - reach.Endpoint, reach.Status — backend/services/reach/reach.go
//     Status (embedding a redacted Endpoint) is what `GET /api/network/reach`
//     serves as its `endpoints` array (see registerReachStatus in
//     backend/cmd/server/reachwire.go: `out["endpoints"] = rt.Set.Statuses()`).
//   - tunnel.LinkState, tunnel.LinkStatus — backend/services/reach/tunnel/agent.go
//     LinkStatus is what the same route serves as its `links` array
//     (`out["links"] = rt.Agent.Status()`); LinkState is its string-enum
//     `state` field.
//
// NOT covered: the top-level `/api/network/reach` response itself, because
// it is an inline `map[string]any{...}` in reachwire.go, not a named Go
// type — there is nothing for wiregen to parse a declaration of. See the
// generated file's header for how the two arrays it emits map onto that
// response.
var targets = []target{
	{"backend/cmd/server/health.go", "clusterHealthResponse"},
	{"backend/services/reach/reach.go", "Endpoint"},
	{"backend/services/reach/reach.go", "Status"},
	{"backend/services/reach/tunnel/agent.go", "LinkState"},
	{"backend/services/reach/tunnel/agent.go", "LinkStatus"},
}

type field struct {
	jsonName string
	optional bool
	tsType   string
	doc      string
}

type structDef struct {
	name   string
	fields []field
}

type enumDef struct {
	name    string
	members []enumMember
}

type enumMember struct {
	value string // the Go string literal, unquoted
	doc   string // best-effort: the const's own name/comment, for a trailing note
}

// repoRoot is prefixed to every target path. It defaults to "." (run from the
// repo root) but is settable with -root so the tool does not depend on the
// caller's working directory: this package has its own go.mod, so `go run .`
// necessarily runs from scripts/wiregen/ and would otherwise never find
// backend/. `npm run gen:wire-types` passes -root ../.. for exactly that reason.
var repoRoot = flag.String("root", ".", "path to the repository root")

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wiregen: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, t := range targets {
		if _, ok := files[t.file]; ok {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(*repoRoot, t.file), nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parsing %s: %w (pass -root <repo root> if not running from it)", t.file, err)
		}
		files[t.file] = f
	}

	// known = every target type name, so a field whose type is one of them
	// resolves to a TS reference instead of an "unmapped type" error.
	known := map[string]bool{}
	for _, t := range targets {
		known[t.name] = true
	}

	var out strings.Builder
	writeHeader(&out)

	for _, t := range targets {
		f := files[t.file]
		spec, genDecl := findTypeSpec(f, t.name)
		if spec == nil {
			return fmt.Errorf("type %s not found (as a top-level type declaration) in %s", t.name, t.file)
		}

		switch ty := spec.Type.(type) {
		case *ast.StructType:
			sd, err := extractStruct(t.name, ty, known)
			if err != nil {
				return err
			}
			writeStruct(&out, sd)

		case *ast.Ident:
			if ty.Name != "string" {
				return fmt.Errorf("%s: only `type X string` enums are supported, got underlying type %q", t.name, ty.Name)
			}
			ed, err := extractEnum(f, t.name)
			if err != nil {
				return err
			}
			if len(ed.members) == 0 {
				return fmt.Errorf("%s: declared as a string type but no `const ... %s = \"...\"` values were found", t.name, t.name)
			}
			writeEnum(&out, ed)

		default:
			return fmt.Errorf("%s: unsupported type declaration kind %T (wiregen handles structs and string enums only)", t.name, ty)
		}
		_ = genDecl
	}

	fmt.Print(out.String())
	return nil
}

func writeHeader(out *strings.Builder) {
	out.WriteString(`// Code generated by scripts/wiregen (go run ./scripts/wiregen), invoked via
// ` + "`npm run gen:wire-types`" + `. DO NOT EDIT — edit the Go source below and
// regenerate instead.
//
// Source types (see the ` + "`targets`" + ` slice in scripts/wiregen/main.go to add more):
//   clusterHealthResponse   backend/cmd/server/health.go            GET /api/health
//   Endpoint, Status        backend/services/reach/reach.go         GET /api/network/reach -> "endpoints"
//   LinkState, LinkStatus   backend/services/reach/tunnel/agent.go  GET /api/network/reach -> "links"
//
// The /api/network/reach response itself is ` + "`{ enabled: boolean, endpoints: Status[], links: LinkStatus[] }`" + `
// — an inline Go map literal (reachwire.go), not a named type, so it has no
// generated declaration here; see registerReachStatus in
// backend/cmd/server/reachwire.go.
//
// How Go fields became these properties (encoding/json's own rules):
//   - the ` + "`json:\"name\"`" + ` tag is the wire property name (fields with no tag keep
//     their exact Go name, which none of these do); ` + "`json:\"-\"`" + ` fields are dropped
//   - ` + "`,omitempty`" + ` makes the TS property optional (` + "`?:`" + `) — Go omits the key
//     entirely rather than sending a zero value
//   - a Go ` + "`type X string`" + ` with a matching ` + "`const`" + ` block becomes a TS
//     string-literal union, not a bare ` + "`string`" + `
//   - ` + "`time.Time`" + ` encodes as an RFC3339 string via encoding/json's default
//     MarshalJSON — never a JS Date
//
`)
}

func findTypeSpec(f *ast.File, name string) (*ast.TypeSpec, *ast.GenDecl) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				continue
			}
			return ts, gd
		}
	}
	return nil, nil
}

func extractStruct(typeName string, st *ast.StructType, known map[string]bool) (structDef, error) {
	sd := structDef{name: typeName}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return sd, fmt.Errorf("%s: embedded/anonymous field is not supported by wiregen", typeName)
		}
		tagVal := ""
		if f.Tag != nil {
			unquoted, err := strconv.Unquote(f.Tag.Value)
			if err != nil {
				return sd, fmt.Errorf("%s: bad struct tag %s: %w", typeName, f.Tag.Value, err)
			}
			tagVal = unquoted
		}
		jsonTag := reflect.StructTag(tagVal).Get("json")

		tsType, err := resolveType(f.Type, known)
		if err != nil {
			return sd, fmt.Errorf("%s.%s: %w", typeName, f.Names[0].Name, err)
		}

		for _, nameIdent := range f.Names {
			jsonName := nameIdent.Name
			optional := false
			skip := false
			if jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if parts[0] == "-" && len(parts) == 1 {
					skip = true
				} else {
					if parts[0] != "" {
						jsonName = parts[0]
					}
					for _, p := range parts[1:] {
						if p == "omitempty" {
							optional = true
						}
					}
				}
			}
			if skip {
				continue
			}
			sd.fields = append(sd.fields, field{
				jsonName: jsonName,
				optional: optional,
				tsType:   tsType,
				doc:      docText(f.Doc),
			})
		}
	}
	return sd, nil
}

// resolveType maps a Go field-type AST expression to a TS type. It errors
// (rather than falling back to `any`/`unknown`) on anything outside this
// generator's deliberately small vocabulary, so an out-of-scope field fails
// generation instead of shipping a silently wrong type.
func resolveType(expr ast.Expr, known map[string]bool) (string, error) {
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return "string", nil
		case "bool":
			return "boolean", nil
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64",
			"float32", "float64", "byte", "rune":
			return "number", nil
		default:
			if known[t.Name] {
				return t.Name, nil
			}
			return "", fmt.Errorf("unmapped type %q (not a basic Go type and not in wiregen's `targets`)", t.Name)
		}

	case *ast.SelectorExpr:
		pkgIdent, ok := t.X.(*ast.Ident)
		if !ok {
			return "", fmt.Errorf("unsupported selector type %v", t)
		}
		sel := pkgIdent.Name + "." + t.Sel.Name
		switch sel {
		case "time.Time":
			// encoding/json's default (time.Time).MarshalJSON emits RFC3339.
			return "string", nil
		default:
			return "", fmt.Errorf("unmapped selector type %q — add a case to resolveType", sel)
		}

	case *ast.ArrayType:
		if t.Len != nil {
			return "", fmt.Errorf("fixed-size arrays are not supported by wiregen")
		}
		inner, err := resolveType(t.Elt, known)
		if err != nil {
			return "", err
		}
		return inner + "[]", nil

	case *ast.MapType:
		keyTS, err := resolveType(t.Key, known)
		if err != nil {
			return "", err
		}
		valTS, err := resolveType(t.Value, known)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Record<%s, %s>", keyTS, valTS), nil

	case *ast.StarExpr:
		inner, err := resolveType(t.X, known)
		if err != nil {
			return "", err
		}
		// encoding/json marshals a nil pointer as JSON null (or omits the key
		// entirely if the field also has `,omitempty`, handled by the caller).
		return inner + " | null", nil

	default:
		return "", fmt.Errorf("unmapped Go type expression %T", expr)
	}
}

func docText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	var lines []string
	for _, c := range cg.List {
		line := strings.TrimPrefix(c.Text, "//")
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, " ")
}

func extractEnum(f *ast.File, typeName string) (enumDef, error) {
	ed := enumDef{name: typeName}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typeIdent, ok := vs.Type.(*ast.Ident)
			if !ok || typeIdent.Name != typeName {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return ed, fmt.Errorf("%s: const %s is not a plain string literal — wiregen requires `const X %s = \"...\"`", typeName, name.Name, typeName)
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					return ed, fmt.Errorf("%s: const %s: %w", typeName, name.Name, err)
				}
				doc := docText(vs.Doc)
				note := name.Name
				if doc != "" {
					note = doc
				}
				ed.members = append(ed.members, enumMember{value: val, doc: note})
			}
		}
	}
	return ed, nil
}

// exportName capitalises an unexported Go type name (e.g. the package-private
// `clusterHealthResponse`) so it is a conventionally-named TS export. Exported
// Go names (Endpoint, Status, ...) pass through unchanged.
func exportName(goName string) string {
	if goName == "" {
		return goName
	}
	return strings.ToUpper(goName[:1]) + goName[1:]
}

func writeStruct(out *strings.Builder, sd structDef) {
	if exportName(sd.name) != sd.name {
		fmt.Fprintf(out, "// TS export capitalised from Go's unexported `%s`.\n", sd.name)
	}
	fmt.Fprintf(out, "export interface %s {\n", exportName(sd.name))
	for _, f := range sd.fields {
		if f.doc != "" {
			fmt.Fprintf(out, "  /** %s */\n", f.doc)
		}
		opt := ""
		if f.optional {
			opt = "?"
		}
		fmt.Fprintf(out, "  %s%s: %s\n", f.jsonName, opt, f.tsType)
	}
	out.WriteString("}\n\n")
}

func writeEnum(out *strings.Builder, ed enumDef) {
	fmt.Fprintf(out, "export type %s =\n", ed.name)
	for _, m := range ed.members {
		fmt.Fprintf(out, "  | %q // %s\n", m.value, m.doc)
	}
	out.WriteString("\n")
}
