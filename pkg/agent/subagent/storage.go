package subagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	return &Storage{dir: dir}, nil
}

// EnsureDir guarantees the storage directory exists.
func (s *Storage) EnsureDir() error {
	return os.MkdirAll(s.dir, 0755)
}


// SaveDefinition serializes an AgentDef to disk.
func (s *Storage) SaveDefinition(def AgentDef) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal agent definition: %w", err)
	}
	path := filepath.Join(s.dir, def.Name+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write agent definition file: %w", err)
	}
	return os.Chmod(path, 0600)
}

// LoadDefinition deserializes an AgentDef from disk by name.
func (s *Storage) LoadDefinition(name string) (*AgentDef, error) {
	path := filepath.Join(s.dir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent definition file: %w", err)
	}
	var def AgentDef
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("failed to parse agent definition JSON: %w", err)
	}
	return &def, nil
}

// DeleteDefinition deletes the definition file for the specified agent.
func (s *Storage) DeleteDefinition(name string) error {
	return s.removeIfExists(name + ".json")
}

func (s *Storage) removeIfExists(filename string) error {
	path := filepath.Join(s.dir, filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ListDefinitions loads all existing agent definitions from disk.
func (s *Storage) ListDefinitions() ([]AgentDef, error) {
	if _, err := os.Stat(s.dir); os.IsNotExist(err) {
		return nil, nil
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
		if err == nil && def != nil {
			defs = append(defs, *def)
		}
	}
	return defs, nil
}

// SaveState serializes the execution state of an agent.
func (s *Storage) SaveState(name string, state AgentState) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal agent state: %w", err)
	}
	path := filepath.Join(s.dir, name+"_state.json")
	return os.WriteFile(path, data, 0644)
}

// LoadState deserializes the execution state of an agent.
func (s *Storage) LoadState(name string) (*AgentState, error) {
	path := filepath.Join(s.dir, name+"_state.json")
	if _, err := os.Stat(path); err != nil {
		return nil, nil // No state file yet
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent state file: %w", err)
	}
	var state AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse agent state JSON: %w", err)
	}
	return &state, nil
}

// DeleteState removes the state file for the specified agent.
func (s *Storage) DeleteState(name string) error {
	return s.removeIfExists(name + "_state.json")
}
