package githubrunners

import (
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// PoolArgs are a Pool's inputs: one organisation's runners on one machine.
type PoolArgs struct {
	// Not `id`: Pulumi reserves that property name on every resource and strips it from a
	// component's inputs, so a pool declared with `id` arrived with an empty one.
	ID           string `pulumi:"poolId"`
	ConfigURL    string `pulumi:"configUrl"`
	ScaleSetName string `pulumi:"scaleSetName"`

	App   *GitHubApp         `pulumi:"app,optional"`
	Token pulumi.StringInput `pulumi:"token,optional" provider:"secret"`

	RunnerGroup *RunnerGroup `pulumi:"runnerGroup,optional"`

	MinRunners *int    `pulumi:"minRunners,optional"`
	MaxRunners *int    `pulumi:"maxRunners,optional"`
	Image      *string `pulumi:"image,optional"`

	PodmanImage *string `pulumi:"podmanImage,optional"`

	CPURequest              *string       `pulumi:"cpuRequest,optional"`
	CPULimit                *string       `pulumi:"cpuLimit,optional"`
	MemoryRequest           *string       `pulumi:"memoryRequest,optional"`
	MemoryLimit             *string       `pulumi:"memoryLimit,optional"`
	EphemeralStorageRequest *string       `pulumi:"ephemeralStorageRequest,optional"`
	EphemeralStorageLimit   *string       `pulumi:"ephemeralStorageLimit,optional"`
	WorkVolumeSize          *string       `pulumi:"workVolumeSize,optional"`
	DockerMemoryLimit       *string       `pulumi:"dockerMemoryLimit,optional"`
	DockerStorageLimit      *string       `pulumi:"dockerStorageLimit,optional"`
	PodResources            *PodResources `pulumi:"podResources,optional"`

	TopologySpread     *bool             `pulumi:"topologySpread,optional"`
	PriorityClassName  *string           `pulumi:"priorityClassName,optional"`
	Devices            map[string]string `pulumi:"devices,optional"`
	SupplementalGroups []int             `pulumi:"supplementalGroups,optional"`

	// Env is every job's default environment, which a job can override. Its use is telling jobs how
	// much of the machine to assume: with no CPU limit, nproc and every tool's default parallelism
	// see the whole node, so two jobs each sized to all of it overload the node.
	Env           map[string]string  `pulumi:"env,optional"`
	Hook          *PoolHook          `pulumi:"hook,optional"`
	NetworkPolicy *PoolNetworkPolicy `pulumi:"networkPolicy,optional"`
}

// GitHubApp is the recommended credential for a pool.
type GitHubApp struct {
	AppID          string             `pulumi:"appId"`
	InstallationID string             `pulumi:"installationId"`
	PrivateKey     pulumi.StringInput `pulumi:"privateKey" provider:"secret"`
}

// RunnerGroup is the organisation's runner group the pool's runners join, created with the pool's credential.
type RunnerGroup struct {
	Name                     string  `pulumi:"name"`
	Visibility               *string `pulumi:"visibility,optional"`
	AllowsPublicRepositories *bool   `pulumi:"allowsPublicRepositories,optional"`
}

// PodResources is one budget for the whole runner pod (pod-level resources, Kubernetes 1.34+).
type PodResources struct {
	CPULimit      *string `pulumi:"cpuLimit,optional"`
	MemoryLimit   *string `pulumi:"memoryLimit,optional"`
	CPURequest    *string `pulumi:"cpuRequest,optional"`
	MemoryRequest *string `pulumi:"memoryRequest,optional"`
}

// PoolHook is what runs before every job's first step.
type PoolHook struct {
	RejectForks *bool             `pulumi:"rejectForks,optional"`
	Script      *string           `pulumi:"script,optional"`
	Files       map[string]string `pulumi:"files,optional"`
	Env         map[string]string `pulumi:"env,optional"`
}

// PoolNetworkPolicy keeps runner pods off private addresses, except where allowed.
type PoolNetworkPolicy struct {
	BlockPrivate *bool          `pulumi:"blockPrivate,optional"`
	Allow        []NetworkAllow `pulumi:"allow,optional"`
}

// NetworkAllow is one exception: pods matching the selectors, on these TCP ports.
type NetworkAllow struct {
	NamespaceLabels map[string]string `pulumi:"namespaceLabels,optional"`
	PodLabels       map[string]string `pulumi:"podLabels,optional"`
	Ports           []int             `pulumi:"ports,optional"`
}

func (a *PoolArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.ID, "A DNS-safe key for the pool; its runners live in namespace arc-runners-<poolId>.")
	an.Describe(&a.ConfigURL, "https://github.com/<org>, or a repository URL for repository runners.")
	an.Describe(&a.ScaleSetName, "The runs-on label: <machine>-<os>-<arch>, e.g. ryzen-linux-x64.")
	an.Describe(&a.App, "Recommended GitHub App credential for ARC and the optional runner group. Set exactly one of app or token. For runner group management, the App needs organisation Self-hosted runners: write.")
	an.Describe(&a.Token, "Alternative fine-grained PAT for ARC and the optional runner group. Set exactly one of app or token. For ARC, grant Self-hosted runners read and write, and repository Administration read.")
	an.Describe(&a.RunnerGroup, "The organisation's runner group the runners join, created with the pool's GitHub App or PAT. Requires an organisation configUrl.")
	an.Describe(&a.MinRunners, "Idle runners kept ready. Default 0.")
	an.Describe(&a.MaxRunners, "Jobs at once. Default 2.")
	an.Describe(&a.Image, "The runner image, pinned by digest. Default: the antimatter actions-runner image.")
	an.Describe(&a.CPULimit, "The runner container's CPU limit. Defaults to no container CPU limit. A pod-level CPU limit takes precedence.")
	an.Describe(&a.MemoryLimit, "The runner container's memory limit. Defaults to none. A pod-level memory limit takes precedence.")
	an.Describe(&a.DockerMemoryLimit, "The Docker sidecar's memory limit. Defaults to none. A pod-level memory limit takes precedence.")
	an.Describe(&a.PodResources, "One budget the runner and its Docker sidecar share. A pod-level CPU or memory limit replaces the corresponding container limits.")
	an.Describe(&a.PriorityClassName, "The runner pods' PriorityClass, e.g. a low one so the cluster's own services win under pressure and runners are evicted first. The class must exist.")
	an.Describe(&a.TopologySpread, "Spread the pool's runners evenly across nodes, hard. For multi-node clusters.")
	an.Describe(&a.Devices, "Device-plugin resources each runner pod gets, in both the runner and its Docker sidecar (so a pod takes the count twice), e.g. {\"devic.es/fuse\": \"1\"}.")
	an.Describe(&a.SupplementalGroups, "Groups the runner pod's processes join.")
	an.Describe(&a.Env, "Every job's default environment, which a job can override, e.g. {GOMAXPROCS: \"2\", CARGO_BUILD_JOBS: \"2\", FLTH_RESOURCE_PROFILE: \"low\"}: with no CPU limit, jobs otherwise size their parallelism to the whole node.")
	an.Describe(&a.Hook, "What runs before every job's first step.")
	an.Describe(&a.NetworkPolicy, "Keep runner pods off private addresses.")
}

func (h *PoolHook) Annotate(an infer.Annotator) {
	an.Describe(&h.RejectForks, "Fail every job started by a pull request from a fork. Default true.")
	an.Describe(&h.Script, "A script run after the fork check; a non-zero exit fails the job.")
	an.Describe(&h.Files, "Extra files beside the script in /etc/arc-hooks.")
	an.Describe(&h.Env, "Environment for the runner container, which the hook sees.")
}
