package subagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/storage"
)

const (
	maxDefinitionBytes = 8 << 20
	maxStateBytes      = 64 << 20
)

// Storage manages on-disk persistence for subagent definitions and session states.
type Storage struct {
	dir string
}

// NewStorage creates a Storage instance for the given directory. If dir is empty,
// it defaults to ~/.loop/agents.
func NewStorage(dir string) (*Storage, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve user home directory: %w", err)
		}
		dir = filepath.Join(home, ".loop", "agents")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &Storage{dir: abs}, nil
}

// EnsureDir guarantees the storage directory exists.
func (s *Storage) EnsureDir() error {
	return os.MkdirAll(s.dir, 0700)
}

func (s *Storage) writeJSONFile(filename string, value any, limit int) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize %s: %w", filename, err)
	}
	if len(data) > limit {
		return fmt.Errorf("%s exceeds %d MiB", filename, limit>>20)
	}
	file, err := os.CreateTemp(s.dir, "."+filename+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", filename, err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return fmt.Errorf("secure temporary file for %s: %w", filename, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write temporary file for %s: %w", filename, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync temporary file for %s: %w", filename, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", filename, err)
	}
	if err := os.Rename(tempPath, filepath.Join(s.dir, filename)); err != nil {
		return fmt.Errorf("replace %s: %w", filename, err)
	}
	return storage.SyncDirectory(s.dir)
}

// SaveDefinition serializes an AgentDef to disk.
func (s *Storage) SaveDefinition(def AgentDef) error {
	if err := ValidateAgentName(def.Name); err != nil {
		return err
	}
	return s.writeJSONFile(def.Name+".json", def, maxDefinitionBytes)
}

// LoadDefinition deserializes an AgentDef from disk by name.
func (s *Storage) LoadDefinition(name string) (*AgentDef, error) {
	if err := ValidateAgentName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, name+".json")
	data, err := storage.ReadFile(path, maxDefinitionBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent definition file: %w", err)
	}
	var def AgentDef
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("failed to parse agent definition JSON: %w", err)
	}
	if err := ValidateAgentName(def.Name); err != nil {
		return nil, fmt.Errorf("invalid stored agent name: %w", err)
	}
	if def.Name != name {
		return nil, fmt.Errorf("agent definition %q declares unexpected name %q", name, def.Name)
	}
	return &def, nil
}

// DeleteDefinition deletes the definition file for the specified agent.
func (s *Storage) DeleteDefinition(name string) error {
	if err := ValidateAgentName(name); err != nil {
		return err
	}
	return s.removeIfExists(name + ".json")
}

func (s *Storage) removeIfExists(filename string) error {
	path := filepath.Join(s.dir, filename)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return storage.SyncDirectory(s.dir)
}

// ListDefinitions loads all existing agent definitions from disk.
func (s *Storage) ListDefinitions() ([]AgentDef, error) {
	if _, err := os.Stat(s.dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect agent definitions directory: %w", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list agent definitions directory: %w", err)
	}

	var defs []AgentDef
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), "_state.json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		def, err := s.LoadDefinition(name)
		if err != nil {
			return nil, fmt.Errorf("load agent definition %q: %w", name, err)
		}
		defs = append(defs, *def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

// SaveState serializes the execution state of an agent.
func (s *Storage) SaveState(name string, state AgentState) error {
	if err := ValidateAgentName(name); err != nil {
		return err
	}
	return s.writeJSONFile(name+"_state.json", state, maxStateBytes)
}

// LoadState deserializes the execution state of an agent.
func (s *Storage) LoadState(name string) (*AgentState, error) {
	if err := ValidateAgentName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, name+"_state.json")
	data, err := storage.ReadFile(path, maxStateBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read agent state file: %w", err)
	}
	var state AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse agent state JSON: %w", err)
	}
	return &state, nil
}
