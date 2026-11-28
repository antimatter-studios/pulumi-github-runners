package githubrunners

import (
	"net/http"
	"net/http/httptest"
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
	write(jobStartedHook, strings.ReplaceAll(jobStartedScript(true, false, true), hooksDir, dir))
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

// runClaimHook runs the whole composed hook as a pool with gateClaim mounts it: the real fork check,
// the real claim script and a pool script, which appends "custom" to the log.
func runClaimHook(t *testing.T, gate, payload, log string) (string, error) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	dir := t.TempDir()
	externals := filepath.Join(dir, "externals")
	if err := os.MkdirAll(filepath.Join(externals, "node20/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(externals, "node20/bin/node")); err != nil {
		t.Fatal(err)
	}
	files, err := hookFiles(PoolHook{Script: sp("#!/bin/sh\necho custom >> " + log + "\n")}, true)
	if err != nil {
		t.Fatal(err)
	}
	replace := strings.NewReplacer(hooksDir, dir, "/home/runner/externals", externals)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(replace.Replace(content)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	event := filepath.Join(dir, "event.json")
	if err := os.WriteFile(event, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(dir, jobStartedHook))
	cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1", "no_proxy=127.0.0.1", "RUNNER_GATE_URL="+gate,
		"GITHUB_EVENT_PATH="+event, "GITHUB_EVENT_NAME=pull_request", "GITHUB_REPOSITORY=x/y",
		"GITHUB_REPOSITORY_OWNER=x", "GITHUB_RUN_ID=42", "GITHUB_RUN_ATTEMPT=1", "GITHUB_JOB=unit")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// With gateClaim the hook runs the fork check, then the claim, then the pool's script. A refused fork
// never reaches the gate, and an expired reservation stops the job before the pool's script.
func TestJobStartedClaimsBetweenTheForkCheckAndThePoolScript(t *testing.T) {
	const own = `{"repository":{"full_name":"x/y"},"pull_request":{"head":{"repo":{"full_name":"x/y"}}}}`
	const fork = `{"repository":{"full_name":"x/y"},"pull_request":{"head":{"repo":{"full_name":"fork/y"}}}}`
	cases := []struct {
		name, payload, answer, log, says string
		fails                            bool
	}{
		{"claimed", own, "ok", "claim /claim?attempt=1&job=unit&org=x&run=42\ncustom\n", "reservation claimed", false},
		{"refused fork", fork, "ok", "", "refuses jobs from forks", true},
		{"expired reservation", own, "expired 37 120", "claim /claim?attempt=1&job=unit&org=x&run=42\n", "reservation expired", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "log")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					t.Error(err)
					return
				}
				defer f.Close()
				_, _ = f.WriteString("claim " + r.URL.Path + "?" + r.URL.Query().Encode() + "\n")
				_, _ = w.Write([]byte(c.answer + "\n"))
			}))
			defer server.Close()
			out, err := runClaimHook(t, server.URL, c.payload, log)
			if failed := err != nil; failed != c.fails || !strings.Contains(out, c.says) {
				t.Fatalf("failed=%v, want %v and %q: %s", failed, c.fails, c.says, out)
			}
			if got, _ := os.ReadFile(log); string(got) != c.log {
				t.Fatalf("ran %q, want %q: %s", got, c.log, out)
			}
		})
	}
}

// An unreachable gate fails open: a gate outage must not fail every job.
func TestJobStartedRunsWhenTheGateIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	unreachable := server.URL
	server.Close()
	log := filepath.Join(t.TempDir(), "log")
	out, err := runClaimHook(t, unreachable, `{"repository":{"full_name":"x/y"},"pull_request":{"head":{"repo":{"full_name":"x/y"}}}}`, log)
	if err != nil || !strings.Contains(out, "could not claim (unreachable); running") {
		t.Fatalf("err %v: %s", err, out)
	}
	if got, _ := os.ReadFile(log); string(got) != "custom\n" {
		t.Fatalf("the pool's script did not run: %q: %s", got, out)
	}
}
