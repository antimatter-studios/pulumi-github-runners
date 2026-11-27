package githubrunners

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }
func bp(b bool) *bool     { return &b }
func ip(i int) *int       { return &i }

func basePool() PoolArgs {
	return PoolArgs{ID: "acme", ConfigURL: "https://github.com/acme", ScaleSetName: "ryzen-linux-x64"}
}

// get walks a decoded tree by keys and indexes, failing the test where the path does not exist.
func get(t *testing.T, v any, path ...any) any {
	t.Helper()
	// Through JSON, so a test reads the values exactly as Helm will.
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var cur any
	if err := json.Unmarshal(raw, &cur); err != nil {
		t.Fatal(err)
	}
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("at %v: not a map", step)
			}
			if cur, ok = m[s]; !ok {
				t.Fatalf("missing %q in %v", s, path)
			}
		case int:
			l, ok := cur.([]any)
			if !ok || s >= len(l) {
				t.Fatalf("at %v: no index %d", path, s)
			}
			cur = l[s]
		}
	}
	return cur
}

func has(t *testing.T, v any, path ...any) bool {
	t.Helper()
	raw, _ := json.Marshal(v)
	var cur any
	_ = json.Unmarshal(raw, &cur)
	for _, step := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[step.(string)]; !ok {
			return false
		}
	}
	return true
}

func container(t *testing.T, tpl map[string]any, name string) map[string]any {
	t.Helper()
	for _, c := range get(t, tpl, "spec", "containers").([]any) {
		if c.(map[string]any)["name"] == name {
			return c.(map[string]any)
		}
	}
	t.Fatalf("no container %s", name)
	return nil
}

func env(t *testing.T, c map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range c["env"].([]any) {
		m := e.(map[string]any)
		out[m["name"].(string)] = m["value"].(string)
	}
	return out
}

// Each test below is one of the antimatter-vps constraints; the names say what broke without it.

func TestRootInThePodIsNotRootOnTheNode(t *testing.T) {
	tpl := podTemplate(basePool())
	if get(t, tpl, "spec", "hostUsers") != false {
		t.Fatal("hostUsers must be false")
	}
	if get(t, tpl, "spec", "automountServiceAccountToken") != false {
		t.Fatal("runner pods must not get a Kubernetes token")
	}
	for _, name := range []string{"runner", "docker"} {
		if container(t, tpl, name)["securityContext"].(map[string]any)["privileged"] == true {
			t.Fatalf("%s is privileged", name)
		}
	}
}

func TestDottedNamesAreLookedUpAsGivenFirst(t *testing.T) {
	opt := get(t, podTemplate(basePool()), "spec", "dnsConfig", "options", 0).(map[string]any)
	if opt["name"] != "ndots" || opt["value"] != "1" {
		t.Fatalf("got %v", opt)
	}
}

func TestTheRunnerImageIsPinnedByDigestAndOverridable(t *testing.T) {
	if c := container(t, podTemplate(basePool()), "runner"); c["image"] != DefaultRunnerImage || !strings.Contains(DefaultRunnerImage, "@sha256:") {
		t.Fatalf("image %v", c["image"])
	}
	a := basePool()
	a.Image = sp("example/runner@sha256:abc")
	if c := container(t, podTemplate(a), "runner"); c["image"] != "example/runner@sha256:abc" {
		t.Fatalf("image %v", c["image"])
	}
	if _, ok := container(t, podTemplate(basePool()), "runner")["command"]; ok {
		t.Fatal("the image starts run.sh itself; a command would replace it")
	}
}

func TestDockerIsTheRootlessPodmanSidecar(t *testing.T) {
	tpl := podTemplate(basePool())
	runner, docker := container(t, tpl, "runner"), container(t, tpl, "docker")
	e := env(t, runner)
	if e["DOCKER_HOST"] != "unix:///run/docker/docker.sock" || e["DOCKER_BUILDKIT"] != "0" {
		t.Fatalf("runner env %v", e)
	}
	sc := docker["securityContext"].(map[string]any)
	caps := sc["capabilities"].(map[string]any)["add"].([]any)
	if !reflect.DeepEqual(caps, []any{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE"}) {
		t.Fatalf("sidecar capabilities %v: SYS_PTRACE is what docker exec and health checks need", caps)
	}
	if sc["procMount"] != "Unmasked" || sc["runAsUser"] != float64(0) {
		t.Fatalf("sidecar security %v", sc)
	}
	cmd := strings.Join(toStrings(docker["command"].([]any)), " ")
	for _, want := range []string{"catatonit", "podman healthcheck run", "podman system service --time=0 unix:///run/docker/docker.sock"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("sidecar command lacks %q", want)
		}
	}
}

func TestThePodmanConfigStripsOomScoreAdjAndUsesNoCgroups(t *testing.T) {
	f := podmanFiles()
	if !strings.Contains(f["containers.conf"], `cgroups = "disabled"`) || !strings.Contains(f["containers.conf"], `crun = ["/etc/arc-podman/crun"]`) {
		t.Fatalf("containers.conf:\n%s", f["containers.conf"])
	}
	for _, ns := range []string{`netns = "bridge"`, `utsns = "private"`, `ipcns = "private"`} {
		if !strings.Contains(f["containers.conf"], ns) {
			t.Errorf("containers.conf lacks %s", ns)
		}
	}
	if !strings.Contains(f["storage.conf"], `mount_program = ""`) {
		t.Fatal("storage must be the kernel's overlay, not fuse-overlayfs")
	}

	// The wrapper itself, run against a process spec like the one podman writes.
	dir := t.TempDir()
	spec := filepath.Join(dir, "process.json")
	if err := os.WriteFile(spec, []byte(`{"args":["sh"],"oomScoreAdj":0,"cwd":"/"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCrun := filepath.Join(dir, "crun")
	wrapper := strings.Replace(f["crun"], "exec /usr/bin/crun", "exec true", 1)
	if err := os.WriteFile(fakeCrun, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fakeCrun, "exec", "--process="+spec, "ctr")
	cmd.Env = append(os.Environ(), "PATH="+gnuSedPath(t, dir)+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, _ := os.ReadFile(spec)
	if strings.Contains(string(got), "oomScoreAdj") {
		t.Fatalf("oomScoreAdj survived: %s", got)
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("the wrapper left invalid JSON: %s", got)
	}
}

func TestTmpAndTheCheckoutAreSharedWithTheSidecar(t *testing.T) {
	tpl := podTemplate(basePool())
	for _, name := range []string{"runner", "docker"} {
		mounts := map[string]string{}
		for _, m := range container(t, tpl, name)["volumeMounts"].([]any) {
			mm := m.(map[string]any)
			mounts[mm["name"].(string)] = mm["mountPath"].(string)
		}
		if mounts["tmp"] != "/tmp" || mounts["work"] != "/home/runner/_work" {
			t.Fatalf("%s mounts %v", name, mounts)
		}
	}
	work := get(t, tpl, "spec", "volumes", 0).(map[string]any)
	if work["name"] != "work" || work["emptyDir"].(map[string]any)["sizeLimit"] != "10Gi" {
		t.Fatalf("work volume %v: node-local, never network storage", work)
	}
}

func TestSizingDefaultsAndNoCPULimitUnlessAsked(t *testing.T) {
	r := container(t, podTemplate(basePool()), "runner")["resources"].(map[string]any)
	if r["requests"].(map[string]any)["cpu"] != "100m" || r["requests"].(map[string]any)["memory"] != "256Mi" {
		t.Fatalf("requests %v", r["requests"])
	}
	lim := r["limits"].(map[string]any)
	if _, ok := lim["cpu"]; ok {
		t.Fatal("no CPU limit by default")
	}
	if lim["memory"] != "4Gi" || lim["ephemeral-storage"] != "10Gi" {
		t.Fatalf("limits %v", lim)
	}
	a := basePool()
	a.CPULimit, a.DockerMemoryLimit = sp("2"), sp("6Gi")
	tpl := podTemplate(a)
	if container(t, tpl, "runner")["resources"].(map[string]any)["limits"].(map[string]any)["cpu"] != "2" {
		t.Fatal("cpuLimit not applied")
	}
	if container(t, tpl, "docker")["resources"].(map[string]any)["limits"].(map[string]any)["memory"] != "6Gi" {
		t.Fatal("dockerMemoryLimit not applied")
	}
}

func TestPodResourcesAreOneSharedBudget(t *testing.T) {
	if has(t, podTemplate(basePool()), "spec", "resources") {
		t.Fatal("no pod-level resources unless asked")
	}
	a := basePool()
	a.PodResources = &PodResources{CPULimit: sp("4"), MemoryLimit: sp("2Gi"), CPURequest: sp("500m"), MemoryRequest: sp("512Mi")}
	got := get(t, podTemplate(a), "spec", "resources")
	want := map[string]any{"limits": map[string]any{"cpu": "4", "memory": "2Gi"}, "requests": map[string]any{"cpu": "500m", "memory": "512Mi"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestTopologySpreadIsHardAndOptional(t *testing.T) {
	if has(t, podTemplate(basePool()), "spec", "topologySpreadConstraints") {
		t.Fatal("off by default: a one-node cluster has nothing to spread across")
	}
	a := basePool()
	a.TopologySpread = bp(true)
	c := get(t, podTemplate(a), "spec", "topologySpreadConstraints", 0).(map[string]any)
	if c["whenUnsatisfiable"] != "DoNotSchedule" || c["maxSkew"] != float64(1) || c["topologyKey"] != "kubernetes.io/hostname" {
		t.Fatalf("got %v", c)
	}
}

func TestDevicesComeFromDevicePluginsNotHostPaths(t *testing.T) {
	a := basePool()
	a.Devices = map[string]string{"devic.es/fuse": "1", "devic.es/kvm": "1"}
	tpl := podTemplate(a)
	lim := container(t, tpl, "runner")["resources"].(map[string]any)["limits"].(map[string]any)
	if lim["devic.es/fuse"] != "1" || lim["devic.es/kvm"] != "1" {
		t.Fatalf("limits %v", lim)
	}
	raw, _ := json.Marshal(tpl)
	if strings.Contains(string(raw), "hostPath") {
		t.Fatal("a hostPath device cannot start with hostUsers: false")
	}
	sc := container(t, tpl, "runner")["securityContext"].(map[string]any)
	if sc["appArmorProfile"].(map[string]any)["type"] != "Unconfined" {
		t.Fatal("FUSE mounts need AppArmor unconfined on the runner")
	}
}

func TestTheHookIsMountedAndCalledBeforeEveryJob(t *testing.T) {
	tpl := podTemplate(basePool())
	if env(t, container(t, tpl, "runner"))["ACTIONS_RUNNER_HOOK_JOB_STARTED"] != "/etc/arc-hooks/job-started.sh" {
		t.Fatal("hook not configured")
	}
	a := basePool()
	a.Hook = &PoolHook{Env: map[string]string{"RUNNER_GATE_URL": "http://runner-gate-agent.arc-systems.svc:8080"}}
	if env(t, container(t, podTemplate(a), "runner"))["RUNNER_GATE_URL"] == "" {
		t.Fatal("hook env not passed to the runner")
	}
}

func TestHookFiles(t *testing.T) {
	files, err := hookFiles(PoolHook{})
	if err != nil {
		t.Fatal(err)
	}
	if files[jobStartedHook] != "#!/bin/bash\nset -euo pipefail\n/etc/arc-hooks/reject-forks.sh\n" || files["reject-forks.sh"] == "" {
		t.Fatalf("default hook: %q", files[jobStartedHook])
	}
	files, err = hookFiles(PoolHook{RejectForks: bp(false), Script: sp("#!/bin/sh\nexit 0\n"), Files: map[string]string{"allowed-actors": "christhomas\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["reject-forks.sh"]; ok || files["custom.sh"] == "" || files["allowed-actors"] != "christhomas\n" {
		t.Fatalf("files %v", files)
	}
	if files[jobStartedHook] != "#!/bin/bash\nset -euo pipefail\n/etc/arc-hooks/custom.sh\n" {
		t.Fatalf("hook %q", files[jobStartedHook])
	}
	for _, bad := range []string{jobStartedHook, "reject-forks.sh", "custom.sh", "../x", "a/b"} {
		if _, err := hookFiles(PoolHook{Files: map[string]string{bad: ""}}); err == nil {
			t.Errorf("file name %q accepted", bad)
		}
	}
}

func TestCheckPool(t *testing.T) {
	ok := basePool()
	ok.Token = stringInput("t")
	if err := checkPool(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*PoolArgs){
		"no credential":          func(a *PoolArgs) { a.Token = nil },
		"both credentials":       func(a *PoolArgs) { a.App = &GitHubApp{AppID: "1", InstallationID: "2", PrivateKey: stringInput("k")} },
		"bad id":                 func(a *PoolArgs) { a.ID = "Acme_Runners" },
		"bad url":                func(a *PoolArgs) { a.ConfigURL = "https://gitlab.com/acme" },
		"label without machine":  func(a *PoolArgs) { a.ScaleSetName = "linux-x64" },
		"antimatter-style label": func(a *PoolArgs) { a.ScaleSetName = "ryzen-linux-x64-kvm" },
		"max below min":          func(a *PoolArgs) { a.MinRunners, a.MaxRunners = ip(3), ip(2) },
		"group without admin":    func(a *PoolArgs) { a.RunnerGroup = &RunnerGroup{Name: "ryzen"} },
		"fork policy w/o admin":  func(a *PoolArgs) { a.ForkPrApproval = sp("all_external_contributors") },
		"reserved hook file":     func(a *PoolArgs) { a.Hook = &PoolHook{Files: map[string]string{"custom.sh": ""}} },
	}
	for name, mutate := range cases {
		a := ok
		mutate(&a)
		if err := checkPool(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, label := range []string{"ryzen-linux-x64", "vps-linux-x64", "macbook-macos-arm64", "ryzen-windows-arm64", "antimatter-vps-linux-x64"} {
		a := ok
		a.ScaleSetName = label
		if err := checkPool(a); err != nil {
			t.Errorf("%s refused: %v", label, err)
		}
	}
}

func TestScaleSetValues(t *testing.T) {
	a := basePool()
	a.MaxRunners = ip(4)
	v := scaleSetValues(a, "arc-github", "ryzen")
	if v["githubConfigUrl"] != "https://github.com/acme" || v["githubConfigSecret"] != "arc-github" ||
		v["runnerScaleSetName"] != "ryzen-linux-x64" || v["runnerGroup"] != "ryzen" || v["minRunners"] != 0 || v["maxRunners"] != 4 {
		t.Fatalf("values %v", v)
	}
	if _, ok := scaleSetValues(basePool(), "s", "")["runnerGroup"]; ok {
		t.Fatal("no group means the Default group, so the key is left out")
	}
	if _, ok := v["containerMode"]; ok {
		t.Fatal("no containerMode: the sidecar is ours, not the chart's dind")
	}
}

func TestNetworkPolicy(t *testing.T) {
	spec := networkPolicySpec(PoolNetworkPolicy{Allow: []NetworkAllow{{PodLabels: map[string]string{"app": "runner-gate-agent"}, Ports: []int{8080}}}})
	internet := get(t, spec, "egress", 1, "to", 0, "ipBlock").(map[string]any)
	if internet["cidr"] != "0.0.0.0/0" || len(internet["except"].([]any)) != 5 {
		t.Fatalf("internet rule %v", internet)
	}
	agent := get(t, spec, "egress", 2).(map[string]any)
	if get(t, agent, "to", 0, "podSelector", "matchLabels", "app") != "runner-gate-agent" || get(t, agent, "ports", 0, "port") != float64(8080) {
		t.Fatalf("exception %v", agent)
	}
	if _, err := toNetworkPolicySpec(spec); err != nil {
		t.Fatal(err)
	}
	open := networkPolicySpec(PoolNetworkPolicy{BlockPrivate: bp(false)})
	if has(t, get(t, open, "egress", 1, "to", 0, "ipBlock"), "except") {
		t.Fatal("blockPrivate false must not block private ranges")
	}
}

func toStrings(v []any) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = s.(string)
	}
	return out
}

// gnuSedPath is a directory whose sed is GNU sed, as in the podman image the wrapper runs in. BSD
// sed (macOS) reads `-i -E` as a backup suffix, so the test would exercise the wrong program.
func gnuSedPath(t *testing.T, dir string) string {
	t.Helper()
	if exec.Command("sed", "--version").Run() == nil {
		return dir
	}
	gsed, err := exec.LookPath("gsed")
	if err != nil {
		t.Skip("GNU sed is not installed")
	}
	bin := filepath.Join(dir, "gnu")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gsed, filepath.Join(bin, "sed")); err != nil {
		t.Fatal(err)
	}
	return bin
}
