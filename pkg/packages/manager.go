package packages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/config"
	"loop/pkg/process"
	"loop/pkg/storage"
)

const maxRegistryBytes = 1 << 20

type Package struct {
	Source    string `json:"source"`
	Directory string `json:"directory"`
	Revision  string `json:"revision,omitempty"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
}

type Manifest struct {
	APIVersion int                              `json:"api_version"`
	Providers  map[string]config.ProviderConfig `json:"providers"`
	Name       string                           `json:"name"`
	Version    string                           `json:"version"`
	Skills     []string                         `json:"skills"`
	Prompts    []string                         `json:"prompts"`
	Plugins    []string                         `json:"plugins"`
	Extensions []string                         `json:"extensions"`
	Hooks      map[string]string                `json:"hooks"`
}

type Manager struct{ Directory string }

func (m Manager) List() ([]Package, error) {
	data, err := storage.ReadFile(filepath.Join(m.Directory, "packages.json"), maxRegistryBytes)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var packages []Package
	if err := json.Unmarshal(data, &packages); err != nil {
		return nil, err
	}
	return packages, nil
}

func encodeRegistry(packages []Package) ([]byte, error) {
	data, err := json.MarshalIndent(packages, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data) > maxRegistryBytes {
		return nil, fmt.Errorf("package registry exceeds 1 MiB")
	}
	return data, nil
}

func (m Manager) save(ctx context.Context, packages []Package) error {
	data, err := encodeRegistry(packages)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return config.WritePrivateFile(filepath.Join(m.Directory, "packages.json"), data)
}

func (m Manager) Install(ctx context.Context, source string) (Package, error) {
	if source == "" {
		return Package{}, fmt.Errorf("package source is empty")
	}
	release, err := storage.LockContext(ctx, filepath.Join(m.Directory, ".packages.lock"), true)
	if err != nil {
		return Package{}, err
	}
	defer release()
	entries, err := m.List()
	if err != nil {
		return Package{}, err
	}
	sum := sha256.Sum256([]byte(source))
	key := hex.EncodeToString(sum[:16])
	destination := filepath.Join(m.Directory, "packages", key)
	installed := Package{Source: source, Directory: destination}
	preparedDirectory := ""
	if strings.HasPrefix(source, "git:") || strings.HasPrefix(source, "https://") {
		remote := strings.TrimPrefix(source, "git:")
		remote, ref, _ := strings.Cut(remote, "#")
		if !strings.HasPrefix(remote, "https://") && !strings.HasPrefix(remote, "ssh://") {
			return installed, fmt.Errorf("git package source must use HTTPS or SSH")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return installed, err
		}
		staging, err := os.MkdirTemp(filepath.Dir(destination), ".install-*")
		if err != nil {
			return installed, err
		}
		defer os.RemoveAll(staging)
		if _, err := command(ctx, "git", "clone", "--no-checkout", "--", remote, staging); err != nil {
			return installed, err
		}
		if ref == "" {
			ref = "HEAD"
		}
		if strings.HasPrefix(ref, "-") {
			return installed, fmt.Errorf("invalid git ref")
		}
		resolved, err := command(ctx, "git", "-C", staging, "rev-parse", "--verify", ref+"^{commit}")
		if err != nil {
			return installed, err
		}
		installed.Revision = strings.TrimSpace(resolved)
		if _, err := command(ctx, "git", "-C", staging, "checkout", "--detach", installed.Revision); err != nil {
			return installed, err
		}
		manifest, err := ReadManifestContext(ctx, staging)
		if err != nil {
			return installed, err
		}
		installed.Name, installed.Version = manifest.Name, manifest.Version
		preparedDirectory = staging
	} else if strings.HasPrefix(source, "npm:") {
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return installed, err
		}
		staging, err := os.MkdirTemp(filepath.Dir(destination), ".npm-*")
		if err != nil {
			return installed, err
		}
		defer os.RemoveAll(staging)
		output, err := command(ctx, "npm", "pack", "--ignore-scripts", "--json", "--pack-destination", staging, "--", strings.TrimPrefix(source, "npm:"))
		if err != nil {
			return installed, err
		}
		var packed []struct {
			Filename  string `json:"filename"`
			Integrity string `json:"integrity"`
		}
		if err := json.Unmarshal([]byte(output), &packed); err != nil || len(packed) != 1 {
			return installed, fmt.Errorf("invalid npm pack response")
		}
		if filepath.Base(packed[0].Filename) != packed[0].Filename {
			return installed, fmt.Errorf("invalid npm archive name")
		}
		if err := unpack(ctx, filepath.Join(staging, packed[0].Filename), filepath.Join(staging, "content")); err != nil {
			return installed, err
		}
		manifest, err := ReadManifestContext(ctx, filepath.Join(staging, "content"))
		if err != nil {
			return installed, err
		}
		installed.Name, installed.Version, installed.Revision = manifest.Name, manifest.Version, packed[0].Integrity
		preparedDirectory = filepath.Join(staging, "content")
	} else {
		local := strings.TrimPrefix(source, "local:")
		installed.Directory, err = config.CanonicalWorkspace(local)
		if err != nil {
			return installed, err
		}
		manifest, err := ReadManifestContext(ctx, installed.Directory)
		if err != nil {
			return installed, err
		}
		installed.Name, installed.Version = manifest.Name, manifest.Version
	}
	found := false
	for index, entry := range entries {
		if entry.Source == source {
			entries[index] = installed
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, installed)
	}
	if err := ctx.Err(); err != nil {
		return installed, err
	}
	return installed, m.publishInstall(ctx, entries, preparedDirectory, destination)
}

func (m Manager) Remove(source string) error {
	return m.RemoveContext(context.Background(), source)
}

// RemoveContext allows cancellation while waiting for the package registry.
func (m Manager) RemoveContext(ctx context.Context, source string) error {
	if source == "" {
		return fmt.Errorf("package source is empty")
	}
	release, err := storage.LockContext(ctx, filepath.Join(m.Directory, ".packages.lock"), true)
	if err != nil {
		return err
	}
	defer release()
	entries, err := m.List()
	if err != nil {
		return err
	}
	var retained []Package
	found := false
	for _, entry := range entries {
		if entry.Source == source || entry.Name == source {
			found = true
		} else {
			retained = append(retained, entry)
		}
	}
	if !found {
		return fmt.Errorf("package %q is not installed", source)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.save(ctx, retained)
}

func (m Manager) Update(ctx context.Context, source string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := m.List()
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if source != "" && source != entry.Source && source != entry.Name {
			continue
		}
		found = true
		if _, err := m.Install(ctx, entry.Source); err != nil {
			return err
		}
	}
	if !found && source != "" {
		return fmt.Errorf("package %q is not installed", source)
	}
	return nil
}

func ReadManifest(directory string) (Manifest, error) {
	return ReadManifestContext(context.Background(), directory)
}

// ReadManifestContext stops resource resolution when startup or installation is canceled.
func ReadManifestContext(ctx context.Context, directory string) (Manifest, error) {
	var manifest Manifest
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	data, err := storage.ReadFile(filepath.Join(directory, "loop.json"), 1<<20)
	if os.IsNotExist(err) {
		data, err = storage.ReadFile(filepath.Join(directory, "package.json"), 1<<20)
		if os.IsNotExist(err) {
			return manifest, fmt.Errorf("package needs loop.json or package.json: %w", err)
		}
		if err != nil {
			return manifest, fmt.Errorf("read package.json: %w", err)
		}
		var npm struct {
			Name    string   `json:"name"`
			Version string   `json:"version"`
			Loop    Manifest `json:"loop"`
		}
		if err := json.Unmarshal(data, &npm); err != nil {
			return manifest, err
		}
		manifest = npm.Loop
		manifest.Name, manifest.Version = npm.Name, npm.Version
	} else {
		if err != nil {
			return manifest, err
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return manifest, err
		}
	}
	if manifest.APIVersion < 0 || manifest.APIVersion > 1 {
		return manifest, fmt.Errorf("unsupported package API version %d", manifest.APIVersion)
	}
	if manifest.Name == "" {
		return manifest, fmt.Errorf("package manifest needs a name")
	}
	base, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return manifest, err
	}
	for _, group := range []*[]string{&manifest.Skills, &manifest.Prompts, &manifest.Plugins, &manifest.Extensions} {
		for i, path := range *group {
			if err := ctx.Err(); err != nil {
				return manifest, err
			}
			resolved, err := resourcePathWithBase(base, directory, path)
			if err != nil {
				return manifest, err
			}
			(*group)[i] = resolved
		}
	}
	for event := range manifest.Hooks {
		switch event {
		case "input", "context", "session_start", "session_end", "agent_start", "agent_end", "turn_start", "turn_end", "message_update", "message_end", "tool_call", "tool_result", "agent_settled", "queue_update", "compaction_start", "compaction_end", "retry", "integration_error":
		default:
			return manifest, fmt.Errorf("unsupported extension event %q", event)
		}
	}
	for event, path := range manifest.Hooks {
		if err := ctx.Err(); err != nil {
			return manifest, err
		}
		resolved, err := resourcePathWithBase(base, directory, path)
		if err != nil {
			return manifest, err
		}
		manifest.Hooks[event] = resolved
	}
	return manifest, ctx.Err()
}

func ResourcePath(directory, relative string) (string, error) {
	base, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	return resourcePathWithBase(base, directory, relative)
}

func resourcePathWithBase(base, directory, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		rel, err := filepath.Rel(base, relative)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			path, evalErr := filepath.EvalSymlinks(relative)
			if evalErr != nil {
				return "", evalErr
			}
			relResolved, relErr := filepath.Rel(base, path)
			if relErr != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
				return "", fmt.Errorf("package resource escapes package directory")
			}
			return path, nil
		}
		return "", fmt.Errorf("package resource path must be relative")
	}
	path, err := filepath.EvalSymlinks(filepath.Join(directory, relative))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("package resource escapes package directory")
	}
	return path, nil
}

func command(ctx context.Context, name string, args ...string) (string, error) {
	result, err := process.Run(ctx, process.Options{Executable: name, Arguments: args, Timeout: 5 * time.Minute})
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", name, err)
	}
	return result.Stdout, nil
}
