package githubrunners

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Every value here comes from antimatter-vps (src/githubActionsRunners.ts), where each one was
// learned on a production cluster. The comments say what broke without it; keep them with the code.

// DefaultRunnerImage is GitHub's runner with the OS tools of GitHub's ubuntu-24.04 image baked in,
// built in github.com/antimatter-studios/actions-runner-image and pinned by digest. Pods used to
// apt-get those tools at start: 42-87 s a job, and on 2026-11-19 one apt-get update hung for 8h45m
// and its runner never registered.
const DefaultRunnerImage = "ghcr.io/antimatter-studios/actions-runner:2.338.2-c01744f@sha256:af60b418707b4407c31c50b230d68d028afbee121746b55dce555d3a089b0277"

// DefaultPodmanImage is the Docker-compatible API for jobs, as a sidecar.
const DefaultPodmanImage = "quay.io/podman/stable:v6.1.3@sha256:1d280cfab8e4cdaa221229c121f681c67463fbd82ad2fa6d13fdcf91c6fc747e"

const (
	dockerSocketDir = "/run/docker"
	hooksDir        = "/etc/arc-hooks"
	podmanConfigDir = "/etc/arc-podman"
	hooksConfigMap  = "arc-runner-hooks"
	podmanConfigMap = "arc-runner-podman"
	jobStartedHook  = "job-started.sh"
)

// podmanStorageConf: the kernel's own overlay. fuse-overlayfs would need /dev/fuse.
const podmanStorageConf = `[storage]
driver = "overlay"
[storage.options.overlay]
# The kernel's own overlay: fuse-overlayfs would need /dev/fuse.
mount_program = ""
`

// podmanContainersConf: no cgroups, because a user namespace cannot create them (the sidecar's own
// limits bound everything), and private namespaces per container, as Docker gives. The podman image
// defaults every namespace to "host", and then `docker run --hostname` fails ("cannot set hostname
// when running in the host UTS namespace") and containers share the pod's network.
const podmanContainersConf = `[containers]
# A user namespace cannot create cgroups; the sidecar's own limits bound everything.
cgroups = "disabled"
netns = "bridge"
utsns = "private"
ipcns = "private"
[engine]
cgroup_manager = "cgroupfs"
[engine.runtimes]
crun = ["/etc/arc-podman/crun"]
`

// crunWrapper is podman's OCI runtime: crun, minus the oomScoreAdj an exec asks for. The Docker API
// sets OomScoreAdj 0 on every container and podman passes it to each exec; lowering a score needs
// CAP_SYS_RESOURCE in the node's own user namespace, which hostUsers: false never has. Without this,
// every `docker exec`, health checks included, failed with "write to /proc/self/oom_score_adj:
// Permission denied".
const crunWrapper = `#!/bin/sh
prev=
for a in "$@"; do
    f=
    case "$a" in --process=*) f=${a#--process=} ;; esac
    [ "$prev" = --process ] && f=$a
    [ -n "$f" ] && [ -f "$f" ] && sed -i -E 's/,"oomScoreAdj":-?[0-9]+//; s/"oomScoreAdj":-?[0-9]+,?//' "$f"
    prev=$a
done
exec /usr/bin/crun "$@"
`

// rejectForksHook refuses every job started by a pull request from a fork, before its first step.
// The runner runs it, so nothing in the pull request can skip it; a non-zero exit fails the job.
//
// Node rather than jq, because node ships inside the runner. The runner carries several builds and
// the newest by name is node24_alpine, linked against musl, which cannot run on the glibc image: the
// first build that runs is used (homelab-server #158).
const rejectForksHook = `#!/bin/bash
set -euo pipefail
node=
for candidate in $(ls -d /home/runner/externals/node*/bin/node 2>/dev/null | sort -rV); do
  if "$candidate" -e 0 2>/dev/null; then node=$candidate; break; fi
done
if [ -z "$node" ]; then echo '::error::reject-forks: no node in the runner that runs here' >&2; exit 1; fi
exec "$node" - "$GITHUB_EVENT_PATH" "$GITHUB_EVENT_NAME" <<'JS'
const [path, name] = process.argv.slice(2);
const event = JSON.parse(require('fs').readFileSync(path, 'utf8'));
const base = event.repository && event.repository.full_name;
let head = null;
if (name.startsWith('pull_request')) {
  head = event.pull_request && event.pull_request.head && event.pull_request.head.repo
    ? event.pull_request.head.repo.full_name : 'a deleted fork';
} else if (name === 'workflow_run') {
  head = event.workflow_run && event.workflow_run.head_repository
    ? event.workflow_run.head_repository.full_name : null;
}
if (head !== null && head !== base) {
  console.error('::error::This runner refuses jobs from forks (' + head + ' -> ' + base + ', event ' + name + ').');
  process.exit(1);
}
JS
`

// jobStartedScript is the hook the runner calls: the fork check first, when the pool asks for it,
// then the pool's own script. Either failing fails the job.
func jobStartedScript(rejectForks bool, custom bool) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -euo pipefail\n")
	if rejectForks {
		b.WriteString(hooksDir + "/reject-forks.sh\n")
	}
	if custom {
		b.WriteString(hooksDir + "/custom.sh\n")
	}
	return b.String()
}

// hookFiles is the ConfigMap the hook directory is mounted from.
func hookFiles(h PoolHook) (map[string]string, error) {
	files := map[string]string{}
	for name, content := range h.Files {
		if name == jobStartedHook || name == "reject-forks.sh" || name == "custom.sh" || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(name) {
			return nil, fmt.Errorf("hook file %q: a reserved or invalid name", name)
		}
		files[name] = content
	}
	rejectForks := h.RejectForks == nil || *h.RejectForks
	custom := h.Script != nil && *h.Script != ""
	if rejectForks {
		files["reject-forks.sh"] = rejectForksHook
	}
	if custom {
		files["custom.sh"] = *h.Script
	}
	files[jobStartedHook] = jobStartedScript(rejectForks, custom)
	return files, nil
}

func podmanFiles() map[string]string {
	return map[string]string{"storage.conf": podmanStorageConf, "containers.conf": podmanContainersConf, "crun": crunWrapper}
}

func orDefault(v *string, d string) string {
	if v == nil || *v == "" {
		return d
	}
	return *v
}

// envList turns a map into the container env list, in name order so the result is stable.
func envList(m map[string]string) []any {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, k := range names {
		out = append(out, map[string]any{"name": k, "value": m[k]})
	}
	return out
}

// podTemplate is the runner pod: the runner and its podman sidecar.
func podTemplate(a PoolArgs) map[string]any {
	runnerLimits := map[string]any{
		"memory":            orDefault(a.MemoryLimit, "4Gi"),
		"ephemeral-storage": orDefault(a.EphemeralStorageLimit, "10Gi"),
	}
	dockerLimits := map[string]any{
		"memory":            orDefault(a.DockerMemoryLimit, "4Gi"),
		"ephemeral-storage": orDefault(a.DockerStorageLimit, "20Gi"),
	}
	// CPU is a guideline by default: a small request places the pod and weights it under contention,
	// and no limit lets a job use whatever is free. A pool on a node that stalls under load sets one.
	if a.CPULimit != nil && *a.CPULimit != "" {
		runnerLimits["cpu"] = *a.CPULimit
	}
	devices := make([]string, 0, len(a.Devices))
	for res := range a.Devices {
		devices = append(devices, res)
	}
	sort.Strings(devices)
	for _, res := range devices {
		runnerLimits[res] = a.Devices[res]
	}

	env := map[string]string{
		"ACTIONS_RUNNER_HOOK_JOB_STARTED": hooksDir + "/" + jobStartedHook,
		"DOCKER_HOST":                     "unix://" + dockerSocketDir + "/docker.sock",
		// podman serves the classic build API; BuildKit needs cgroups.
		"DOCKER_BUILDKIT": "0",
	}
	if a.Hook != nil {
		for k, v := range a.Hook.Env {
			env[k] = v
		}
	}

	runner := map[string]any{
		"name":  "runner",
		"image": orDefault(a.Image, DefaultRunnerImage),
		// When pods scale from zero, the homelab's first Docker step ran about a second
		// before podman created the socket ("dial unix /run/docker/docker.sock: no such file
		// or directory"). The image's run.sh ignores RUNNER_WAIT_FOR_DOCKER_IN_SECONDS.
		// Wait for the socket here as in homelab-server PR #161.
		"command": []any{"sh", "-c", "for i in $(seq 1 120); do [ -S " + dockerSocketDir + "/docker.sock ] && break; sleep 0.5; done; exec /home/runner/run.sh"},
		"env":     envList(env),
		// Memory too: reserve little, cap at the limit. When a node runs out, the kernel and kubelet
		// kill whatever is furthest over its request, so a CI job dies before the cluster's services.
		"resources": map[string]any{
			"requests": map[string]any{
				"cpu":               orDefault(a.CPURequest, "100m"),
				"memory":            orDefault(a.MemoryRequest, "256Mi"),
				"ephemeral-storage": orDefault(a.EphemeralStorageRequest, "2Gi"),
			},
			"limits": runnerLimits,
		},
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": true, // sudo; contained by hostUsers: false
			"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
			// FUSE mounts: root in the pod's user namespace only, as the sidecar is.
			"capabilities":    map[string]any{"add": []any{"SYS_ADMIN"}},
			"appArmorProfile": map[string]any{"type": "Unconfined"},
		},
		"volumeMounts": []any{
			map[string]any{"name": "work", "mountPath": "/home/runner/_work"},
			// Shared /tmp: `docker run -v /tmp/x:/x` resolves the path in the sidecar.
			map[string]any{"name": "tmp", "mountPath": "/tmp"},
			map[string]any{"name": "hooks", "mountPath": hooksDir, "readOnly": true},
			map[string]any{"name": "docker-socket", "mountPath": dockerSocketDir},
		},
	}

	docker := map[string]any{
		"name":  "docker",
		"image": orDefault(a.PodmanImage, DefaultPodmanImage),
		// catatonit reaps the DNS server and other daemons podman leaves behind. podman schedules
		// health checks with systemd timers, and there is no systemd here, so without the loop a
		// services: container stays "starting" and the job never starts.
		"command": []any{"/usr/libexec/podman/catatonit", "--", "sh", "-c",
			"(until [ -S " + dockerSocketDir + "/docker.sock ]; do sleep 0.2; done; chmod 666 " + dockerSocketDir + "/docker.sock) & " +
				"(while sleep 2; do for c in $(podman ps -q --filter health=starting --filter health=unhealthy --filter health=healthy); do podman healthcheck run \"$c\" >/dev/null 2>&1; done; done) & " +
				"exec podman system service --time=0 unix://" + dockerSocketDir + "/docker.sock"},
		"env": []any{
			map[string]any{"name": "CONTAINERS_STORAGE_CONF", "value": podmanConfigDir + "/storage.conf"},
			map[string]any{"name": "CONTAINERS_CONF_OVERRIDE", "value": podmanConfigDir + "/containers.conf"},
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "128Mi", "ephemeral-storage": "1Gi"},
			"limits":   dockerLimits,
		},
		"securityContext": map[string]any{
			// Root of the pod's user namespace: an unprivileged UID on the node.
			"runAsUser":                0,
			"privileged":               false,
			"allowPrivilegeEscalation": true,
			// Namespaced to the pod: mounts and bridges for nested containers. SYS_PTRACE: docker exec
			// and health checks open a container process's namespaces.
			"capabilities":    map[string]any{"add": []any{"SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE"}},
			"procMount":       "Unmasked",
			"seccompProfile":  map[string]any{"type": "Unconfined"},
			"appArmorProfile": map[string]any{"type": "Unconfined"},
		},
		"volumeMounts": []any{
			// Same path as the runner, so `docker run -v $PWD:/src` finds the checkout.
			map[string]any{"name": "work", "mountPath": "/home/runner/_work"},
			map[string]any{"name": "tmp", "mountPath": "/tmp"},
			map[string]any{"name": "docker-socket", "mountPath": dockerSocketDir},
			map[string]any{"name": "podman-config", "mountPath": podmanConfigDir, "readOnly": true},
		},
	}

	spec := map[string]any{
		"automountServiceAccountToken": false,
		// Root in the pod (sudo, apt-get) is an unprivileged UID on the node.
		"hostUsers": false,
		// Look a dotted name up as given first, as GitHub's runners do. With Kubernetes' ndots:5 the
		// namespace's search suffix came first, and a test of which name was queried failed.
		"dnsConfig":  map[string]any{"options": []any{map[string]any{"name": "ndots", "value": "1"}}},
		"containers": []any{runner, docker},
		"volumes": []any{
			// Node-local, never network storage.
			map[string]any{"name": "work", "emptyDir": map[string]any{"sizeLimit": orDefault(a.WorkVolumeSize, "10Gi")}},
			map[string]any{"name": "hooks", "configMap": map[string]any{"name": hooksConfigMap, "defaultMode": 0o555}},
			map[string]any{"name": "docker-socket", "emptyDir": map[string]any{}},
			map[string]any{"name": "tmp", "emptyDir": map[string]any{}},
			map[string]any{"name": "podman-config", "configMap": map[string]any{"name": podmanConfigMap, "defaultMode": 0o555}},
		},
	}
	// Spread a pool's runners evenly across nodes, HARD: soft spreading put four on one 4-core node
	// and a `go test -race` suite timed out. On a one-node cluster it does nothing, so it is optional.
	if a.TopologySpread != nil && *a.TopologySpread {
		spec["topologySpreadConstraints"] = []any{map[string]any{
			"maxSkew":           1,
			"topologyKey":       "kubernetes.io/hostname",
			"whenUnsatisfiable": "DoNotSchedule",
			"labelSelector":     map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/component": "runner"}},
		}}
	}
	// One budget the runner and the sidecar share (pod-level resources, Kubernetes 1.34+), instead of
	// a fixed split that leaves one container's share idle while the other is OOM-killed.
	if r := a.PodResources; r != nil {
		res := map[string]any{}
		if lim := quantities(r.CPULimit, r.MemoryLimit); len(lim) > 0 {
			res["limits"] = lim
			// Both containers share these limits. Kubernetes rejects container limits above
			// the pod limit, including our default memory limits when the pod budget is smaller.
			for resource := range lim {
				delete(runnerLimits, resource)
				delete(dockerLimits, resource)
			}
		}
		if req := quantities(r.CPURequest, r.MemoryRequest); len(req) > 0 {
			res["requests"] = req
		}
		if len(res) > 0 {
			spec["resources"] = res
		}
	}
	if len(a.SupplementalGroups) > 0 {
		groups := make([]any, len(a.SupplementalGroups))
		for i, g := range a.SupplementalGroups {
			groups[i] = g
		}
		spec["securityContext"] = map[string]any{"supplementalGroups": groups}
	}
	return map[string]any{"spec": spec}
}

func quantities(cpu, memory *string) map[string]any {
	out := map[string]any{}
	if cpu != nil && *cpu != "" {
		out["cpu"] = *cpu
	}
	if memory != nil && *memory != "" {
		out["memory"] = *memory
	}
	return out
}

// scaleSetValues are the gha-runner-scale-set chart's values for a pool.
func scaleSetValues(a PoolArgs, secretName string, runnerGroup string) map[string]any {
	v := map[string]any{
		"githubConfigUrl":    strings.TrimSuffix(a.ConfigURL, "/"),
		"githubConfigSecret": secretName,
		"runnerScaleSetName": a.ScaleSetName,
		"minRunners":         intOr(a.MinRunners, 0),
		"maxRunners":         intOr(a.MaxRunners, 2),
		"template":           podTemplate(a),
	}
	if runnerGroup != "" {
		v["runnerGroup"] = runnerGroup
	}
	return v
}

func intOr(v *int, d int) int {
	if v == nil {
		return d
	}
	return *v
}

var (
	poolIDPattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
	configURLPattern = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?$`)
	// <machine>-<os>-<arch>, e.g. ryzen-linux-x64.
	scaleSetPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*-(?:linux|windows|macos)-(?:x64|arm64)$`)
)

// checkPool refuses a pool that cannot work, before anything is created.
func checkPool(a PoolArgs) error {
	if !poolIDPattern.MatchString(a.ID) {
		return fmt.Errorf("pool id %q must be a DNS-safe name of at most 40 characters", a.ID)
	}
	if !configURLPattern.MatchString(strings.TrimSuffix(a.ConfigURL, "/")) {
		return fmt.Errorf("pool %s: configUrl %q must be a GitHub organisation or repository URL", a.ID, a.ConfigURL)
	}
	if !scaleSetPattern.MatchString(a.ScaleSetName) {
		return fmt.Errorf("pool %s: scaleSetName %q must be <machine>-<os>-<arch>, e.g. ryzen-linux-x64", a.ID, a.ScaleSetName)
	}
	minR, maxR := intOr(a.MinRunners, 0), intOr(a.MaxRunners, 2)
	if minR < 0 {
		return fmt.Errorf("pool %s: minRunners must not be negative", a.ID)
	}
	if maxR < 1 || maxR < minR {
		return fmt.Errorf("pool %s: maxRunners must be at least 1 and at least minRunners", a.ID)
	}
	if (a.App == nil) == (a.Token == nil) {
		return fmt.Errorf("pool %s: give exactly one of app (a GitHub App, recommended) or token (a PAT)", a.ID)
	}
	if a.RunnerGroup != nil && orgOf(a.ConfigURL) == "" {
		return fmt.Errorf("pool %s: a runner group needs an organisation configUrl", a.ID)
	}
	if a.Hook != nil {
		if _, err := hookFiles(*a.Hook); err != nil {
			return fmt.Errorf("pool %s: %w", a.ID, err)
		}
	}
	return nil
}

// orgOf is the organisation of an organisation URL, or "" for a repository URL.
func orgOf(configURL string) string {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSuffix(configURL, "/"), "https://github.com/"), "/")
	if len(parts) != 1 {
		return ""
	}
	return parts[0]
}
