package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestProjectsDiscoverRepositories(t *testing.T) {
	cfg, root, err := loadWithProjects(t, `  $ROOT/workspace:
    targets: [codex, antigravity]
    skills: {include: ["team-*"]}
    repository_dirs: [".", ".worktrees"]
  $ROOT/workspace/group/override:
    targets: [codex]
    skills: {include: ["override-*"]}
`, "workspace/group/app/.git", "workspace/group/override/.git",
		"workspace/.work/temporary/.git", "workspace/.worktrees/review",
		"workspace/group/independent/.git", "workspace/group/custom/.git",
		"workspace/ordinary", "unrelated/.git")
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	review := filepath.Join(workspace, ".worktrees", "review")
	gitDir := filepath.Join(workspace, "group", "app", ".git")
	if err := os.WriteFile(filepath.Join(review, ".git"), []byte("gitdir: "+gitDir+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	independent := filepath.Join(workspace, "group", "independent", ".skillshare")
	if err := os.Mkdir(independent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(independent, "config.yaml"), []byte("targets: [codex]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.Targets["custom"] = TargetConfig{Skills: &ResourceTargetConfig{
		Path: filepath.Join(workspace, "group", "custom", ".agents", "skills"),
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, relative := range map[string]string{
		"workspace/group/app@codex":         "group/app",
		"workspace/.worktrees/review@codex": ".worktrees/review",
	} {
		target, ok := cfg.Targets[name]
		if !ok {
			t.Errorf("missing target %q", name)
			continue
		}
		skills := target.SkillsConfig()
		want := filepath.Join(workspace, filepath.FromSlash(relative), ".agents", "skills")
		if skills.Path != want || !slices.Equal(skills.Include, []string{"team-*"}) {
			t.Errorf("%s: got %+v, want %q with inherited filter", name, skills, want)
		}
	}
	if len(cfg.Targets) != 5 || len(cfg.Projects) != 2 || len(cfg.ProjectTargetsFor(workspace)) != 3 {
		t.Errorf("want five targets (three for workspace) and two declarations, got %v and %v", cfg.Targets, cfg.Projects)
	}
	if target := cfg.Targets["override@codex"]; !slices.Equal(target.SkillsConfig().Include, []string{"override-*"}) {
		t.Errorf("explicit project did not keep its filter: %+v", target)
	}
}

func TestDiscoveredCheckoutRefusesRedirectedTarget(t *testing.T) {
	cfg, root, err := loadWithProjects(t, `  $ROOT/workspace:
    targets: [codex]
    skills: {}
    repository_dirs: ["."]
`, "workspace/app/.git", "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	target, ok := cfg.Targets["workspace/app@codex"]
	if !ok {
		t.Fatal("checkout was not discovered")
	}
	checkout := filepath.Join(root, "workspace", "app")
	if err := os.Symlink(filepath.Join(root, "unrelated"), filepath.Join(checkout, ".agents")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := target.ValidateDiscoveredSkillsPath(); err == nil {
		t.Error("accepted a destination redirected outside the checkout")
	}
	if err := os.Rename(checkout, filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "unrelated", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "unrelated"), checkout); err != nil {
		t.Fatal(err)
	}
	if err := target.ValidateDiscoveredSkillsPath(); err == nil {
		t.Error("accepted a checkout redirected outside the workspace")
	}
}

func TestProjectsRejectRepositoryDirsOutsideRoot(t *testing.T) {
	for _, dir := range []string{"../outside", "/outside", "nested/../outside", "C:/outside"} {
		t.Run(dir, func(t *testing.T) {
			_, _, err := loadWithProjects(t, "  $ROOT/workspace: {targets: [codex], skills: {}, repository_dirs: ['"+dir+"']}\n", "workspace")
			if err == nil {
				t.Errorf("accepted repository_dirs entry %q", dir)
			}
		})
	}
}
