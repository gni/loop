package packages

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"crypto/sha256"
	"loop/pkg/config"
	"loop/pkg/process"
	"loop/pkg/storage"
	"time"
)

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
	data, err := os.ReadFile(filepath.Join(m.Directory, "packages.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("package registry exceeds 1 MiB")
	}
	var packages []Package
	if err := json.Unmarshal(data, &packages); err != nil {
		return nil, err
	}
	return packages, nil
}

func (m Manager) save(packages []Package) error {
	data, err := json.MarshalIndent(packages, "", "  ")
	if err != nil {
		return err
	}
	return config.WritePrivateFile(filepath.Join(m.Directory, "packages.json"), data)
}

func (m Manager) Install(ctx context.Context, source string) (Package, error) {
	release, err := storage.Lock(filepath.Join(m.Directory, ".packages.lock"), true)
	if err != nil {
		return Package{}, err
	}
	defer release()
	entries, err := m.List()
	if err != nil {
		return Package{}, err
	}
	if source == "" {
		return Package{}, fmt.Errorf("package source is empty")
	}
	sum := sha256.Sum256([]byte(source))
	key := hex.EncodeToString(sum[:16])
	destination := filepath.Join(m.Directory, "packages", key)
	installed := Package{Source: source, Directory: destination}
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
		manifest, err := ReadManifest(staging)
		if err != nil {
			return installed, err
		}
		installed.Name, installed.Version = manifest.Name, manifest.Version
		if err := replaceDirectory(staging, destination); err != nil {
			return installed, err
		}
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
		if err := unpack(filepath.Join(staging, packed[0].Filename), filepath.Join(staging, "content")); err != nil {
			return installed, err
		}
		manifest, err := ReadManifest(filepath.Join(staging, "content"))
		if err != nil {
			return installed, err
		}
		installed.Name, installed.Version, installed.Revision = manifest.Name, manifest.Version, packed[0].Integrity
		if err := replaceDirectory(filepath.Join(staging, "content"), destination); err != nil {
			return installed, err
		}
	} else {
		local := strings.TrimPrefix(source, "local:")
		installed.Directory, err = config.CanonicalWorkspace(local)
		if err != nil {
			return installed, err
		}
		manifest, err := ReadManifest(installed.Directory)
		if err != nil {
			return installed, err
		}
		installed.Name, installed.Version = manifest.Name, manifest.Version
	}
	for index, entry := range entries {
		if entry.Source == source {
			entries[index] = installed
			return installed, m.save(entries)
		}
	}
	entries = append(entries, installed)
	return installed, m.save(entries)
}

func (m Manager) Remove(source string) error {
	release, err := storage.Lock(filepath.Join(m.Directory, ".packages.lock"), true)
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
	return m.save(retained)
}

func (m Manager) Update(ctx context.Context, source string) error {
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
	var manifest Manifest
	data, err := storage.ReadFile(filepath.Join(directory, "loop.json"), 1<<20)
	if os.IsNotExist(err) {
		data, err = storage.ReadFile(filepath.Join(directory, "package.json"), 1<<20)
		if err != nil {
			return manifest, fmt.Errorf("package needs loop.json or package.json")
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
	if manifest.APIVersion > 1 {
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
		resolved, err := resourcePathWithBase(base, directory, path)
		if err != nil {
			return manifest, err
		}
		manifest.Hooks[event] = resolved
	}
	return manifest, nil
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

func replaceDirectory(source, destination string) error {
	backup := destination + ".previous"
	exists := false
	if _, err := os.Stat(destination); err == nil {
		exists = true
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		if exists {
			if restoreErr := os.Rename(backup, destination); restoreErr != nil {
				return fmt.Errorf("install failed: %v; restore failed: %w", err, restoreErr)
			}
		}
		return err
	}
	if exists {
		return os.RemoveAll(backup)
	}
	return nil
}

func unpack(path, destination string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gzipReader, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(io.LimitReader(gzipReader, 64<<20))
	total := int64(0)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(header.Name, "package/")
		if name == "package" || name == "" {
			continue
		}
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive path escapes package directory")
		}
		target := filepath.Join(destination, clean)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("package archive contains an unsupported link or special file")
		}
		total += header.Size
		if total > 64<<20 || header.Size < 0 {
			return fmt.Errorf("package archive exceeds 64 MiB")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(header.Mode)&0700)
		if err != nil {
			return err
		}
		_, err = io.CopyN(output, reader, header.Size)
		closeErr := output.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
