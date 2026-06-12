package tools

import (
	"bufio"
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
)

func (r Registry) grepRegex(call Call) Result { return r.grepRegexStream(call, nil) }

func (r Registry) grepRegexStream(call Call, emit func(string)) Result {
	pattern := strings.TrimSpace(argString(call, "pattern"))
	if pattern == "" {
		return fail(call.Name, "pattern is required")
	}
	if !argBoolDefault(call, "case_sensitive", false) {
		pattern = "(?i)" + pattern
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return fail(call.Name, "invalid regular expression: "+err.Error())
	}
	return r.regexSearchStream(call, expression, emit)
}

func (r Registry) findSymbol(call Call) Result {
	symbol := strings.TrimSpace(argString(call, "symbol"))
	if symbol == "" {
		return fail(call.Name, "symbol is required")
	}
	quoted := regexp.QuoteMeta(symbol)
	pattern := `(?i)\b(?:func\s+(?:\([^\n)]*\)\s*)?|type\s+|class\s+|def\s+|interface\s+|struct\s+|enum\s+|(?:var|const|let)\s+)` + quoted + `\b`
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	result := r.regexSearch(call, expression)
	result.Tool = call.Name
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["symbol"] = symbol
	return result
}

func (r Registry) findRefs(call Call) Result {
	symbol := strings.TrimSpace(argString(call, "symbol"))
	if symbol == "" {
		return fail(call.Name, "symbol is required")
	}
	expression, err := regexp.Compile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	result := r.regexSearch(call, expression)
	result.Tool = call.Name
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["symbol"] = symbol
	return result
}

func (r Registry) regexSearch(call Call, expression *regexp.Regexp) Result {
	return r.regexSearchStream(call, expression, nil)
}

func (r Registry) regexSearchStream(call Call, expression *regexp.Regexp, emit func(string)) Result {
	root, err := r.safePath(argStringDefault(call, "path", "."))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	maxItems := argIntDefault(call, "max", 200)
	matches := make([]string, 0, maxItems)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() && shouldSkipDir(entry.Name()) && path != root {
			return filepath.SkipDir
		}
		if entry.IsDir() || looksBinary(path) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		display := r.displayPath(path)
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if expression.MatchString(line) {
				match := fmt.Sprintf("%s:%d:%s", display, lineNo, strings.TrimSpace(line))
				matches = append(matches, match)
				if emit != nil {
					emit(match + "\n")
				}
				if len(matches) >= maxItems {
					_ = file.Close()
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return fail(call.Name, err.Error())
	}
	result := ok(call.Name, strings.Join(matches, "\n"))
	result.Metadata = map[string]any{
		"path":    filepath.ToSlash(filepath.Clean(argStringDefault(call, "path", "."))),
		"matches": len(matches),
		"max":     maxItems,
	}
	return result
}

func (r Registry) fileSummary(call Call) Result {
	path, err := r.safePath(argString(call, "path"))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	display := r.displayPath(path)
	ext := strings.ToLower(filepath.Ext(path))
	var lines []string
	lines = append(lines, "File: "+display)
	if ext == ".go" {
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
		if parseErr != nil {
			return fail(call.Name, parseErr.Error())
		}
		lines = append(lines, "Package: "+file.Name.Name)
		var imports []string
		for _, item := range file.Imports {
			imports = append(imports, strings.Trim(item.Path.Value, `"`))
		}
		if len(imports) > 0 {
			lines = append(lines, "Imports: "+strings.Join(imports, ", "))
		}
		var definitions []string
		for _, declaration := range file.Decls {
			switch item := declaration.(type) {
			case *ast.FuncDecl:
				kind := "func"
				if item.Recv != nil {
					kind = "method"
				}
				definitions = append(definitions, kind+" "+item.Name.Name)
			case *ast.GenDecl:
				for _, spec := range item.Specs {
					switch value := spec.(type) {
					case *ast.TypeSpec:
						definitions = append(definitions, "type "+value.Name.Name)
					case *ast.ValueSpec:
						for _, name := range value.Names {
							definitions = append(definitions, strings.ToLower(item.Tok.String())+" "+name.Name)
						}
					}
				}
			}
		}
		if len(definitions) > 0 {
			lines = append(lines, "Definitions:", "- "+strings.Join(definitions, "\n- "))
		}
	} else {
		definitions := summarizeTextDefinitions(string(data), ext)
		if len(definitions) > 0 {
			lines = append(lines, "Definitions:", "- "+strings.Join(definitions, "\n- "))
		} else {
			lines = append(lines, fmt.Sprintf("Text source: %d lines, %d bytes", strings.Count(string(data), "\n")+1, len(data)))
		}
	}
	result := ok(call.Name, strings.Join(lines, "\n"))
	result.Metadata = map[string]any{"path": display, "bytes": len(data)}
	return result
}

func summarizeTextDefinitions(content, ext string) []string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?(?:function|class|interface|type|enum|const|let|var)\s+([A-Za-z_$][\w$]*)`),
		regexp.MustCompile(`(?m)^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`(?m)^\s*class\s+([A-Za-z_]\w*)`),
	}
	seen := map[string]bool{}
	var out []string
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(content, 200) {
			if len(match) < 2 || seen[match[1]] {
				continue
			}
			seen[match[1]] = true
			out = append(out, match[1])
		}
	}
	sort.Strings(out)
	return out
}

func (r Registry) dependencyGraph(call Call) Result {
	root, err := r.safePath(argStringDefault(call, "path", "."))
	if err != nil {
		return fail(call.Name, err.Error())
	}
	maxFiles := argIntDefault(call, "max", 250)
	var lines []string
	files := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() && shouldSkipDir(entry.Name()) && path != root {
			return filepath.SkipDir
		}
		if entry.IsDir() || files >= maxFiles {
			if files >= maxFiles {
				return filepath.SkipAll
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".go" && ext != ".py" && ext != ".js" && ext != ".jsx" && ext != ".ts" && ext != ".tsx" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		dependencies := sourceImports(path, data)
		if len(dependencies) == 0 {
			return nil
		}
		lines = append(lines, r.displayPath(path)+" -> "+strings.Join(dependencies, ", "))
		files++
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return fail(call.Name, err.Error())
	}
	result := ok(call.Name, strings.Join(lines, "\n"))
	result.Metadata = map[string]any{"files": files, "max": maxFiles}
	return result
}

func sourceImports(path string, data []byte) []string {
	ext := strings.ToLower(filepath.Ext(path))
	seen := map[string]bool{}
	var out []string
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if ext == ".go" {
		file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ImportsOnly|parser.SkipObjectResolution)
		if err == nil {
			for _, item := range file.Imports {
				add(item.Path.Value)
			}
		}
	} else if ext == ".py" {
		pattern := regexp.MustCompile(`(?m)^\s*(?:from\s+([^\s]+)\s+import|import\s+([^\n#]+))`)
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			value := firstNonEmpty(match[1], match[2])
			for _, part := range strings.Split(value, ",") {
				fields := strings.Fields(strings.TrimSpace(part))
				if len(fields) > 0 {
					add(fields[0])
				}
			}
		}
	} else {
		patterns := []*regexp.Regexp{
			regexp.MustCompile(`(?m)\bfrom\s+["']([^"']+)["']`),
			regexp.MustCompile(`(?m)\brequire\(\s*["']([^"']+)["']\s*\)`),
			regexp.MustCompile(`(?m)^\s*import\s+["']([^"']+)["']`),
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
				add(match[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

func (r Registry) detectProjectType(call Call) Result {
	type marker struct {
		file, description, test string
	}
	markers := []marker{
		{"go.mod", "Go module", "go test ./..."},
		{"package.json", "Node.js/JavaScript project", "npm test"},
		{"Cargo.toml", "Rust crate", "cargo test"},
		{"pyproject.toml", "Python project", "pytest"},
		{"requirements.txt", "Python requirements project", "pytest"},
		{"pom.xml", "Maven/Java project", "mvn test"},
		{"build.gradle", "Gradle/Java project", "gradle test"},
		{"Makefile", "Make-based project", "make test"},
	}
	var found []string
	var tests []string
	for _, item := range markers {
		if _, err := os.Stat(filepath.Join(r.WorkspaceRoot, item.file)); err == nil {
			found = append(found, item.description+" ("+item.file+")")
			tests = append(tests, item.test)
		}
	}
	if len(found) == 0 {
		found = append(found, "No recognized project manifest found")
	}
	output := "Detected:\n- " + strings.Join(found, "\n- ")
	if len(tests) > 0 {
		output += "\nSuggested verification:\n- " + strings.Join(uniqueStrings(tests), "\n- ")
	}
	result := ok(call.Name, output)
	result.Metadata = map[string]any{"project_types": found, "test_commands": uniqueStrings(tests)}
	return result
}

func (r Registry) listDependencies(call Call) Result {
	var sections []string
	if data, err := os.ReadFile(filepath.Join(r.WorkspaceRoot, "go.mod")); err == nil {
		deps := parseGoModDependencies(string(data))
		if len(deps) > 0 {
			sections = append(sections, "go.mod\n- "+strings.Join(deps, "\n- "))
		}
	}
	if data, err := os.ReadFile(filepath.Join(r.WorkspaceRoot, "package.json")); err == nil {
		var manifest struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &manifest) == nil {
			var deps []string
			for name, version := range manifest.Dependencies {
				deps = append(deps, name+" "+version)
			}
			for name, version := range manifest.DevDependencies {
				deps = append(deps, name+" "+version+" (dev)")
			}
			sort.Strings(deps)
			if len(deps) > 0 {
				sections = append(sections, "package.json\n- "+strings.Join(deps, "\n- "))
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(r.WorkspaceRoot, "requirements.txt")); err == nil {
		var deps []string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				deps = append(deps, line)
			}
		}
		if len(deps) > 0 {
			sections = append(sections, "requirements.txt\n- "+strings.Join(deps, "\n- "))
		}
	}
	if len(sections) == 0 {
		return ok(call.Name, "No supported dependency manifest with declared dependencies was found.")
	}
	result := ok(call.Name, strings.Join(sections, "\n\n"))
	result.Metadata = map[string]any{"manifests": len(sections)}
	return result
}

func parseGoModDependencies(content string) []string {
	var out []string
	inBlock := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "//", 2)[0])
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case strings.HasPrefix(line, "require "):
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "require ")))
		case inBlock && line != "":
			out = append(out, line)
		}
	}
	return out
}

func (r Registry) projectCommand(kind string) (string, error) {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(r.WorkspaceRoot, name))
		return err == nil
	}
	switch kind {
	case "lint":
		switch {
		case exists("go.mod"):
			return "go vet ./...", nil
		case exists("package.json"):
			return "npm run lint --if-present", nil
		case exists("pyproject.toml") || exists("requirements.txt"):
			return "python -m ruff check .", nil
		case exists("Cargo.toml"):
			return "cargo clippy --all-targets --all-features", nil
		}
	case "format":
		switch {
		case exists("go.mod"):
			return "go fmt ./...", nil
		case exists("package.json"):
			return "npm run format --if-present", nil
		case exists("pyproject.toml") || exists("requirements.txt"):
			return "python -m ruff format .", nil
		case exists("Cargo.toml"):
			return "cargo fmt", nil
		}
	case "audit":
		switch {
		case exists("package.json"):
			return "npm audit --audit-level=high", nil
		case exists("go.mod"):
			return "govulncheck ./...", nil
		case exists("Cargo.toml"):
			return "cargo audit", nil
		case exists("requirements.txt") || exists("pyproject.toml"):
			return "python -m pip_audit", nil
		}
	}
	return "", fmt.Errorf("no supported project command detected for %s", kind)
}

func validGitRef(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.Contains(value, "..") || strings.Contains(value, "//") {
		return false
	}
	matched, _ := regexp.MatchString(`^[A-Za-z0-9._/-]+$`, value)
	return matched
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
