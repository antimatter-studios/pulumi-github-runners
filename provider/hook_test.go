package githubrunners

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func stringInput(s string) pulumi.StringInput { return pulumi.String(s) }

// The fork hook, run for real: bash, and the first node build that works. A broken musl build is
// placed first, as the runner image ships node24_alpine, which is what failed every job on the
// homelab before homelab-server #158.
func TestRejectForksHook(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	externals := filepath.Join(dir, "externals")
	for _, d := range []string{"node24_alpine/bin", "node20/bin"} {
		if err := os.MkdirAll(filepath.Join(externals, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(externals, "node24_alpine/bin/node"), []byte("#!/bin/sh\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(externals, "node20/bin/node")); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(dir, "reject-forks.sh")
	if err := os.WriteFile(hook, []byte(strings.ReplaceAll(rejectForksHook, "/home/runner/externals", externals)), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, event, payload string
		refused              bool
	}{
		{"fork pull request", "pull_request", `{"repository":{"full_name":"x/y"},"pull_request":{"head":{"repo":{"full_name":"fork/y"}}}}`, true},
		{"fork pull_request_target", "pull_request_target", `{"repository":{"full_name":"x/y"},"pull_request":{"head":{"repo":{"full_name":"fork/y"}}}}`, true},
		{"deleted fork", "pull_request", `{"repository":{"full_name":"x/y"},"pull_request":{"head":{"repo":null}}}`, true},
		{"fork workflow_run", "workflow_run", `{"repository":{"full_name":"x/y"},"workflow_run":{"head_repository":{"full_name":"fork/y"}}}`, true},
		{"same-repository pull request", "pull_request", `{"repository":{"full_name":"x/y"},"pull_request":{"head":{"repo":{"full_name":"x/y"}}}}`, false},
		{"push", "push", `{"repository":{"full_name":"x/y"},"ref":"refs/heads/main"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			event := filepath.Join(t.TempDir(), "event.json")
			if err := os.WriteFile(event, []byte(c.payload), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", hook)
			cmd.Env = append(os.Environ(), "GITHUB_EVENT_PATH="+event, "GITHUB_EVENT_NAME="+c.event)
			out, err := cmd.CombinedOutput()
			if refused := err != nil; refused != c.refused {
				t.Fatalf("refused=%v, want %v: %s", refused, c.refused, out)
			}
			if c.refused && !strings.Contains(string(out), "refuses jobs from forks") {
				t.Fatalf("no explanation: %s", out)
			}
		})
	}
}

// With no node build that runs, the job fails, loudly, rather than passing unchecked.
func TestRejectForksHookFailsClosedWithoutNode(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "reject-forks.sh")
	if err := os.WriteFile(hook, []byte(strings.ReplaceAll(rejectForksHook, "/home/runner/externals", filepath.Join(dir, "none"))), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", hook)
	cmd.Env = append(os.Environ(), "GITHUB_EVENT_PATH=/dev/null", "GITHUB_EVENT_NAME=push")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "no node in the runner") {
		t.Fatalf("err %v: %s", err, out)
	}
}

// The composed hook runs the fork check, then the pool's script, and stops at the first failure.
func TestJobStartedRunsBothInOrder(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("reject-forks.sh", "#!/bin/sh\necho forks >> "+log+"\n")
	write("custom.sh", "#!/bin/sh\necho custom >> "+log+"\n")
	write(jobStartedHook, strings.ReplaceAll(jobStartedScript(true, true), hooksDir, dir))
	if out, err := exec.Command("bash", filepath.Join(dir, jobStartedHook)).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got, _ := os.ReadFile(log); string(got) != "forks\ncustom\n" {
		t.Fatalf("ran %q", got)
	}
	write("reject-forks.sh", "#!/bin/sh\nexit 1\n")
	_ = os.Remove(log)
	if err := exec.Command("bash", filepath.Join(dir, jobStartedHook)).Run(); err == nil {
		t.Fatal("a refused fork must fail the job")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("the pool's script ran after the fork check failed")
	}
}
