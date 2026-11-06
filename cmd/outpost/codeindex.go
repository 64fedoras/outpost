package main

// Code index for large projects (roadmap row 17). Everything here takes a repo
// directory, so the index is keyed per task repo and can move to projects later.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	ignoreFileName  = ".outpostignore"
	maxDeclsPerFile = 60
	maxDeclLen      = 160
)

// ---- .outpostignore ----

type ignoreRule struct {
	pat      string
	neg      bool
	dirOnly  bool
	anchored bool
}

type ignoreSet struct{ rules []ignoreRule }

// loadIgnore reads <repo>/.outpostignore on top of the built-in defaults. The
// syntax is a subset of .gitignore: comments, "!" negation, trailing "/" for
// directories, a "/" inside or at the start anchors the pattern, "*" "?" "[]"
// globs, and a leading "**/" or trailing "/**".
func loadIgnore(repo string) *ignoreSet {
	s := &ignoreSet{}
	s.add(".git/")
	s.add("node_modules/")
	if data, err := os.ReadFile(filepath.Join(repo, ignoreFileName)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			s.add(line)
		}
	}
	return s
}

func (s *ignoreSet) add(line string) {
	line = strings.TrimSpace(strings.TrimRight(line, "\r"))
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}
	var r ignoreRule
	if strings.HasPrefix(line, "!") {
		r.neg = true
		line = line[1:]
	}
	line = strings.TrimSuffix(line, "/**")
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	line = strings.TrimPrefix(line, "**/")
	if strings.HasPrefix(line, "/") {
		r.anchored = true
		line = line[1:]
	} else if strings.Contains(line, "/") {
		r.anchored = true
	}
	if line == "" {
		return
	}
	r.pat = line
	s.rules = append(s.rules, r)
}

// ignored reports whether the slash-separated file path rel is hidden from
// prompts. A file inside an ignored directory is ignored.
func (s *ignoreSet) ignored(rel string) bool {
	comps := strings.Split(rel, "/")
	for i := range comps {
		prefix := strings.Join(comps[:i+1], "/")
		isDir := i < len(comps)-1
		hit := false
		for _, r := range s.rules {
			if r.dirOnly && !isDir {
				continue
			}
			target := comps[i]
			if r.anchored {
				target = prefix
			}
			if ok, _ := path.Match(r.pat, target); ok {
				hit = !r.neg
			}
		}
		if hit {
			return true
		}
	}
	return false
}

// ---- index ----

type indexEntry struct {
	Path    string
	Bytes   int64
	Summary string
	Decls   []string
}

// textFile reports whether data looks like source text.
func textFile(data []byte) bool {
	n := min(len(data), 512)
	return !strings.Contains(string(data[:n]), "\x00")
}

// promptFiles lists the repo files that may appear in prompts: not ignored and
// not too large. Builds and tests still see every file.
func promptFiles(repo string) []repoFile {
	ig := loadIgnore(repo)
	var out []repoFile
	for _, f := range listRepo(repo) {
		if f.Bytes <= maxRepoFileSize && !ig.ignored(f.Path) {
			out = append(out, f)
		}
	}
	return out
}

// repoNeedsIndex reports whether the prompt-visible files are too big to paste
// whole into half the budget.
func repoNeedsIndex(repo string, budget int) bool {
	var total int64
	for _, f := range promptFiles(repo) {
		total += f.Bytes
	}
	return total > int64(budget/2)
}

// buildIndex indexes every prompt-visible text file in repo.
func buildIndex(repo string) []indexEntry {
	var out []indexEntry
	for _, f := range promptFiles(repo) {
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f.Path)))
		if err != nil || !textFile(data) {
			continue
		}
		out = append(out, indexEntry{Path: f.Path, Bytes: f.Bytes, Summary: leadSummary(string(data)), Decls: declarations(f.Path, data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// renderIndex formats the index to fit budget bytes: with declarations when
// they fit, else paths only, else cut off with a count of what was left out.
func renderIndex(idx []indexEntry, budget int) string {
	for _, withDecls := range []bool{true, false} {
		var b strings.Builder
		for i, e := range idx {
			start := b.Len()
			fmt.Fprintf(&b, "%s (%d bytes)", e.Path, e.Bytes)
			if e.Summary != "" {
				b.WriteString(": " + e.Summary)
			}
			b.WriteString("\n")
			if withDecls {
				for _, d := range e.Decls {
					b.WriteString("    " + d + "\n")
				}
			}
			if b.Len() > budget {
				if !withDecls {
					s := b.String()[:start]
					return s + fmt.Sprintf("…%d more files not listed\n", len(idx)-i)
				}
				break
			}
			if i == len(idx)-1 {
				return b.String()
			}
		}
	}
	return ""
}

// declsText is the declarations of one file for a worker prompt; files with no
// parser get a short head instead.
func declsText(repo, rel string) string {
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil || !textFile(data) {
		return ""
	}
	d := declarations(rel, data)
	if len(d) == 0 {
		s := string(data)
		if len(s) > 1500 {
			s = s[:1500] + "\n…(truncated)\n"
		}
		return s
	}
	return strings.Join(d, "\n") + "\n"
}

// leadSummary is the first comment line of a file, a cheap one-line summary.
func leadSummary(src string) string {
	for i, line := range strings.Split(src, "\n") {
		if i > 12 {
			break
		}
		t := strings.TrimSpace(line)
		for _, p := range []string{"//", "/*", "*", "#", `"""`, "--"} {
			if strings.HasPrefix(t, p) {
				t = strings.TrimSpace(strings.TrimPrefix(t, p))
				if t == "" || strings.HasPrefix(t, "go:") || strings.HasPrefix(t, "+build") || strings.HasPrefix(t, "!") {
					t = ""
				}
				if t != "" {
					return firstLine(t, 120)
				}
				break
			}
		}
	}
	return ""
}

var (
	jsDecl = []*regexp.Regexp{
		regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*\w+\s*\([^)]*\)?`),
		regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?class\s+\w+[^{]*`),
		regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+\w+\s*=\s*(?:async\s*)?(?:function\b[^{]*|\([^)]*\)\s*=>|\w+\s*=>)`),
	}
	pyDecl  = regexp.MustCompile(`^(?:async\s+def|def|class)\s+\w+[^:]*`)
	cssDecl = regexp.MustCompile(`^([^\s@}/][^{]*)\{`)
)

func clipDecl(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxDeclLen {
		s = s[:maxDeclLen] + "…"
	}
	return s
}

// declarations lists the top-level functions, types and classes of a file.
// Go uses the real parser; JS, Python and CSS use line patterns.
func declarations(rel string, data []byte) []string {
	var out []string
	switch strings.ToLower(path.Ext(rel)) {
	case ".go":
		out = goDecls(rel, data)
	case ".js", ".mjs", ".jsx", ".ts", ".tsx":
		for _, line := range strings.Split(string(data), "\n") {
			for _, re := range jsDecl {
				if m := re.FindString(line); m != "" {
					out = append(out, clipDecl(m))
					break
				}
			}
		}
	case ".py":
		for _, line := range strings.Split(string(data), "\n") {
			if m := pyDecl.FindString(line); m != "" {
				out = append(out, clipDecl(m))
			}
		}
	case ".css":
		for _, line := range strings.Split(string(data), "\n") {
			if m := cssDecl.FindStringSubmatch(line); m != nil {
				out = append(out, clipDecl(m[1]))
			}
		}
	}
	if len(out) > maxDeclsPerFile {
		n := len(out) - maxDeclsPerFile
		out = append(out[:maxDeclsPerFile], fmt.Sprintf("…%d more", n))
	}
	return out
}

func goDecls(rel string, src []byte) []string {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if f == nil {
		return nil
	}
	text := func(a, b token.Pos) string {
		i, j := fset.Position(a).Offset, fset.Position(b).Offset
		if i < 0 || j > len(src) || i > j {
			return "" // damaged source: positions can run past the end
		}
		return string(src[i:j])
	}
	var out []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			end := d.Type.End()
			if t := clipDecl(text(d.Pos(), end)); t != "" {
				out = append(out, t)
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					kind := ""
					switch sp.Type.(type) {
					case *ast.StructType:
						kind = " struct"
					case *ast.InterfaceType:
						kind = " interface"
					default:
						kind = " " + clipDecl(text(sp.Type.Pos(), sp.Type.End()))
					}
					out = append(out, clipDecl("type "+sp.Name.Name+kind))
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if n.IsExported() {
							out = append(out, d.Tok.String()+" "+n.Name)
						}
					}
				}
			}
		}
	}
	return out
}

// wantedFiles returns the named prompt-visible files in the file format, up to
// budget bytes. Unknown, ignored or unsafe paths are skipped.
func wantedFiles(repo string, want []string, budget int) string {
	ig := loadIgnore(repo)
	var b strings.Builder
	for i, rel := range want {
		if i >= 12 {
			break
		}
		rel = strings.TrimPrefix(path.Clean(filepath.ToSlash(rel)), "./")
		p, err := safeJoin(repo, rel)
		if err != nil || ig.ignored(rel) {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil || !textFile(data) || len(data) > maxRepoFileSize {
			continue
		}
		s := string(data)
		if b.Len()+len(s) > budget {
			fmt.Fprintf(&b, "<<<FILE %s>>>\n(omitted: too large for this prompt)\n<<<END>>>\n", rel)
			continue
		}
		fmt.Fprintf(&b, "<<<FILE %s>>>\n%s<<<END>>>\n", rel, s)
	}
	return b.String()
}
