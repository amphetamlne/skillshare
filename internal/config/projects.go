package config

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"skillshare/internal/utils"
)

// ProjectTargetSep joins a project name and a tool into a target name. User-defined
// target names cannot contain it, so a project target never shadows one.
const ProjectTargetSep = "@"

// ManagedProject is a project folder the global config syncs skills and agents into,
// using each tool's project paths from targets.yaml. The folder needs no .skillshare/.
// Its MCP servers live under mcp.projects, keyed by the same root.
type ManagedProject struct {
	// Name labels the project's targets. Defaults to the folder's base name.
	Name    string   `yaml:"name,omitempty"`
	Targets []string `yaml:"targets,omitempty"`
	// Skills and Agents turn that resource on when present; an empty value syncs all.
	Skills *ResourceTargetConfig `yaml:"skills,omitempty"`
	Agents *ResourceTargetConfig `yaml:"agents,omitempty"`
	// RepositoryDirs optionally discovers additional skills destinations below root.
	RepositoryDirs []string `yaml:"repository_dirs,omitempty"`
}

// ProjectTargetName returns the target name of one tool in a project.
func ProjectTargetName(project, tool string) string {
	return project + ProjectTargetSep + tool
}

// SplitProjectTarget splits a project target name. ok is false for an ordinary target.
func SplitProjectTarget(name string) (project, tool string, ok bool) {
	i := strings.LastIndex(name, ProjectTargetSep)
	if i < 0 {
		return "", name, false
	}
	return name[:i], name[i+1:], true
}

// ProjectRoot is the folder a project target writes into, or "" for an ordinary target.
func (tc TargetConfig) ProjectRoot() string { return tc.projectRoot }

// ProjectOwnerRoot identifies the declaration that owns this target.
func (tc TargetConfig) ProjectOwnerRoot() string { return cmp.Or(tc.projectOwner, tc.projectRoot) }

// ProjectTargetsFor returns the targets owned by the declared project at root.
func (c *Config) ProjectTargetsFor(root string) map[string]TargetConfig {
	root = filepath.Clean(expandPath(root))
	targets := map[string]TargetConfig{}
	for name, target := range c.Targets {
		if target.ProjectOwnerRoot() != "" && utils.PathsEqual(filepath.Clean(target.ProjectOwnerRoot()), root) {
			targets[name] = target
		}
	}
	return targets
}

// ProjectName returns the effective name of the project declared under root.
func (p ManagedProject) ProjectName(root string) string {
	if p.Name != "" {
		return p.Name
	}
	return filepath.Base(expandPath(root))
}

// ProjectToolGroup is the tools of one project that write skills to the same folder.
type ProjectToolGroup struct {
	Tools      []string
	SkillsPath string // absolute
	AgentsPath string // absolute; "" when no tool in the group has a project agents path
}

// Target is the tool the group's target is named after: the one that takes agents,
// else the first listed.
func (g ProjectToolGroup) Target() string {
	for _, tool := range g.Tools {
		if _, ok := LookupProjectAgentTarget(tool); ok {
			return tool
		}
	}
	return g.Tools[0]
}

// ToolGroups resolves the project's tools to folders under root, merging tools that
// share a skills folder such as .agents/skills so it is written once.
func (p ManagedProject) ToolGroups(root string) ([]ProjectToolGroup, error) {
	root = expandPath(root)
	var groups []ProjectToolGroup
	byPath := map[string]int{}
	for _, tool := range p.Targets {
		skills, ok := LookupProjectTarget(tool)
		if !ok {
			return nil, fmt.Errorf("unknown target %q", tool)
		}
		path := filepath.Join(root, skills.Path)
		i, seen := byPath[path]
		if !seen {
			i = len(groups)
			byPath[path] = i
			groups = append(groups, ProjectToolGroup{SkillsPath: path})
		}
		groups[i].Tools = append(groups[i].Tools, tool)
		// ponytail: one agents folder per group. targets.yaml has at most one tool with
		// project agents per shared skills folder; split the group if that changes.
		if agents, ok := LookupProjectAgentTarget(tool); ok && groups[i].AgentsPath == "" {
			groups[i].AgentsPath = filepath.Join(root, agents.Path)
		}
	}
	return groups, nil
}

// MissingProjects lists declared roots that are not folders. They get no targets, so a
// sync never creates a project that was moved or deleted.
func (c *Config) MissingProjects() []string {
	var missing []string
	for root := range c.Projects {
		if info, err := os.Stat(expandPath(root)); err != nil || !info.IsDir() {
			missing = append(missing, root)
		}
	}
	sort.Strings(missing)
	return missing
}

// expandProjects adds each project's targets to c.Targets, where sync, status, diff and
// doctor find them like any other. Save leaves them out again.
func (c *Config) expandProjects() error {
	names := map[string]string{}
	originsByName := map[string]map[string]bool{}
	if c.projectOverlay != nil {
		for root, project := range c.Projects {
			name := project.ProjectName(root)
			if originsByName[name] == nil {
				originsByName[name] = map[string]bool{}
			}
			originsByName[name][c.projectOverlay.origins[root]] = true
		}
	}
	for root, project := range c.Projects {
		abs := expandPath(root)
		if !filepath.IsAbs(abs) {
			return fmt.Errorf("projects: %s must be an absolute path or start with ~", root)
		}
		name := project.ProjectName(root)
		if originsByName[name]["local"] && originsByName[name]["shared"] {
			if info, err := os.Stat(abs); err != nil || !info.IsDir() {
				continue
			}
		}
		if strings.ContainsAny(name, ProjectTargetSep+`/\`) {
			return fmt.Errorf("projects: %s: name %q cannot contain %s, / or \\", root, name, ProjectTargetSep)
		}
		if other, taken := names[name]; taken {
			return fmt.Errorf("projects: %s and %s are both named %q; set name on one of them", other, root, name)
		}
		names[name] = root
		for _, rc := range []*ResourceTargetConfig{project.Skills, project.Agents} {
			if rc != nil && rc.Path != "" {
				return fmt.Errorf("projects: %s: path is not allowed; each target's project path is used", root)
			}
		}
		groups, err := project.ToolGroups(root)
		if err != nil {
			return fmt.Errorf("projects: %s: %w", root, err)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			continue
		}
		c.addProjectTargets(name, abs, "", project.Skills, project.Agents, groups)
	}
	// Discover only after all explicit destinations have claimed their folders.
	var workspaces []string
	for root, project := range c.Projects {
		if len(project.RepositoryDirs) != 0 {
			workspaces = append(workspaces, root)
		}
	}
	if len(workspaces) != 0 {
		reserved := c.reservedRepositoryRoots()
		occupied := map[string]bool{}
		for _, target := range c.Targets {
			if path := skillsFolder(target); path != "" {
				occupied[skillFolderIdentity(path)] = true
			}
		}
		slices.Sort(workspaces)
		for _, root := range workspaces {
			project := c.Projects[root]
			if err := c.addRepositoryTargets(expandPath(root), project.ProjectName(root), project, reserved, occupied); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Config) addProjectTargets(name, root, owner string, skillConfig, agentConfig *ResourceTargetConfig, groups []ProjectToolGroup) {
	for _, group := range groups {
		if skillConfig == nil && (agentConfig == nil || group.AgentsPath == "") {
			continue
		}
		// An agents-only target selects no skills and uses a naming compatible with all modes.
		skills := ResourceTargetConfig{Exclude: []string{"*"}, TargetNaming: "flat"}
		if skillConfig != nil {
			skills = *skillConfig
		}
		skills.Path = group.SkillsPath
		target := TargetConfig{Skills: &skills, projectRoot: root, projectOwner: owner, defaultTargetNaming: c.TargetNaming}
		if len(group.Tools) > 1 {
			for _, tool := range group.Tools {
				target.sharedSkillTargets = append(target.sharedSkillTargets, SkillTargetIdentity{Name: ProjectTargetName(name, tool)})
			}
		}
		if agentConfig != nil && group.AgentsPath != "" {
			agents := *agentConfig
			agents.Path = group.AgentsPath
			target.Agents = &agents
		}
		if c.Targets == nil {
			c.Targets = map[string]TargetConfig{}
		}
		c.Targets[ProjectTargetName(name, group.Target())] = target
	}
}

// OwnTargets returns the targets declared under targets, without the project ones.
func (c *Config) OwnTargets() map[string]TargetConfig {
	own := make(map[string]TargetConfig, len(c.Targets))
	for name, target := range c.Targets {
		if target.projectRoot == "" {
			own[name] = target
		}
	}
	return own
}

// withoutProjectTargets returns c as it should be written: project targets come from
// projects, never from targets, and inferred resource paths stay derived.
func (c *Config) withoutProjectTargets() *Config {
	out := *c
	out.Targets = c.OwnTargets()
	out.Projects = c.sharedProjectDeclarations()
	for name, target := range out.Targets {
		out.Targets[name] = target.withoutDerivedPaths()
	}
	return &out
}

// LoadWithoutProjects loads the config for commands that edit or collect from targets.
// Project targets are edited under projects, and a project's own skills stay in it.
func LoadWithoutProjects() (*Config, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	cfg.Targets = cfg.OwnTargets()
	return cfg, nil
}

// ValidateProjects reports whether projects could replace c.Projects, without changing c.
func (c *Config) ValidateProjects(projects map[string]ManagedProject) error {
	probe := Config{Targets: c.OwnTargets(), Projects: projects, TargetNaming: c.TargetNaming, projectOverlay: c.projectOverlay}
	for _, project := range projects {
		for _, rc := range []*ResourceTargetConfig{project.Skills, project.Agents} {
			if rc != nil && !IsValidSyncMode(rc.Mode) {
				return fmt.Errorf("invalid sync mode %q (valid: %s)", rc.Mode, strings.Join(ValidSyncModes, ", "))
			}
		}
	}
	if err := c.ProjectNamingError(projects, c.Mode); err != nil {
		return err
	}
	return probe.expandProjects()
}

// ProjectNamingError checks each project's effective skills naming against its
// effective mode under global mode, which the targets it expands into will inherit.
// It covers projects whose folder is missing, which expand into no target yet.
func (c *Config) ProjectNamingError(projects map[string]ManagedProject, mode string) error {
	for _, root := range slices.Sorted(maps.Keys(projects)) {
		if sk := projects[root].Skills; sk != nil && sk.IsEnabled() {
			if err := TargetNamingModeConfigError(cmp.Or(sk.TargetNaming, c.TargetNaming), cmp.Or(sk.Mode, mode), root); err != nil {
				return fmt.Errorf("projects: %s: %w", root, err)
			}
		}
	}
	return nil
}

// ConvertibleProject is a group of ordinary targets whose folders are the tool paths of
// one project folder, so projects can declare them instead.
type ConvertibleProject struct {
	Root    string
	Targets []string // target names, sorted
	Project ManagedProject
}

// ConvertibleProjects finds targets with a custom path such as ~/work/app/.claude/skills.
// A folder is offered only when its targets agree on mode and filters, because a project
// has one set of each.
func (c *Config) ConvertibleProjects() []ConvertibleProject {
	specs, err := loadTargetSpecs()
	if err != nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	homeID, _ := projectRootIdentity(home)
	declared := map[string]bool{}
	for _, root := range c.SharedProjectRoots() {
		id, _ := projectRootIdentity(root)
		declared[id] = true
	}
	for root := range c.Projects {
		id, _ := projectRootIdentity(root)
		declared[id] = true
	}
	withoutPath := func(rc ResourceTargetConfig) ResourceTargetConfig {
		rc.Path = ""
		rc.Agent = ""
		rc.SharedTargets = nil
		return rc
	}

	byRoot := map[string]*ConvertibleProject{}
	mixed := map[string]bool{}
	for _, name := range sortedTargetNames(c.OwnTargets()) {
		target := c.Targets[name]
		skills := target.SkillsConfig()
		root, tool := projectToolForPath(specs, name, skills.Path)
		id, _ := projectRootIdentity(root)
		if root == "" || id == homeID || declared[id] || c.HasLocalProject(root) {
			continue
		}
		var agents *ResourceTargetConfig
		if ac := target.AgentsConfig(); ac.Path != "" {
			known, ok := LookupProjectAgentTarget(tool)
			if !ok || ac.Path != filepath.Join(root, known.Path) {
				mixed[root] = true
				continue
			}
			rc := withoutPath(ac)
			agents = &rc
		}
		sk := withoutPath(skills)
		sk.TargetNaming = ""
		if target.Skills != nil {
			sk.TargetNaming = target.Skills.TargetNaming
		}
		found := byRoot[root]
		if found == nil {
			byRoot[root] = &ConvertibleProject{Root: root, Targets: []string{name}, Project: ManagedProject{Targets: []string{tool}, Skills: &sk, Agents: agents}}
			continue
		}
		if !reflect.DeepEqual(*found.Project.Skills, sk) || (agents != nil && found.Project.Agents != nil && !reflect.DeepEqual(*found.Project.Agents, *agents)) {
			mixed[root] = true
			continue
		}
		found.Targets = append(found.Targets, name)
		found.Project.Targets = append(found.Project.Targets, tool)
		if found.Project.Agents == nil {
			found.Project.Agents = agents
		}
	}

	var out []ConvertibleProject
	for root, found := range byRoot {
		if !mixed[root] {
			out = append(out, *found)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	return out
}

func sortedTargetNames(targets map[string]TargetConfig) []string {
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// projectToolForPath reads a skills folder as <root>/<a tool's project path>. The longest
// project path wins; among tools sharing it, the one named like the target, then
// universal, then the first listed.
func projectToolForPath(specs []targetSpec, targetName, path string) (root, tool string) {
	longest := 0
	for _, spec := range specs {
		rel := normalizeTargetPath(spec.Skills.Project)
		// ponytail: a bare folder name such as "skills" would match almost any custom
		// path, so only tool paths with a parent folder are recognised.
		if !strings.ContainsRune(rel, filepath.Separator) || !strings.HasSuffix(path, string(filepath.Separator)+rel) {
			continue
		}
		better := len(rel) > longest
		if len(rel) == longest {
			better = tool != targetName && (targetSpecMatchesName(spec, targetName) || (spec.Name == "universal" && !strings.EqualFold(tool, "universal")))
		}
		if better {
			longest = len(rel)
			root, tool = strings.TrimSuffix(path, string(filepath.Separator)+rel), spec.Name
		}
	}
	return root, tool
}
