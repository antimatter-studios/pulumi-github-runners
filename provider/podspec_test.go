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
}

var wantRunnerCommand = []any{"sh", "-c", "for i in $(seq 1 120); do [ -S /run/docker/docker.sock ] && break; sleep 0.5; done; exec /home/runner/run.sh"}

func TestRunnerWaitsForPodmanSocketBeforeStarting(t *testing.T) {
	cmd := container(t, podTemplate(basePool()), "runner")["command"]
	if !reflect.DeepEqual(cmd, wantRunnerCommand) {
		t.Fatalf("runner command %v, want %v", cmd, wantRunnerCommand)
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

// No CPU or memory limit by default: the owner's decision, runners use whatever is free.
func TestSizingDefaultsAndNoCPULimitUnlessAsked(t *testing.T) {
	r := container(t, podTemplate(basePool()), "runner")["resources"].(map[string]any)
	if r["requests"].(map[string]any)["cpu"] != "100m" || r["requests"].(map[string]any)["memory"] != "256Mi" {
		t.Fatalf("requests %v", r["requests"])
	}
	lim := r["limits"].(map[string]any)
	if _, ok := lim["cpu"]; ok {
		t.Fatal("no CPU limit by default")
	}
	if _, ok := lim["memory"]; ok || lim["ephemeral-storage"] != "10Gi" {
		t.Fatalf("limits %v: no memory limit by default, only the disk limit", lim)
	}
	if dl := container(t, podTemplate(basePool()), "docker")["resources"].(map[string]any)["limits"].(map[string]any); dl["memory"] != nil || dl["ephemeral-storage"] != "20Gi" {
		t.Fatalf("sidecar limits %v: no memory limit by default, only the disk limit", dl)
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

func TestDockerMemoryRequest(t *testing.T) {
	if r := container(t, podTemplate(basePool()), "docker")["resources"].(map[string]any)["requests"].(map[string]any); r["memory"] != "128Mi" {
		t.Fatalf("sidecar requests %v, want 128Mi by default", r)
	}
	a := basePool()
	a.MemoryRequest, a.DockerMemoryRequest = sp("1Gi"), sp("1Gi")
	tpl := podTemplate(a)
	if r := container(t, tpl, "docker")["resources"].(map[string]any)["requests"].(map[string]any); r["memory"] != "1Gi" {
		t.Fatalf("sidecar requests %v: dockerMemoryRequest not applied", r)
	}
	if r := container(t, tpl, "runner")["resources"].(map[string]any)["requests"].(map[string]any); r["memory"] != "1Gi" {
		t.Fatalf("runner requests %v: memoryRequest not applied", r)
	}
	if l := container(t, tpl, "docker")["resources"].(map[string]any)["limits"].(map[string]any); l["memory"] != nil {
		t.Fatalf("sidecar limits %v: a request must not bring a memory limit", l)
	}
}

func TestPodResourcesAreOneSharedBudget(t *testing.T) {
	if has(t, podTemplate(basePool()), "spec", "resources") {
		t.Fatal("no pod-level resources unless asked")
	}
	a := basePool()
	a.PodResources = &PodResources{CPULimit: sp("4"), MemoryLimit: sp("2Gi"), CPURequest: sp("500m"), MemoryRequest: sp("512Mi")}
	a.CPULimit = sp("8")
	a.Devices = map[string]string{"devic.es/kvm": "1"}
	tpl := podTemplate(a)
	got := get(t, tpl, "spec", "resources")
	want := map[string]any{"limits": map[string]any{"cpu": "4", "memory": "2Gi"}, "requests": map[string]any{"cpu": "500m", "memory": "512Mi"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	for name, limits := range map[string]map[string]any{
		"runner": {"ephemeral-storage": "10Gi", "devic.es/kvm": "1"},
		"docker": {"ephemeral-storage": "20Gi", "devic.es/kvm": "1"},
	} {
		if got := get(t, container(t, tpl, name), "resources", "limits"); !reflect.DeepEqual(got, limits) {
			t.Errorf("%s limits: got %v, want %v", name, got, limits)
		}
	}
	t.Run("CPU limit only", func(t *testing.T) {
		a.MemoryLimit, a.DockerMemoryLimit = sp("4Gi"), sp("4Gi")
		a.PodResources = &PodResources{CPULimit: sp("4"), MemoryRequest: sp("512Mi")}
		tpl := podTemplate(a)
		for _, name := range []string{"runner", "docker"} {
			limits := get(t, container(t, tpl, name), "resources", "limits").(map[string]any)
			if limits["memory"] != "4Gi" || limits["cpu"] != nil {
				t.Errorf("%s limits: got %v, want the container memory limit only", name, limits)
			}
		}
	})
	t.Run("memory limit only", func(t *testing.T) {
		a.PodResources = &PodResources{MemoryLimit: sp("2Gi"), CPURequest: sp("500m")}
		limits := get(t, container(t, podTemplate(a), "runner"), "resources", "limits").(map[string]any)
		if limits["cpu"] != "8" || limits["memory"] != nil {
			t.Errorf("runner limits: got %v, want the container CPU limit only", limits)
		}
	})
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
	for _, name := range []string{"runner", "docker"} {
		// The sidecar needs them too: containers a job starts with `docker run --device` run there.
		lim := container(t, tpl, name)["resources"].(map[string]any)["limits"].(map[string]any)
		if lim["devic.es/fuse"] != "1" || lim["devic.es/kvm"] != "1" {
			t.Fatalf("%s limits %v", name, lim)
		}
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

func TestSlotsAreOptionalAndTakenByTheRunnerOnly(t *testing.T) {
	for _, slots := range []*PoolSlots{nil, {}, {Enabled: bp(false), ResourceName: sp("example.com/slot")}} {
		a := basePool()
		a.Slots = slots
		raw, _ := json.Marshal(podTemplate(a))
		if strings.Contains(string(raw), "slot") {
			t.Fatalf("slots %+v: a pool without slots must schedule as before, got %s", slots, raw)
		}
	}
	for want, slots := range map[string]*PoolSlots{
		defaultSlotResource: {Enabled: bp(true)},
		"example.com/slot":  {Enabled: bp(true), ResourceName: sp("example.com/slot")},
	} {
		a := basePool()
		a.Slots = slots
		tpl := podTemplate(a)
		resources := container(t, tpl, "runner")["resources"].(map[string]any)
		// Requests and limits both: the API server refuses an extended resource whose request
		// differs from its limit.
		if resources["requests"].(map[string]any)[want] != "1" || resources["limits"].(map[string]any)[want] != "1" {
			t.Fatalf("runner resources %v, want one %s", resources, want)
		}
		// One per pod: the sidecar taking one too would halve the node's runners.
		raw, _ := json.Marshal(container(t, tpl, "docker"))
		if strings.Contains(string(raw), want) {
			t.Fatalf("the docker sidecar takes a slot: %s", raw)
		}
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
	files, err := hookFiles(PoolHook{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if files[jobStartedHook] != "#!/bin/bash\nset -euo pipefail\n/etc/arc-hooks/reject-forks.sh\n" || files["reject-forks.sh"] == "" {
		t.Fatalf("default hook: %q", files[jobStartedHook])
	}
	files, err = hookFiles(PoolHook{RejectForks: bp(false), Script: sp("#!/bin/sh\nexit 0\n"), Files: map[string]string{"allowed-actors": "christhomas\n"}}, false)
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
		if _, err := hookFiles(PoolHook{Files: map[string]string{bad: ""}}, false); err == nil {
			t.Errorf("file name %q accepted", bad)
		}
	}
}

func TestCheckPool(t *testing.T) {
	ok := basePool()
	ok.App = &GitHubApp{AppID: "1", InstallationID: "2", PrivateKey: stringInput("k")}
	if err := checkPool(ok); err != nil {
		t.Fatal(err)
	}
	for name, credential := range map[string]PoolArgs{
		"App": ok,
		"PAT": {ID: ok.ID, ConfigURL: ok.ConfigURL, ScaleSetName: ok.ScaleSetName, Token: stringInput("t")},
	} {
		credential.RunnerGroup = &RunnerGroup{Name: "ryzen"}
		if err := checkPool(credential); err != nil {
			t.Errorf("runner group with %s: %v", name, err)
		}
	}
	cases := map[string]func(*PoolArgs){
		"no credential":          func(a *PoolArgs) { a.App = nil },
		"both credentials":       func(a *PoolArgs) { a.Token = stringInput("t") },
		"bad id":                 func(a *PoolArgs) { a.ID = "Acme_Runners" },
		"bad url":                func(a *PoolArgs) { a.ConfigURL = "https://gitlab.com/acme" },
		"label without machine":  func(a *PoolArgs) { a.ScaleSetName = "linux-x64" },
		"antimatter-style label": func(a *PoolArgs) { a.ScaleSetName = "ryzen-linux-x64-kvm" },
		"max below min":          func(a *PoolArgs) { a.MinRunners, a.MaxRunners = ip(3), ip(2) },
		"group without a credential": func(a *PoolArgs) {
			a.App, a.RunnerGroup = nil, &RunnerGroup{Name: "ryzen"}
		},
		"group with a repository URL": func(a *PoolArgs) {
			a.ConfigURL, a.RunnerGroup = "https://github.com/acme/repo", &RunnerGroup{Name: "ryzen"}
		},
		"reserved hook file": func(a *PoolArgs) { a.Hook = &PoolHook{Files: map[string]string{"custom.sh": ""}} },
		"agent floor above maxRunners": func(a *PoolArgs) {
			a.MaxRunners, a.MaxRunnersAgent = ip(4), &PoolMaxRunnersAgent{Enabled: bp(true), Floor: ip(5)}
		},
		"agent floor below minRunners": func(a *PoolArgs) {
			a.MinRunners, a.MaxRunners, a.MaxRunnersAgent = ip(2), ip(4), &PoolMaxRunnersAgent{Enabled: bp(true), Floor: ip(1)}
		},
		"slot without a domain": func(a *PoolArgs) {
			a.Slots = &PoolSlots{Enabled: bp(true), ResourceName: sp("slot")}
		},
		"slot in kubernetes.io": func(a *PoolArgs) {
			a.Slots = &PoolSlots{Enabled: bp(true), ResourceName: sp("kubernetes.io/slot")}
		},
		"slot that is also a device": func(a *PoolArgs) {
			a.Slots = &PoolSlots{Enabled: bp(true), ResourceName: sp("devic.es/kvm")}
			a.Devices = map[string]string{"devic.es/kvm": "1"}
		},
	}
	for name, mutate := range cases {
		a := ok
		mutate(&a)
		if err := checkPool(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	withSlots := ok
	withSlots.Slots = &PoolSlots{Enabled: bp(true)}
	if err := checkPool(withSlots); err != nil {
		t.Errorf("default slot resource refused: %v", err)
	}
	for _, label := range []string{"ryzen-linux-x64", "vps-linux-x64", "macbook-macos-arm64", "ryzen-windows-arm64", "antimatter-vps-linux-x64"} {
		a := ok
		a.ScaleSetName = label
		if err := checkPool(a); err != nil {
			t.Errorf("%s refused: %v", label, err)
		}
	}
}

// gateClaim sets RUNNER_GATE_URL from the URL or the agent's namespace, over anything the pool or its
// hook set; without it the variable is the pool's own business.
func TestGateClaimSetsTheClaimURL(t *testing.T) {
	for name, c := range map[string]struct {
		claim PoolGateClaim
		want  string
	}{
		"url":               {PoolGateClaim{URL: sp("http://gate.example:9000")}, "http://gate.example:9000"},
		"url with a slash":  {PoolGateClaim{URL: sp("http://gate.example:9000/")}, "http://gate.example:9000"},
		"agent namespace":   {PoolGateClaim{AgentNamespace: sp("arc-systems")}, "http://runner-gate-agent.arc-systems.svc:8080"},
		"another namespace": {PoolGateClaim{AgentNamespace: sp("gate")}, "http://runner-gate-agent.gate.svc:8080"},
	} {
		a := basePool()
		a.GateClaim = &c.claim
		a.Env = map[string]string{"RUNNER_GATE_URL": "http://pool-env"}
		a.Hook = &PoolHook{Env: map[string]string{"RUNNER_GATE_URL": "http://hook-env"}}
		if got := env(t, container(t, podTemplate(a), "runner"))["RUNNER_GATE_URL"]; got != c.want {
			t.Errorf("%s: RUNNER_GATE_URL %q, want %q", name, got, c.want)
		}
	}
	if _, ok := env(t, container(t, podTemplate(basePool()), "runner"))["RUNNER_GATE_URL"]; ok {
		t.Fatal("no gateClaim: no claim URL")
	}
}

func TestGateClaimHookFiles(t *testing.T) {
	files, err := hookFiles(PoolHook{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if files[jobStartedHook] != "#!/bin/bash\nset -euo pipefail\n/etc/arc-hooks/reject-forks.sh\n/etc/arc-hooks/gate-claim.sh\n" ||
		!strings.Contains(files["gate-claim.sh"], "# Claim this job's reservation.") {
		t.Fatalf("hook %q", files[jobStartedHook])
	}
	files, err = hookFiles(PoolHook{RejectForks: bp(false), Script: sp("#!/bin/sh\n")}, true)
	if err != nil {
		t.Fatal(err)
	}
	if files[jobStartedHook] != "#!/bin/bash\nset -euo pipefail\n/etc/arc-hooks/gate-claim.sh\n/etc/arc-hooks/custom.sh\n" {
		t.Fatalf("hook %q", files[jobStartedHook])
	}
	if _, err := hookFiles(PoolHook{Files: map[string]string{"gate-claim.sh": ""}}, true); err == nil {
		t.Fatal("with gateClaim the name is the plugin's")
	}
	// A pool that mounts its own copy, without gateClaim, keeps working.
	own, err := hookFiles(PoolHook{Files: map[string]string{"gate-claim.sh": "mine"}}, false)
	if err != nil || own["gate-claim.sh"] != "mine" || strings.Contains(own[jobStartedHook], "gate-claim.sh") {
		t.Fatalf("err %v, files %v", err, own)
	}
}

// The runners' egress policy has to let them reach the agent they claim from.
func TestGateClaimNamespaceIsTheNetworkPolicysDefault(t *testing.T) {
	a := basePool()
	a.NetworkPolicy = &PoolNetworkPolicy{}
	if got := poolNetworkPolicy(a).GateAgentNamespace; got != nil {
		t.Fatalf("no gateClaim: %q", *got)
	}
	a.GateClaim = &PoolGateClaim{AgentNamespace: sp("gate")}
	if got := poolNetworkPolicy(a).GateAgentNamespace; got == nil || *got != "gate" {
		t.Fatalf("gate agent namespace %v", got)
	}
	if a.NetworkPolicy.GateAgentNamespace != nil {
		t.Fatal("the caller's args were changed")
	}
	a.NetworkPolicy.GateAgentNamespace = sp("elsewhere")
	if got := poolNetworkPolicy(a).GateAgentNamespace; got == nil || *got != "elsewhere" {
		t.Fatalf("an explicit namespace wins: %v", got)
	}
}

func TestCheckGateClaim(t *testing.T) {
	ok := basePool()
	ok.Token = stringInput("t")
	for name, claim := range map[string]PoolGateClaim{
		"url":             {URL: sp("https://gate.example")},
		"agent namespace": {AgentNamespace: sp("arc-systems")},
	} {
		a := ok
		a.GateClaim = &claim
		if err := checkPool(a); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	for name, claim := range map[string]PoolGateClaim{
		"neither":            {},
		"empty strings":      {URL: sp(""), AgentNamespace: sp("")},
		"both":               {URL: sp("http://gate:8080"), AgentNamespace: sp("arc-systems")},
		"url without host":   {URL: sp("http://")},
		"url without scheme": {URL: sp("gate:8080")},
		"url with a query":   {URL: sp("http://gate:8080?x=1")},
		"bad namespace":      {AgentNamespace: sp("Arc.Systems")},
	} {
		a := ok
		a.GateClaim = &claim
		if err := checkPool(a); err == nil || !strings.Contains(err.Error(), "gateClaim") {
			t.Errorf("%s: %v", name, err)
		}
	}
	reserved := ok
	reserved.GateClaim = &PoolGateClaim{AgentNamespace: sp("arc-systems")}
	reserved.Hook = &PoolHook{Files: map[string]string{"gate-claim.sh": ""}}
	if err := checkPool(reserved); err == nil {
		t.Error("a hook file over the claim script: accepted")
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

func TestMaxRunnersStaysDeclaredUnlessTheAgentOwnsIt(t *testing.T) {
	for _, agent := range []*PoolMaxRunnersAgent{nil, {}, {Enabled: bp(false), Floor: ip(1)}} {
		a := basePool()
		a.MaxRunners = ip(6)
		a.MaxRunnersAgent = agent
		v := scaleSetValues(a, "s", "")
		if v["maxRunners"] != 6 || has(t, v, "resourceMeta") {
			t.Fatalf("agent %+v: values %v, want the declared maxRunners and no ceiling", agent, v)
		}
	}
}

func TestTheAgentStartsAPoolAtTheFloorUnderItsCeiling(t *testing.T) {
	for _, tc := range []struct {
		name              string
		min, floor        *int
		wantMax, wantMinR int
	}{
		{"default floor", nil, nil, 1, 0},
		{"floor follows minRunners", ip(3), nil, 3, 3},
		{"explicit floor", ip(1), ip(2), 2, 1},
	} {
		a := basePool()
		a.MinRunners, a.MaxRunners = tc.min, ip(6)
		a.MaxRunnersAgent = &PoolMaxRunnersAgent{Enabled: bp(true), Floor: tc.floor}
		if err := checkPool(withApp(a)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		v := scaleSetValues(a, "s", "")
		if v["maxRunners"] != tc.wantMax || v["minRunners"] != tc.wantMinR {
			t.Errorf("%s: maxRunners %v minRunners %v, want %d and %d", tc.name, v["maxRunners"], v["minRunners"], tc.wantMax, tc.wantMinR)
		}
		// A string: annotation values are strings, and the chart quotes them anyway.
		if got := get(t, v, "resourceMeta", "autoscalingRunnerSet", "annotations", maxRunnersCeilingAnnotation); got != "6" {
			t.Errorf("%s: ceiling %v, want \"6\"", tc.name, got)
		}
	}
}

func withApp(a PoolArgs) PoolArgs {
	a.App = &GitHubApp{AppID: "1", InstallationID: "2", PrivateKey: stringInput("k")}
	return a
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

// A pool's job environment reaches every job, and cannot override what the runner itself depends on.
func TestPoolEnvIsEveryJobsDefault(t *testing.T) {
	a := basePool()
	a.Env = map[string]string{"GOMAXPROCS": "2", "FLTH_RESOURCE_PROFILE": "low", "DOCKER_HOST": "tcp://elsewhere"}
	e := env(t, container(t, podTemplate(a), "runner"))
	if e["GOMAXPROCS"] != "2" || e["FLTH_RESOURCE_PROFILE"] != "low" {
		t.Fatalf("env %v", e)
	}
	if e["DOCKER_HOST"] != "unix:///run/docker/docker.sock" {
		t.Fatalf("a pool overrode DOCKER_HOST: %v", e["DOCKER_HOST"])
	}
}

func TestPriorityClassIsOptional(t *testing.T) {
	if has(t, podTemplate(basePool()), "spec", "priorityClassName") {
		t.Fatal("no priority class unless asked")
	}
	a := basePool()
	a.PriorityClassName = sp("ci-runner")
	if get(t, podTemplate(a), "spec", "priorityClassName") != "ci-runner" {
		t.Fatal("priorityClassName not applied")
	}
}
