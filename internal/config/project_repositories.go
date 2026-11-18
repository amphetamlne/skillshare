package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"skillshare/internal/install"
	"skillshare/internal/projectdir"
	"skillshare/internal/sourcewalk"
	"skillshare/internal/utils"
)

// discoverProjectRepositories returns checkout roots below the selected scan
// directories. Each checkout is a leaf. An explicit project, including a
// project hidden on this machine, owns its own subtree.
func discoverProjectRepositories(root string, dirs []string, reserved map[string]bool) ([]string, error) {
	root = filepath.Clean(root)
	var roots []string
	seen := map[string]bool{}
	physicalRoot := sourcewalk.Canonical(root)
	for _, dir := range dirs {
		relative, err := filepath.Localize(dir)
		if err != nil || strings.ContainsAny(dir, `\:`) {
			return nil, fmt.Errorf("repository_dirs entry %q must be a relative slash-separated path", dir)
		}
		scanRoot := filepath.Join(root, relative)
		if err := rejectDirectoryLinks(root, scanRoot); err != nil {
			return nil, err
		}
		if insideExplicitProject(root, scanRoot, reserved) {
			continue
		}
		physicalScan := filepath.Join(physicalRoot, relative)
		err = filepath.WalkDir(physicalScan, func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() || utils.IsLinkMode(path, entry.Type()) {
				return nil
			}
			relativePath, err := filepath.Rel(physicalRoot, path)
			if err != nil {
				return err
			}
			id, _ := projectRootIdentity(path)
			if path != physicalScan && (reserved[id] || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if relativePath != "." && isDiscoveredCheckout(path) {
				if _, ownsConfig := projectdir.Find(path); ownsConfig {
					return filepath.SkipDir
				}
				if !seen[id] {
					seen[id] = true
					roots = append(roots, filepath.Join(root, relativePath))
				}
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(roots)
	return roots, nil
}

func insideExplicitProject(root, scanRoot string, reserved map[string]bool) bool {
	for path := scanRoot; path != root; path = filepath.Dir(path) {
		id, _ := projectRootIdentity(sourcewalk.Canonical(path))
		if reserved[id] {
			return true
		}
	}
	return false
}

func (c *Config) reservedRepositoryRoots() map[string]bool {
	roots := map[string]bool{}
	addRoot := func(root string) {
		id, _ := projectRootIdentity(root)
		roots[id] = true
		physical, _ := projectRootIdentity(sourcewalk.Canonical(expandPath(root)))
		roots[physical] = true
	}
	for root := range c.Projects {
		addRoot(root)
	}
	if c.projectOverlay != nil {
		for _, root := range c.projectOverlay.state.Excluded {
			addRoot(root)
		}
	}
	return roots
}

// ValidateDiscoveredSkillsPath applies the checkout boundary only to
// auto-discovered targets. Explicit targets retain their existing path rules.
func (tc TargetConfig) ValidateDiscoveredSkillsPath() error {
	if tc.projectOwner == "" {
		return nil
	}
	checkout := tc.ProjectRoot()
	if err := rejectDirectoryLinks(tc.projectOwner, checkout); err != nil {
		return err
	}
	if !isDiscoveredCheckout(checkout) {
		return fmt.Errorf("discovered checkout %s no longer has a Git marker", checkout)
	}
	return rejectDirectoryLinks(checkout, tc.SkillsConfig().Path)
}

func isDiscoveredCheckout(path string) bool {
	return !utils.IsSymlinkOrJunction(filepath.Join(path, ".git")) && install.IsGitRepo(path)
}

// Check each literal component rather than relying on EvalSymlinks, which can
// leave Windows junctions unresolved. Missing target folders are allowed.
func rejectDirectoryLinks(root, path string) error {
	root, path = filepath.Clean(root), filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s is outside the project folder %s", path, root)
	}
	if rel == "." {
		return nil
	}
	at := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if utils.IsLinkMode(at, info.Mode()) {
			return fmt.Errorf("%s crosses a directory link at %s", path, at)
		}
	}
	return nil
}

func skillFolderIdentity(path string) string {
	id, _ := projectRootIdentity(sourcewalk.Canonical(path))
	return id
}

func (c *Config) addRepositoryTargets(root, name string, project ManagedProject, reserved, occupied map[string]bool) error {
	if len(project.RepositoryDirs) == 0 {
		return nil
	}
	roots, err := discoverProjectRepositories(root, project.RepositoryDirs, reserved)
	if err != nil {
		return fmt.Errorf("projects: %s: %w", root, err)
	}
	if project.Skills == nil || !project.Skills.IsEnabled() {
		return nil
	}
	for _, checkout := range roots {
		relative, err := filepath.Rel(root, checkout)
		if err != nil {
			return err
		}
		groups, err := project.ToolGroups(checkout)
		if err != nil {
			return err
		}
		for _, group := range groups {
			id := skillFolderIdentity(group.SkillsPath)
			if occupied[id] {
				continue
			}
			occupied[id] = true
			c.addProjectTargets(name+"/"+filepath.ToSlash(relative), checkout, root, project.Skills, nil, []ProjectToolGroup{group})
		}
	}
	return nil
}
