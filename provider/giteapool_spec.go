package githubrunners

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"sigs.k8s.io/yaml"
)

// A GiteaRunnerPool's inputs, its checks and the plain data it is built from. The design started
// from antimatter-vps (src/giteaActions.ts, the official actions chart with a privileged dind
// sidecar); the rootless engine is this plugin's own, from Pool. ARC cannot drive Gitea runners,
// which speak another protocol, so the two share the pod design and nothing else.
//
// This first version is a fixed-size StatefulSet of long-lived runners. Later: ephemeral runners
// (gitea-runner register --ephemeral, one job each) started by a poller of the instance's queued
// jobs, taking node slots from the same budget as Pool's runners (#22-#28, Pool.slots), so both
// kinds of runner follow one measure of a node's load.

const (
	// DefaultGiteaRunnerImage is Gitea's runner, pinned by digest: the version antimatter-vps runs.
	DefaultGiteaRunnerImage = "docker.gitea.com/runner:2.0.1@sha256:f0348958d5a425584bd319be27315cd022c34153dec555778ea0164d852fe160"
	// DefaultGiteaPodmanImage is the rootless engine of a Gitea pool, older than Pool's
	// DefaultPodmanImage on purpose. The runner copies each action into a docker:// job's container as
	// an archive under /var/run/act, and podman 6.1.3's Docker API refuses to follow the image's
	// /var/run symlink ("statat var/run/act: path escapes from parent"), failing every `uses:` step.
	// 5.8 copies it. Run actions/checkout in a docker:// job before changing this.
	DefaultGiteaPodmanImage = "quay.io/podman/stable:v5.8@sha256:f7a17ef911a2b9b866ada8a9426a07e29a6e4f8109ef8c0e0b7010c2541b4e49"
	// DefaultDindImage is the engine of a pool with engine "dind", pinned by digest.
	DefaultDindImage = "docker:29-dind@sha256:fbbc96e0af32cdc190aff92ba97a1281a115351a723afe21af78879cc55126b0"
)

const (
	scopeInstance = "instance"
	scopeOrg      = "org"
	scopeUser     = "user"
	scopeRepo     = "repo"

	engineRootless   = "podman"
	enginePrivileged = "dind"

	// giteaSocketDir holds the engine's socket in both containers. Not /run/docker, which is
	// dockerd's own exec root.
	giteaSocketDir      = "/run/engine"
	giteaDataDir        = "/data"
	giteaConfigDir      = "/etc/gitea-runner"
	giteaTokenDir       = "/run/secrets/gitea-runner"
	giteaTokenKey       = "token"
	giteaWorkDir        = "/home/runner/_work"
	giteaEngineVolume   = "engine-storage"
	giteaConfigFile     = giteaConfigDir + "/config.yaml"
	giteaSecretName     = "gitea-runner-registration"
	giteaConfigMap      = "gitea-runner-config"
	giteaPodmanMap      = "gitea-runner-podman"
	giteaConfigChecksum = "github-runners.antimatter.studios/config-checksum"
)

var giteaDefaultLabels = []string{
	"ubuntu-latest:docker://docker.gitea.com/runner-images:ubuntu-latest",
	"ubuntu-24.04:docker://docker.gitea.com/runner-images:ubuntu-24.04",
	"ubuntu-22.04:docker://docker.gitea.com/runner-images:ubuntu-22.04",
}

var (
	giteaNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	// <name>, <name>:host or <name>:docker://<image>, as the runner reads them.
	giteaLabelPattern = regexp.MustCompile(`^[^\s,:]+(?::host|:docker://[^\s,]+)?$`)
)

// GiteaRunnerPoolArgs are a GiteaRunnerPool's inputs: one set of identical runners for one
// repository, organisation or Gitea instance.
type GiteaRunnerPoolArgs struct {
	// Not `id`, which Pulumi reserves (see PoolArgs.ID).
	ID                string             `pulumi:"poolId"`
	InstanceURL       string             `pulumi:"instanceUrl"`
	Scope             string             `pulumi:"scope"`
	Owner             *string            `pulumi:"owner,optional"`
	Repo              *string            `pulumi:"repo,optional"`
	RegistrationToken pulumi.StringInput `pulumi:"registrationToken" provider:"secret"`

	Labels   []string `pulumi:"labels,optional"`
	Replicas *int     `pulumi:"replicas,optional"`

	Capacity         *int    `pulumi:"capacity,optional"`
	ContainerOptions *string `pulumi:"containerOptions,optional"`

	Engine      *string `pulumi:"engine,optional"`
	Image       *string `pulumi:"image,optional"`
	EngineImage *string `pulumi:"engineImage,optional"`

	RunnerResources    *GiteaContainerResources `pulumi:"runnerResources,optional"`
	EngineResources    *GiteaContainerResources `pulumi:"engineResources,optional"`
	RunnerStorageLimit *string                  `pulumi:"runnerStorageLimit,optional"`
	WorkVolumeSize     *string                  `pulumi:"workVolumeSize,optional"`

	PersistentEngineStorage *bool   `pulumi:"persistentEngineStorage,optional"`
	EngineStorageSize       *string `pulumi:"engineStorageSize,optional"`
	EngineStorageClassName  *string `pulumi:"engineStorageClassName,optional"`

	StorageClassName *string `pulumi:"storageClassName,optional"`
	StorageSize      *string `pulumi:"storageSize,optional"`

	UpdatePartition   *int              `pulumi:"updatePartition,optional"`
	PriorityClassName *string           `pulumi:"priorityClassName,optional"`
	NodeSelector      map[string]string `pulumi:"nodeSelector,optional"`
	TopologySpread    *bool             `pulumi:"topologySpread,optional"`
	Env               map[string]string `pulumi:"env,optional"`

	AutomountServiceAccountToken *bool   `pulumi:"automountServiceAccountToken,optional"`
	ServiceAccountName           *string `pulumi:"serviceAccountName,optional"`
}

// GiteaContainerResources are one container's CPU and memory.
type GiteaContainerResources struct {
	CPURequest    *string `pulumi:"cpuRequest,optional"`
	CPULimit      *string `pulumi:"cpuLimit,optional"`
	MemoryRequest *string `pulumi:"memoryRequest,optional"`
	MemoryLimit   *string `pulumi:"memoryLimit,optional"`
}

func (a *GiteaRunnerPoolArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.ID, "A DNS-safe key for the pool; its runners live in namespace gitea-runners-<poolId> and register as <poolId>-0, <poolId>-1, ...")
	an.Describe(&a.InstanceURL, "The Gitea instance the runners connect to, e.g. https://git.example.com, or its in-cluster Service.")
	an.Describe(&a.Scope, "What the runners serve: instance, org, user or repo. The registration token must be one minted for it.")
	an.Describe(&a.Owner, "The organisation (scope org), the user (scope user) or the repository's owner (scope repo). Not set for scope instance.")
	an.Describe(&a.Repo, "The repository's name, without its owner. Only for scope repo.")
	an.Describe(&a.RegistrationToken, "A runner registration token for the scope: POST <instanceUrl>/api/v1/repos/{owner}/{repo}/actions/runners/registration-token for a repository; /api/v1/orgs/{org}, /api/v1/user (asked as that user) or /api/v1/admin in place of /api/v1/repos/{owner}/{repo} for the other scopes. One token registers any number of runners. Read only when a runner registers.")
	an.Describe(&a.Capacity, "Jobs each runner takes at once. Default 1. Jobs of one runner share its container, engine and disk.")
	an.Describe(&a.ContainerOptions, "Options added to the docker create of every job container, e.g. \"--add-host=git:10.0.0.1\". Jobs that run on the host are not containers.")
	an.Describe(&a.Labels, "The labels workflows name in runs-on. <name>:docker://<image> runs the job in a container of that image in the engine; <name>:host, or a bare <name>, runs its steps in the runner container, which needs image. Default: ubuntu-latest, ubuntu-24.04 and ubuntu-22.04 as containers of Gitea's runner-images.")
	an.Describe(&a.Replicas, "Runners. Default 1. With capacity, the pool takes replicas times capacity jobs at once.")
	an.Describe(&a.Engine, "The Docker sidecar jobs run in: podman (default), rootless in the pod's own user namespace, or dind, Docker in a privileged container.")
	an.Describe(&a.Image, "The runner container's image, pinned by digest. Default: Gitea's runner. Required with host labels: an image with the tools jobs need, plus gitea-runner and tini on its PATH.")
	an.Describe(&a.EngineImage, "The engine sidecar's image, pinned by digest. Default: podman, or docker:dind when engine is dind.")
	an.Describe(&a.RunnerResources, "The runner container's CPU and memory, which is where host jobs run. No limits unless set.")
	an.Describe(&a.EngineResources, "The engine sidecar's CPU and memory, which is where job containers run. No limits unless set.")
	an.Describe(&a.RunnerStorageLimit, "The runner container's ephemeral storage limit, which holds the actions cache. Default 10Gi.")
	an.Describe(&a.WorkVolumeSize, "The workspace host jobs share with the engine, an emptyDir. Used only with host labels. Default 10Gi.")
	an.Describe(&a.PersistentEngineStorage, "Give each runner its own volume for the engine's images and containers, so a restart pulls nothing again. Default true; false uses an emptyDir.")
	an.Describe(&a.EngineStorageSize, "Disk for the engine's images, containers and job workspaces. Default 20Gi.")
	an.Describe(&a.EngineStorageClassName, "StorageClass of the engine's volume, e.g. a node-local one: it is a cache. Default: the cluster's default class. With engine podman it must support idmapped mounts and overlayfs.")
	an.Describe(&a.StorageClassName, "StorageClass of each runner's /data claim, which holds its registration. Default: the cluster's default class. With engine podman it must support idmapped mounts.")
	an.Describe(&a.StorageSize, "Size of each runner's /data claim. Default 1Gi. Changing a claim's size or class replaces the StatefulSet.")
	an.Describe(&a.UpdatePartition, "Runners with an ordinal below this keep their old pod spec on a change, e.g. so a deploy running on them is not restarted by itself. Default 0.")
	an.Describe(&a.PriorityClassName, "The runner pods' PriorityClass, which must exist.")
	an.Describe(&a.NodeSelector, "Node labels the runner pods require.")
	an.Describe(&a.TopologySpread, "Spread the pool's runners evenly across the nodes they may run on, hard: a runner that would unbalance them stays Pending. Default true.")
	an.Describe(&a.AutomountServiceAccountToken, "Mount a Kubernetes API token in the runner pods, which every job can then read. Default false. The pool creates no role or binding, so grant the ServiceAccount only what the jobs need.")
	an.Describe(&a.ServiceAccountName, "An existing ServiceAccount in the pool's namespace for the runner pods. Default: the namespace's default one. The pool creates none.")
	an.Describe(&a.Env, "Every job's default environment. DOCKER_HOST is the runner's own and is refused.")
}

// tokenPath is the scope's registration token API, or an error when scope, owner and repo do not
// name one.
func (a GiteaRunnerPoolArgs) tokenPath() (string, error) {
	const suffix = "/actions/runners/registration-token"
	wantOwner, wantRepo := a.Scope != scopeInstance, a.Scope == scopeRepo
	switch a.Scope {
	case scopeInstance, scopeOrg, scopeUser, scopeRepo:
	default:
		return "", fmt.Errorf("scope %q must be %s, %s, %s or %s", a.Scope, scopeInstance, scopeOrg, scopeUser, scopeRepo)
	}
	for _, field := range []struct {
		name  string
		value *string
		want  bool
	}{{"owner", a.Owner, wantOwner}, {"repo", a.Repo, wantRepo}} {
		switch {
		case !field.want && field.value != nil:
			return "", fmt.Errorf("scope %s takes no %s", a.Scope, field.name)
		case field.want && !giteaNamePattern.MatchString(orDefault(field.value, "")):
			return "", fmt.Errorf("scope %s needs %s, a name without a slash", a.Scope, field.name)
		}
	}
	switch a.Scope {
	case scopeRepo:
		return "/api/v1/repos/" + *a.Owner + "/" + *a.Repo + suffix, nil
	case scopeOrg:
		return "/api/v1/orgs/" + *a.Owner + suffix, nil
	case scopeUser:
		// The API names no user: it answers for the one asking.
		return "/api/v1/user" + suffix, nil
	default:
		return "/api/v1/admin" + suffix, nil
	}
}

func giteaEngine(a GiteaRunnerPoolArgs) string { return orDefault(a.Engine, engineRootless) }

func giteaPersistentEngine(a GiteaRunnerPoolArgs) bool {
	return a.PersistentEngineStorage == nil || *a.PersistentEngineStorage
}

// giteaHostJobs is whether any label runs its jobs in the runner container.
func giteaHostJobs(a GiteaRunnerPoolArgs) bool {
	for _, label := range a.Labels {
		if !strings.Contains(label, ":docker://") {
			return true
		}
	}
	return false
}

// checkGiteaPool refuses a pool that cannot work, before anything is created.
func checkGiteaPool(a GiteaRunnerPoolArgs) error {
	if !poolIDPattern.MatchString(a.ID) {
		return fmt.Errorf("poolId %q must be a DNS-safe name of at most 40 characters", a.ID)
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("gitea pool %s: "+format, append([]any{a.ID}, args...)...)
	}
	u, err := url.Parse(a.InstanceURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fail("instanceUrl must be an http or https URL without credentials or a query, e.g. https://git.example.com")
	}
	if _, err := a.tokenPath(); err != nil {
		return fail("%w", err)
	}
	if a.RegistrationToken == nil {
		return fail("registrationToken is required")
	}
	if intOr(a.Replicas, 1) < 0 {
		return fail("replicas must not be negative")
	}
	if intOr(a.Capacity, 1) < 1 {
		return fail("capacity must be at least 1")
	}
	if intOr(a.UpdatePartition, 0) < 0 {
		return fail("updatePartition must not be negative")
	}
	if engine := giteaEngine(a); engine != engineRootless && engine != enginePrivileged {
		return fail("engine %q must be %s or %s", engine, engineRootless, enginePrivileged)
	}
	for _, label := range a.Labels {
		if !giteaLabelPattern.MatchString(label) {
			return fail("label %q must be <name>, <name>:host or <name>:docker://<image>", label)
		}
	}
	if giteaHostJobs(a) && orDefault(a.Image, "") == "" {
		return fail("host labels run jobs in the runner container, so give image: one with the tools they need, gitea-runner and tini")
	}
	if _, ok := a.Env["DOCKER_HOST"]; ok {
		return fail("env must not set DOCKER_HOST, which is the engine's socket")
	}
	return nil
}

// registrationTokenEndpoint is where to POST for the pool's registration token.
func registrationTokenEndpoint(a GiteaRunnerPoolArgs) string {
	// checkGiteaPool has accepted the scope.
	path, _ := a.tokenPath()
	return strings.TrimSuffix(a.InstanceURL, "/") + path
}

// giteaStateFile is the registration's file name in /data. It changes with the instance and the
// scope, so a runner moved to either registers again instead of keeping its old registration; a
// restart, a new token or new labels keep it.
func giteaStateFile(a GiteaRunnerPoolArgs) string {
	// The owner too: scope user's endpoint does not name the user.
	sum := sha256.Sum256([]byte(registrationTokenEndpoint(a) + "#" + orDefault(a.Owner, "")))
	return ".runner-" + hex.EncodeToString(sum[:6])
}

// giteaRunnerConfig is the runner's config.yaml. Everything unlisted keeps the runner's default.
func giteaRunnerConfig(a GiteaRunnerPoolArgs) (string, error) {
	labels := a.Labels
	if len(labels) == 0 {
		labels = giteaDefaultLabels
	}
	envs := map[string]string{}
	for k, v := range a.Env {
		envs[k] = v
	}
	if giteaEngine(a) == engineRootless {
		// podman serves the classic build API; BuildKit needs cgroups.
		envs["DOCKER_BUILDKIT"] = "0"
	}
	runner := map[string]any{"file": giteaStateFile(a), "labels": labels, "capacity": intOr(a.Capacity, 1)}
	if len(envs) > 0 {
		runner["envs"] = envs
	}
	config := map[string]any{"runner": runner}
	if options := orDefault(a.ContainerOptions, ""); options != "" {
		config["container"] = map[string]any{"options": options}
	}
	if giteaHostJobs(a) {
		// On the volume the engine shares, so `docker run -v "$PWD":/src` finds the checkout.
		config["host"] = map[string]any{"workdir_parent": giteaWorkDir}
	}
	raw, err := yaml.Marshal(config)
	return string(raw), err
}
