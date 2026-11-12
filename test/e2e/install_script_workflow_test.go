//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowJob struct {
	Needs    yaml.Node `yaml:"needs"`
	Strategy struct {
		Matrix struct {
			OS []string `yaml:"os"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []struct {
		Run string `yaml:"run"`
	} `yaml:"steps"`
}

func jobNeeds(job workflowJob, dependency string) bool {
	if job.Needs.Kind == yaml.ScalarNode {
		return job.Needs.Value == dependency
	}
	for _, item := range job.Needs.Content {
		if item.Value == dependency {
			return true
		}
	}
	return false
}

func loadWorkflowJobs(t *testing.T, filename string) map[string]workflowJob {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRootDir(t), ".github", "workflows", filename))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow.Jobs
}

func TestReleaseWorkflowRequiresVerification(t *testing.T) {
	jobs := loadWorkflowJobs(t, "release.yml")
	verify, ok := jobs["verify"]
	if !ok {
		t.Fatal("release workflow has no verify job for tagged commits")
	}
	for _, command := range []string{"make check", "make e2e"} {
		found := false
		for _, step := range verify.Steps {
			if strings.Contains(step.Run, command) {
				found = true
			}
		}
		if !found {
			t.Errorf("release verification omits %q", command)
		}
	}
	if !jobNeeds(jobs["build"], "verify") {
		t.Error("release build can run without verification")
	}
	for _, dependency := range []string{"build", "install-smoke"} {
		if !jobNeeds(jobs["release"], dependency) {
			t.Errorf("publishing does not wait for %s", dependency)
		}
	}
}

func TestInstallSmokeRunsOnNativePlatforms(t *testing.T) {
	for _, filename := range []string{"go.yml", "release.yml"} {
		t.Run(filename, func(t *testing.T) {
			job, ok := loadWorkflowJobs(t, filename)["install-smoke"]
			if !ok {
				t.Fatal("workflow has no install-smoke job")
			}
			for _, platform := range []string{"windows-latest", "macos-latest"} {
				if !slices.Contains(job.Strategy.Matrix.OS, platform) {
					t.Errorf("install-smoke does not run on %s", platform)
				}
			}
			var commands string
			for _, step := range job.Steps {
				commands += step.Run
			}
			for _, command := range []string{"scripts/install.sh", "--source", "--version", "providers"} {
				if !strings.Contains(commands, command) {
					t.Errorf("install-smoke omits %q", command)
				}
			}
		})
	}
}
