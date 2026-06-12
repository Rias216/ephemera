package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const codebaseIndexVersion = 2

type codebaseIndex struct {
	Version     int                      `json:"version"`
	GeneratedAt time.Time                `json:"generated_at"`
	Entries     map[string]codebaseEntry `json:"entries"`
}

type codebaseEntry struct {
	Path        string    `json:"path"`
	Language    string    `json:"language,omitempty"`
	Package     string    `json:"package,omitempty"`
	Definitions []string  `json:"definitions,omitempty"`
	Imports     []string  `json:"imports,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Embedding   []float32 `json:"embedding,omitempty"`
	Size        int64     `json:"size"`
	ModUnixNano int64     `json:"mod_unix_nano"`
}

type codebaseIndexManager struct {
	mu     sync.Mutex
	root   string
	path   string
	loaded bool
	index  codebaseIndex
}

func newCodebaseIndexManager(root string) *codebaseIndexManager {
	return &codebaseIndexManager{
		root:  root,
		path:  filepath.Join(root, ".ephemera", "codebase-index.json"),
		index: codebaseIndex{Version: codebaseIndexVersion, Entries: map[string]codebaseEntry{}},
	}
}

func (m *codebaseIndexManager) Relevant(query string, maxEntries int) string {
	if m == nil || strings.TrimSpace(m.root) == "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureLoaded()
	m.refresh()
	if maxEntries <= 0 {
		maxEntries = 24
	}
	terms := semanticTerms(query)
	queryVector, _ := embedText(context.Background(), defaultEmbedder(), query)
	type scored struct {
		entry codebaseEntry
		score float64
	}
	items := make([]scored, 0, len(m.index.Entries))
	for _, entry := range m.index.Entries {
		haystack := strings.Join([]string{entry.Path, entry.Package, strings.Join(entry.Definitions, " "), strings.Join(entry.Imports, " "), entry.Summary}, " ")
		score := cosineSimilarity(queryVector, entry.Embedding)
		if lexical := semanticScore(haystack, terms); lexical > 0 {
			score += float64(lexical) * 0.08
		}
		if len(terms) == 0 && vectorIsZero(queryVector) {
			score = 1
		}
		if score > 0.05 {
			items = append(items, scored{entry: entry, score: score})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].entry.Path < items[j].entry.Path
	})
	if len(items) > maxEntries {
		items = items[:maxEntries]
	}
	if len(items) == 0 {
		return ""
	}
	var lines []string
	for _, item := range items {
		entry := item.entry
		line := "- " + entry.Path
		if entry.Package != "" {
			line += " [" + entry.Package + "]"
		}
		if len(entry.Definitions) > 0 {
			line += ": " + strings.Join(limitStrings(entry.Definitions, 10), ", ")
		} else if entry.Summary != "" {
			line += ": " + entry.Summary
		}
		lines = append(lines, compact(line, 560))
	}
	return strings.Join(lines, "\n")
}

func (m *codebaseIndexManager) ensureLoaded() {
	if m.loaded {
		return
	}
	m.loaded = true
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var loaded codebaseIndex
	if json.Unmarshal(data, &loaded) == nil && loaded.Version == codebaseIndexVersion {
		if loaded.Entries == nil {
			loaded.Entries = map[string]codebaseEntry{}
		}
		m.index = loaded
	}
}

func (m *codebaseIndexManager) refresh() {
	if m.index.Entries == nil {
		m.index.Entries = map[string]codebaseEntry{}
	}
	seen := map[string]bool{}
	changed := false
	count := 0
	_ = filepath.WalkDir(m.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != m.root && skipIndexDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= 2500 || !indexableSource(path) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		rel, err := filepath.Rel(m.root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		count++
		previous, ok := m.index.Entries[rel]
		if ok && previous.Size == info.Size() && previous.ModUnixNano == info.ModTime().UnixNano() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		m.index.Entries[rel] = buildCodebaseEntry(rel, path, data, info)
		changed = true
		return nil
	})
	for path := range m.index.Entries {
		if !seen[path] {
			delete(m.index.Entries, path)
			changed = true
		}
	}
	if !changed {
		return
	}
	m.index.GeneratedAt = time.Now().UTC()
	m.persist()
}

func (m *codebaseIndexManager) persist() {
	if os.MkdirAll(filepath.Dir(m.path), 0o700) != nil {
		return
	}
	data, err := json.MarshalIndent(m.index, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	tmp := m.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, m.path)
	}
}

func buildCodebaseEntry(rel, path string, data []byte, info fs.FileInfo) codebaseEntry {
	entry := codebaseEntry{
		Path:        rel,
		Language:    sourceLanguage(filepath.Ext(path)),
		Size:        info.Size(),
		ModUnixNano: info.ModTime().UnixNano(),
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".go" {
		file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
		if err == nil {
			entry.Package = file.Name.Name
			for _, item := range file.Imports {
				entry.Imports = append(entry.Imports, strings.Trim(item.Path.Value, `"`))
			}
			for _, declaration := range file.Decls {
				switch item := declaration.(type) {
				case *ast.FuncDecl:
					kind := "func "
					if item.Recv != nil {
						kind = "method "
					}
					entry.Definitions = append(entry.Definitions, kind+item.Name.Name)
				case *ast.GenDecl:
					for _, spec := range item.Specs {
						switch value := spec.(type) {
						case *ast.TypeSpec:
							entry.Definitions = append(entry.Definitions, "type "+value.Name.Name)
						case *ast.ValueSpec:
							for _, name := range value.Names {
								entry.Definitions = append(entry.Definitions, strings.ToLower(item.Tok.String())+" "+name.Name)
							}
						}
					}
				}
			}
		}
	} else {
		entry.Definitions = indexTextDefinitions(string(data))
		entry.Imports = indexTextImports(string(data), ext)
	}
	sort.Strings(entry.Definitions)
	sort.Strings(entry.Imports)
	entry.Summary = fmt.Sprintf("%s source, %d lines", entry.Language, strings.Count(string(data), "\n")+1)
	embeddingText := strings.Join([]string{entry.Path, entry.Package, strings.Join(entry.Definitions, " "), strings.Join(entry.Imports, " "), entry.Summary}, " ")
	entry.Embedding, _ = embedText(context.Background(), defaultEmbedder(), embeddingText)
	return entry
}

var indexDefinitionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?(?:function|class|interface|type|enum|const|let|var)\s+([A-Za-z_$][\w$]*)`),
	regexp.MustCompile(`(?m)^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`),
}

func indexTextDefinitions(content string) []string {
	seen := map[string]bool{}
	var out []string
	for _, pattern := range indexDefinitionPatterns {
		for _, match := range pattern.FindAllStringSubmatch(content, 200) {
			if len(match) > 1 && !seen[match[1]] {
				seen[match[1]] = true
				out = append(out, match[1])
			}
		}
	}
	return out
}

func indexTextImports(content, ext string) []string {
	var patterns []*regexp.Regexp
	if ext == ".py" {
		patterns = []*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*from\s+([^\s]+)\s+import`),
			regexp.MustCompile(`(?m)^\s*import\s+([^\n#]+)`),
		}
	} else {
		patterns = []*regexp.Regexp{
			regexp.MustCompile(`(?m)\bfrom\s+["']([^"']+)["']`),
			regexp.MustCompile(`(?m)\brequire\(\s*["']([^"']+)["']\s*\)`),
			regexp.MustCompile(`(?m)^\s*import\s+["']([^"']+)["']`),
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(content, 200) {
			if len(match) < 2 {
				continue
			}
			for _, value := range strings.Split(match[1], ",") {
				fields := strings.Fields(strings.TrimSpace(value))
				if len(fields) == 0 {
					continue
				}
				value = fields[0]
				if value != "" && !seen[value] {
					seen[value] = true
					out = append(out, value)
				}
			}
		}
	}
	return out
}

func indexableSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".rs", ".java", ".kt", ".c", ".cc", ".cpp", ".h", ".hpp":
		return true
	default:
		return false
	}
}

func sourceLanguage(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".js", ".jsx":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".kt":
		return "Kotlin"
	case ".c", ".h":
		return "C"
	case ".cc", ".cpp", ".hpp":
		return "C++"
	default:
		return "source"
	}
}

func skipIndexDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".ephemera", "node_modules", "vendor", "dist", "build", "target", ".next", ".cache", "coverage", "__pycache__", ".venv", "venv":
		return true
	default:
		return false
	}
}

func limitStrings(values []string, maxItems int) []string {
	if len(values) <= maxItems {
		return values
	}
	out := append([]string(nil), values[:maxItems]...)
	out = append(out, fmt.Sprintf("+%d more", len(values)-maxItems))
	return out
}
