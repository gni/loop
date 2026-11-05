package resources

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"loop/pkg/domain/ignore"
	"loop/pkg/storage"
)

type ProjectMetadata struct {
	Stack          string
	TestCommand    string
	BuildCommand   string
	GitBranch      string
	KeyDirectories []string
	KeyConfigs     []string
}

type cachedProjectContext struct {
	checkedAt time.Time
	context   string
}

var (
	projectContextMu    sync.Mutex
	projectContextCache = make(map[string]cachedProjectContext)
)

// DetectGitBranch reads the current git branch name from the directory or its ancestors.
func DetectGitBranch(workspaceRoot string) string {
	curr, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return ""
	}
	for {
		gitPath := filepath.Join(curr, ".git")
		fi, err := os.Stat(gitPath)
		if err == nil {
			if fi.IsDir() {
				return readHeadBranch(filepath.Join(gitPath, "HEAD"))
			}
			data, err := storage.ReadFile(gitPath, 16<<10)
			if err == nil {
				text := strings.TrimSpace(string(data))
				if strings.HasPrefix(text, "gitdir:") {
					targetDir := strings.TrimSpace(strings.TrimPrefix(text, "gitdir:"))
					if !filepath.IsAbs(targetDir) {
						targetDir = filepath.Join(curr, targetDir)
					}
					return readHeadBranch(filepath.Join(targetDir, "HEAD"))
				}
			}
		}
		parent := filepath.Dir(curr)
		if parent == curr || parent == "." {
			break
		}
		curr = parent
	}
	return ""
}

func readHeadBranch(headFile string) string {
	data, err := storage.ReadFile(headFile, 16<<10)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if strings.HasPrefix(content, "ref: refs/heads/") {
		return strings.TrimPrefix(content, "ref: refs/heads/")
	}
	if strings.HasPrefix(content, "ref: ") {
		return strings.TrimPrefix(content, "ref: ")
	}
	if len(content) >= 7 {
		return content[:7]
	}
	return content
}

// DetectProjectContext introspects the workspace root to produce a concise technical summary
// of the project stack, primary test runner, git branch, and top-level directory layout.
func DetectProjectContext(workspaceRoot string) string {
	if workspaceRoot == "" {
		workspaceRoot = "."
	}
	cleanRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return ""
	}

	projectContextMu.Lock()
	if cached, ok := projectContextCache[cleanRoot]; ok && time.Since(cached.checkedAt) < 3*time.Second {
		projectContextMu.Unlock()
		return cached.context
	}
	projectContextMu.Unlock()

	meta := inspectProject(cleanRoot)
	var sb strings.Builder

	if meta.GitBranch != "" {
		fmt.Fprintf(&sb, "Git Branch: %s\n", meta.GitBranch)
	}
	if meta.Stack != "" {
		fmt.Fprintf(&sb, "Tech Stack: %s\n", meta.Stack)
	}
	if meta.TestCommand != "" {
		fmt.Fprintf(&sb, "Primary Test Command: %s\n", meta.TestCommand)
	}
	if meta.BuildCommand != "" {
		fmt.Fprintf(&sb, "Primary Build Command: %s\n", meta.BuildCommand)
	}
	if len(meta.KeyDirectories) > 0 {
		fmt.Fprintf(&sb, "Key Directories: %s\n", strings.Join(meta.KeyDirectories, ", "))
	}
	if len(meta.KeyConfigs) > 0 {
		fmt.Fprintf(&sb, "Key Configs: %s\n", strings.Join(meta.KeyConfigs, ", "))
	}

	result := strings.TrimSpace(sb.String())
	projectContextMu.Lock()
	projectContextCache[cleanRoot] = cachedProjectContext{checkedAt: time.Now(), context: result}
	projectContextMu.Unlock()

	return result
}

func inspectProject(root string) ProjectMetadata {
	var meta ProjectMetadata
	meta.GitBranch = DetectGitBranch(root)

	entries, err := os.ReadDir(root)
	if err != nil {
		return meta
	}

	hasFile := make(map[string]bool)
	var dirs []string
	var configs []string

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if !ignore.IsDirName(name, true) {
				dirs = append(dirs, name)
			}
		} else {
			hasFile[name] = true
			switch name {
			case "Makefile", "Dockerfile", "docker-compose.yml", "docker-compose.yaml",
				"CMakeLists.txt", "tsconfig.json", "go.mod", "Cargo.toml", "package.json",
				"pyproject.toml", "setup.py", "requirements.txt", "pom.xml", "build.gradle",
				"build.gradle.kts", "build.zig", "Gemfile", "composer.json":
				configs = append(configs, name)
			}
		}
	}

	sort.Strings(dirs)
	sort.Strings(configs)
	if len(dirs) > 10 {
		dirs = dirs[:10]
	}
	meta.KeyDirectories = dirs
	meta.KeyConfigs = configs

	// Go project
	if hasFile["go.mod"] {
		modName, goVersion := parseGoMod(filepath.Join(root, "go.mod"))
		if modName != "" {
			meta.Stack = fmt.Sprintf("Go (module: %s, go: %s)", modName, goVersion)
		} else {
			meta.Stack = "Go"
		}
		meta.TestCommand = "go test ./..."
		meta.BuildCommand = "go build ./..."
		return meta
	}

	// Rust project
	if hasFile["Cargo.toml"] {
		packageName, edition := parseCargoToml(filepath.Join(root, "Cargo.toml"))
		if packageName != "" {
			meta.Stack = fmt.Sprintf("Rust (crate: %s, edition: %s)", packageName, edition)
		} else {
			meta.Stack = "Rust"
		}
		meta.TestCommand = "cargo test"
		meta.BuildCommand = "cargo build"
		return meta
	}

	// Node / TypeScript project
	if hasFile["package.json"] {
		packageManager := "npm"
		if hasFile["pnpm-lock.yaml"] {
			packageManager = "pnpm"
		} else if hasFile["yarn.lock"] {
			packageManager = "yarn"
		} else if hasFile["bun.lockb"] || hasFile["bun.lock"] {
			packageManager = "bun"
		}

		isTypeScript := hasFile["tsconfig.json"]
		packageName, hasTestScript, hasBuildScript := parsePackageJSON(filepath.Join(root, "package.json"))
		runtimeName := "Node.js"
		if isTypeScript {
			runtimeName = "TypeScript / Node.js"
		}
		if packageName != "" {
			meta.Stack = fmt.Sprintf("%s (package: %s, runner: %s)", runtimeName, packageName, packageManager)
		} else {
			meta.Stack = fmt.Sprintf("%s (runner: %s)", runtimeName, packageManager)
		}

		if hasTestScript {
			meta.TestCommand = fmt.Sprintf("%s test", packageManager)
		}
		if hasBuildScript {
			meta.BuildCommand = fmt.Sprintf("%s run build", packageManager)
		}
		return meta
	}

	// Python project
	if hasFile["pyproject.toml"] || hasFile["setup.py"] || hasFile["requirements.txt"] || hasFile["Pipfile"] {
		meta.Stack = "Python"
		meta.TestCommand = "pytest"
		return meta
	}

	// C / C++ project
	if hasFile["CMakeLists.txt"] {
		meta.Stack = "C/C++ (CMake)"
		meta.BuildCommand = "cmake -B build && cmake --build build"
		meta.TestCommand = "ctest --test-dir build"
		return meta
	}
	if hasFile["Makefile"] {
		meta.Stack = "Make / C / Polyglot"
		meta.BuildCommand = "make"
		meta.TestCommand = "make test"
		return meta
	}

	// Java / Kotlin project
	if hasFile["pom.xml"] {
		meta.Stack = "Java (Maven)"
		meta.TestCommand = "mvn test"
		meta.BuildCommand = "mvn package"
		return meta
	}
	if hasFile["build.gradle"] || hasFile["build.gradle.kts"] {
		meta.Stack = "Java/Kotlin (Gradle)"
		meta.TestCommand = "./gradlew test"
		meta.BuildCommand = "./gradlew build"
		return meta
	}

	// Zig project
	if hasFile["build.zig"] {
		meta.Stack = "Zig"
		meta.TestCommand = "zig build test"
		meta.BuildCommand = "zig build"
		return meta
	}

	return meta
}

func parseGoMod(path string) (moduleName, goVersion string) {
	data, err := storage.ReadFile(path, 1<<20)
	if err != nil {
		return "", ""
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			moduleName = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		} else if strings.HasPrefix(line, "go ") {
			goVersion = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
		if moduleName != "" && goVersion != "" {
			break
		}
	}
	return moduleName, goVersion
}

func parseCargoToml(path string) (packageName, edition string) {
	data, err := storage.ReadFile(path, 1<<20)
	if err != nil {
		return "", ""
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	inPackage := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[package]" {
			inPackage = true
			continue
		}
		if strings.HasPrefix(line, "[") && line != "[package]" {
			inPackage = false
			continue
		}
		if inPackage {
			if strings.HasPrefix(line, "name =") {
				packageName = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name =")), "\"")
			} else if strings.HasPrefix(line, "edition =") {
				edition = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "edition =")), "\"")
			}
		}
		if packageName != "" && edition != "" {
			break
		}
	}
	return packageName, edition
}

func parsePackageJSON(path string) (packageName string, hasTestScript, hasBuildScript bool) {
	data, err := storage.ReadFile(path, 1<<20)
	if err != nil {
		return "", false, false
	}
	var manifest struct {
		Name    string            `json:"name"`
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", false, false
	}
	if manifest.Scripts != nil {
		if testScript, ok := manifest.Scripts["test"]; ok && testScript != "" && !strings.Contains(testScript, "no test specified") {
			hasTestScript = true
		}
		if buildScript, ok := manifest.Scripts["build"]; ok && buildScript != "" {
			hasBuildScript = true
		}
	}
	return manifest.Name, hasTestScript, hasBuildScript
}
