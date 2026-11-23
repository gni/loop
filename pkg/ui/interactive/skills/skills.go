package skills

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/style"
)

func RunSkillPicker(
	loadSkills func() []tool.Skill,
	reloadSkills func() ([]tool.Skill, error),
	loadedNames func([]tool.Skill) map[string]bool,
	theme style.UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
) (string, error) {
	ctx := context.Background()
	if !interactive.IsNilReader(rlInput) {
		if provider, ok := rlInput.(interface{ GetInputContext() context.Context }); ok {
			if inputCtx := provider.GetInputContext(); inputCtx != nil {
				ctx = inputCtx
			}
		}
	}
	return RunSkillPickerContext(ctx, loadSkills, reloadSkills, loadedNames, theme, rlInput, rlOutput)
}

func RunSkillPickerContext(
	ctx context.Context,
	loadSkills func() []tool.Skill,
	reloadSkills func() ([]tool.Skill, error),
	loadedNames func([]tool.Skill) map[string]bool,
	theme style.UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
) (string, error) {
	var selected string
	err := interactive.RunAltScreenSession(
		ctx,
		rlInput,
		rlOutput,
		func(sr *interactive.SessionReader, signals chan os.Signal) error {
			var pickErr error
			selected, pickErr = runSkillPickerTTY(sr, signals, rlOutput, loadSkills, reloadSkills, loadedNames, theme)
			return pickErr
		},
		func(sr *interactive.SessionReader) error {
			var pickErr error
			selected, pickErr = runSkillPickerFallback(sr, rlOutput, loadSkills, reloadSkills, loadedNames)
			return pickErr
		},
	)
	return selected, err
}

func runSkillPickerTTY(
	sr *interactive.SessionReader,
	signals chan os.Signal,
	rlOutput io.Writer,
	loadSkills func() []tool.Skill,
	reloadSkills func() ([]tool.Skill, error),
	loadedNames func([]tool.Skill) map[string]bool,
	theme style.UITheme,
) (string, error) {
	status := ""
	for {
		var active []tool.Skill
		if loadSkills != nil {
			active = sortedSkills(loadSkills())
		}
		var loaded map[string]bool
		if loadedNames != nil {
			loaded = loadedNames(active)
		}
		items := make([]string, 0, len(active)+1)
		for _, s := range active {
			items = append(items, formatSkillPickerItem(s, loaded[strings.ToLower(s.Name)]))
		}
		if reloadSkills != nil {
			items = append(items, "Reload skills from disk")
		}
		if len(items) == 0 {
			return "", interactive.ErrSelectionCancelled
		}

		title := "loop skills"
		if status != "" {
			title = "loop skills · " + status
		} else if len(active) == 0 {
			title = "loop skills · no skills found"
		}

		index, err := interactive.RunInteractiveSelect(sr, signals, rlOutput, title, items, theme)
		if err != nil {
			return "", err
		}
		if index < len(active) {
			return active[index].Name, nil
		}
		if reloadSkills != nil {
			reloaded, reloadErr := reloadSkills()
			if reloadErr != nil {
				status = fmt.Sprintf("reload failed: %v", reloadErr)
			} else {
				status = fmt.Sprintf("reloaded %d skill(s)", len(reloaded))
			}
		}
	}
}

func runSkillPickerFallback(
	sr *interactive.SessionReader,
	rlOutput io.Writer,
	loadSkills func() []tool.Skill,
	reloadSkills func() ([]tool.Skill, error),
	loadedNames func([]tool.Skill) map[string]bool,
) (string, error) {
	for {
		var active []tool.Skill
		if loadSkills != nil {
			active = sortedSkills(loadSkills())
		}
		var loaded map[string]bool
		if loadedNames != nil {
			loaded = loadedNames(active)
		}

		fmt.Fprint(rlOutput, "\r\n==================================================\r\n")
		fmt.Fprint(rlOutput, "                  loop skills                    \r\n")
		fmt.Fprint(rlOutput, "==================================================\r\n")
		if len(active) == 0 {
			fmt.Fprint(rlOutput, "no skills found.\r\n")
		} else {
			for i, s := range active {
				desc := strings.Join(strings.Fields(s.Description), " ")
				if len([]rune(desc)) > 60 {
					desc = string([]rune(desc)[:60]) + "..."
				}
				tag := ""
				if loaded[strings.ToLower(s.Name)] {
					tag += " [loaded]"
				}
				if s.DisableModelInvocation {
					tag += " [manual]"
				}
				if desc != "" {
					fmt.Fprintf(rlOutput, "  [%d] %s%s - %s\r\n", i+1, s.Name, tag, desc)
				} else {
					fmt.Fprintf(rlOutput, "  [%d] %s%s\r\n", i+1, s.Name, tag)
				}
			}
		}
		fmt.Fprint(rlOutput, "--------------------------------------------------\r\n")
		fmt.Fprint(rlOutput, "options:\r\n")
		fmt.Fprint(rlOutput, "  <number>     : load skill\r\n")
		if reloadSkills != nil {
			fmt.Fprint(rlOutput, "  r            : reload skills from disk\r\n")
		}
		fmt.Fprint(rlOutput, "  q            : cancel and return\r\n")
		fmt.Fprint(rlOutput, "\r\nchoose option: ")

		inputStr, err := interactive.ReadInputRaw(sr, rlOutput)
		if err != nil {
			return "", err
		}
		inputStr = strings.TrimSpace(inputStr)
		if inputStr == "" {
			continue
		}
		if inputStr == "q" || inputStr == "exit" {
			return "", interactive.ErrSelectionCancelled
		}
		if inputStr == "r" && reloadSkills != nil {
			reloaded, reloadErr := reloadSkills()
			if reloadErr != nil {
				fmt.Fprintf(rlOutput, "failed to reload skills: %v\r\n", reloadErr)
			} else {
				fmt.Fprintf(rlOutput, "reloaded %d skill(s) from disk.\r\n", len(reloaded))
			}
			continue
		}
		num, err := strconv.Atoi(inputStr)
		if err == nil && num >= 1 && num <= len(active) {
			return active[num-1].Name, nil
		}
		fmt.Fprint(rlOutput, "invalid choice. please try again.\r\n")
	}
}

func formatSkillPickerItem(s tool.Skill, isLoaded bool) string {
	name := strings.Join(strings.Fields(s.Name), " ")
	desc := strings.Join(strings.Fields(s.Description), " ")
	var tags []string
	if isLoaded {
		tags = append(tags, "[loaded]")
	}
	if s.DisableModelInvocation {
		tags = append(tags, "[manual]")
	}
	parts := []string{name}
	if len(tags) > 0 {
		parts = append(parts, strings.Join(tags, " "))
	}
	if desc != "" {
		parts = append(parts, desc)
	} else if s.Path != "" {
		parts = append(parts, filepath.Base(s.Path))
	}
	return strings.Join(parts, " · ")
}

func sortedSkills(in []tool.Skill) []tool.Skill {
	out := append([]tool.Skill(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
