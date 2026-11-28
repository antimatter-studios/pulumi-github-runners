package githubrunners

import (
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// GateAgentArgs configure one cluster's runner-gate agent.
type GateAgentArgs struct {
	Cluster           pulumi.StringInput      `pulumi:"cluster"`
	Token             pulumi.StringInput      `pulumi:"token" provider:"secret"`
	Image             pulumi.StringInput      `pulumi:"image"`
	GateURL           pulumi.StringPtrInput   `pulumi:"gateUrl,optional"`
	Namespace         pulumi.StringPtrInput   `pulumi:"namespace,optional"`
	ListenerNamespace pulumi.StringPtrInput   `pulumi:"listenerNamespace,optional"`
	RunnerNamespaces  pulumi.StringArrayInput `pulumi:"runnerNamespaces"`
	Interval          *string                 `pulumi:"interval,optional"`
	ImagePull         GateAgentImagePullInput `pulumi:"imagePull"`

	NetworkPolicy *GateAgentNetworkPolicy `pulumi:"networkPolicy,optional"`

	// ManageSlots lets the agent publish each node's runner slots (see PoolSlots). Off by default:
	// the agent starts in dry-run, and the write permission is granted only when it is asked for.
	ManageSlots *bool `pulumi:"manageSlots,optional"`
	// ManageMaxRunners lets the agent set each pool's live maxRunners (see PoolMaxRunnersAgent). Off
	// by default, for the same reason.
	ManageMaxRunners *bool `pulumi:"manageMaxRunners,optional"`
	// MaxRunnersMode is the agent's live maxRunners: off, dry-run or apply (AGENT_MAX_RUNNERS).
	MaxRunnersMode *string `pulumi:"maxRunnersMode,optional"`
	// MaxRunnersSettings are the agent's other AGENT_MAX_RUNNERS_* settings.
	MaxRunnersSettings map[string]string `pulumi:"maxRunnersSettings,optional"`
	// Probe is the per-node runner-gate probe (#23, #31).
	Probe *GateAgentProbe `pulumi:"probe,optional"`
}

// GateAgentProbe runs runner-gate probe on every node: AIMD runner slots from pressure, and eviction
// of the youngest runner pod when memory runs out.
type GateAgentProbe struct {
	Enabled  *bool             `pulumi:"enabled,optional"`
	Evict    *string           `pulumi:"evict,optional"`
	Settings map[string]string `pulumi:"settings,optional"`
}

func (p *GateAgentProbe) Annotate(an infer.Annotator) {
	an.Describe(&p.Enabled, "Run the probe, a DaemonSet on every node. Default false. It logs its slot decisions; it publishes them only with manageSlots.")
	an.Describe(&p.Evict, "Pressure-triggered eviction of the youngest runner pod: off, dry-run (log only) or on. Default dry-run. on lets the probe delete pods in runnerNamespaces, and only there.")
	an.Describe(&p.Settings, "How the probe reacts, PROBE_* env: pressure thresholds, rates and interval, e.g. {PROBE_PSI_MEMORY_SOME_HIGH: \"15\", PROBE_INCREASE_STEP: \"1\"}. Defaults and meanings: antimatter-vps docs/runner-gate.md. No slot counts: PROBE_FLOOR and PROBE_CEILING are refused, the bounds come from the node. PROBE_SLOTS and PROBE_EVICT come from manageSlots and evict.")
}

// GateAgentNetworkPolicy guards the agent's port: runner namespaces, plus the sources listed here.
type GateAgentNetworkPolicy struct {
	Enabled *bool             `pulumi:"enabled,optional"`
	Allow   []GateAgentSource `pulumi:"allow,optional"`
}

// GateAgentSource is one extra source: pods matching the selectors.
type GateAgentSource struct {
	NamespaceLabels map[string]string `pulumi:"namespaceLabels,optional"`
	PodLabels       map[string]string `pulumi:"podLabels,optional"`
}

func (a *GateAgentArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Cluster, "The cluster name registered with the public runner-gate, e.g. homelab.")
	an.Describe(&a.Token, "The public runner-gate's agent token for this cluster.")
	an.Describe(&a.Image, "The agent image, pinned to a 16-hex tag: "+gateAgentImageRepository+":<tag>.")
	an.Describe(&a.GateURL, "The public runner-gate URL. Defaults to "+defaultGateAgentURL+".")
	an.Describe(&a.Namespace, "Existing namespace in which to deploy the agent. Defaults to "+defaultGateAgentNamespace+".")
	an.Describe(&a.ListenerNamespace, "Existing namespace of the ARC listener pods. Defaults to "+defaultGateAgentNamespace+".")
	an.Describe(&a.RunnerNamespaces, "Existing runner namespaces in which the agent can list pods, pod metrics and ephemeral runners.")
	an.Describe(&a.Interval, "How often to push a pool snapshot, e.g. 10s. Defaults to the agent's interval.")
	an.Describe(&a.ImagePull, "Registry credentials for the agent image's pull Secret.")
	an.Describe(&a.ManageSlots, "Let the probe publish each node's runner slots (the extended resource "+defaultSlotResource+") for pools with slots enabled: PROBE_SLOTS=apply and patch on nodes/status for the probe. Needs probe.enabled. Default false (dry run); nothing broader than nodes/status patch is granted.")
	an.Describe(&a.MaxRunnersMode, "The agent's live maxRunners: off, dry-run (log the changes) or apply (needs manageMaxRunners). Default apply with manageMaxRunners, else off.")
	an.Describe(&a.MaxRunnersSettings, "How the agent's live maxRunners reacts: AGENT_MAX_RUNNERS_STEP (the most one change raises a pool, default 1) and AGENT_MAX_RUNNERS_INTERVAL (the least time between two changes of a pool, default 1m). No counts or caps.")
	an.Describe(&a.Probe, "The per-node probe (runner-gate probe): AIMD slots and youngest-runner eviction. Off unless enabled.")
	an.Describe(&a.ManageMaxRunners, "Let the agent read and patch the AutoscalingRunnerSets in runnerNamespaces, so it can set each pool's live maxRunners (spec.maxRunners only, by its own code). Default false; granted in runnerNamespaces only.")
	an.Describe(&a.NetworkPolicy, "Who may reach the agent's port 8080. By default only pods in namespaces labelled "+poolNamespaceLabel+", which every Pool's namespace is.")
}

func (p *GateAgentNetworkPolicy) Annotate(an infer.Annotator) {
	an.Describe(&p.Enabled, "Create the agent's ingress NetworkPolicy. Default true; false lets any pod reach the agent.")
	an.Describe(&p.Allow, "Extra sources allowed to reach port 8080, e.g. a metrics scraper: {namespaceLabels: {\"kubernetes.io/metadata.name\": \"monitoring\"}}. Runner namespaces not created by a Pool need an entry too.")
}

func (s *GateAgentSource) Annotate(an infer.Annotator) {
	an.Describe(&s.NamespaceLabels, "Labels of the source's namespace. Unset, podLabels select only pods in the agent's own namespace.")
	an.Describe(&s.PodLabels, "Labels of the source pods. Unset, every pod in the matching namespaces.")
}

func (a *GateAgentImagePull) Annotate(an infer.Annotator) {
	an.Describe(&a.Registry, "Registry host for the image pull credentials. Defaults to "+defaultGateAgentRegistry+".")
}

// PoolArgs are a Pool's inputs: one organisation's runners on one machine.
type PoolArgs struct {
	// Not `id`: Pulumi reserves that property name on every resource and strips it from a
	// component's inputs, so a pool declared with `id` arrived with an empty one.
	ID           string `pulumi:"poolId"`
	ConfigURL    string `pulumi:"configUrl"`
	ScaleSetName string `pulumi:"scaleSetName"`

	// A pointer to the interface: Pulumi's schema wants an optional object behind a pointer, and the
	// interface is what takes the whole object as one Output.
	App   *GitHubAppInput    `pulumi:"app,optional"`
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
	DockerMemoryRequest     *string       `pulumi:"dockerMemoryRequest,optional"`
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
	Env           pulumi.StringMapInput `pulumi:"env,optional"`
	Hook          *PoolHook             `pulumi:"hook,optional"`
	NetworkPolicy *PoolNetworkPolicy    `pulumi:"networkPolicy,optional"`
	Slots         *PoolSlots            `pulumi:"slots,optional"`

	ListenerPriorityClassName *string              `pulumi:"listenerPriorityClassName,optional"`
	ListenerResources         *ContainerResources  `pulumi:"listenerResources,optional"`
	GateClaim                 *PoolGateClaim       `pulumi:"gateClaim,optional"`
	ListenerScrapeAnnotations *bool                `pulumi:"listenerScrapeAnnotations,optional"`
	MaxRunnersAgent           *PoolMaxRunnersAgent `pulumi:"maxRunnersAgent,optional"`
}

// PoolGateClaim has every job claim its runner-gate reservation before its first step. Exactly one
// field is set.
type PoolGateClaim struct {
	URL            pulumi.StringPtrInput `pulumi:"url,optional"`
	AgentNamespace pulumi.StringPtrInput `pulumi:"agentNamespace,optional"`
}

// PoolMaxRunnersAgent hands the pool's live maxRunners to the runner-gate agent (#25), which grows
// and shrinks it with the machines' load. maxRunners becomes only where a new scale set starts: Pulumi
// writes the live field once, when it creates the scale set, and never again, so a deploy does not
// undo the agent. Nothing here caps the agent.
type PoolMaxRunnersAgent struct {
	Enabled *bool `pulumi:"enabled,optional"`
}

// PoolSlots makes each runner pod take one of its node's runner slots, an extended resource the
// GateAgent publishes per node from the node's measured load (#24). The scheduler then places a
// runner only where there is room, and a pod with no slot stays Pending while its job waits at
// GitHub, where waiting costs nothing.
type PoolSlots struct {
	Enabled      *bool   `pulumi:"enabled,optional"`
	ResourceName *string `pulumi:"resourceName,optional"`
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
	RejectForks *bool                 `pulumi:"rejectForks,optional"`
	Script      *string               `pulumi:"script,optional"`
	Files       map[string]string     `pulumi:"files,optional"`
	Env         pulumi.StringMapInput `pulumi:"env,optional"`
}

// PoolNetworkPolicy keeps runner pods off private addresses, except where allowed.
type PoolNetworkPolicy struct {
	BlockPrivate       *bool                 `pulumi:"blockPrivate,optional"`
	Allow              []NetworkAllow        `pulumi:"allow,optional"`
	GateAgentNamespace pulumi.StringPtrInput `pulumi:"gateAgentNamespace,optional"`
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
	an.Describe(&a.MaxRunners, "Jobs at once. Default 2. With maxRunnersAgent enabled, only the value a new scale set starts at: the agent then owns it, and a later change here is not applied.")
	an.Describe(&a.Image, "The runner image, pinned by digest. Default: the antimatter actions-runner image.")
	an.Describe(&a.CPULimit, "The runner container's CPU limit. Defaults to no container CPU limit. A pod-level CPU limit takes precedence.")
	an.Describe(&a.MemoryLimit, "The runner container's memory limit. Defaults to none. A pod-level memory limit takes precedence.")
	an.Describe(&a.DockerMemoryRequest, "The Docker sidecar's memory request. Default 128Mi. Docker builds and job services run here, so with LimitedSwap this request is also what sizes the sidecar's swap.")
	an.Describe(&a.DockerMemoryLimit, "The Docker sidecar's memory limit. Defaults to none. A pod-level memory limit takes precedence.")
	an.Describe(&a.PodResources, "One budget the runner and its Docker sidecar share. A pod-level CPU or memory limit replaces the corresponding container limits.")
	an.Describe(&a.PriorityClassName, "The runner pods' PriorityClass, e.g. a low one so the cluster's own services win under pressure and runners are evicted first. The class must exist.")
	an.Describe(&a.TopologySpread, "Spread the pool's runners evenly across nodes, hard. For multi-node clusters.")
	an.Describe(&a.Devices, "Device-plugin resources each runner pod gets, in both the runner and its Docker sidecar (so a pod takes the count twice), e.g. {\"devic.es/fuse\": \"1\"}.")
	an.Describe(&a.SupplementalGroups, "Groups the runner pod's processes join.")
	an.Describe(&a.Env, "Every job's default environment, which a job can override, e.g. {GOMAXPROCS: \"2\", CARGO_BUILD_JOBS: \"2\", FLTH_RESOURCE_PROFILE: \"low\"}: with no CPU limit, jobs otherwise size their parallelism to the whole node.")
	an.Describe(&a.Hook, "What runs before every job's first step.")
	an.Describe(&a.NetworkPolicy, "Keep runner pods off private addresses.")
	an.Describe(&a.ListenerScrapeAnnotations, "Put prometheus.io/scrape, port and path annotations on the pool's listener pod, for a scraper that discovers pods by them. Default false. Needs the Controller's listener metrics.")
	an.Describe(&a.MaxRunnersAgent, "Let the runner-gate agent set the live maxRunners from the cluster's load, with no cap. Off unless enabled.")
	an.Describe(&a.Slots, "Take one of the node's runner slots per runner pod, so runners are placed only where the node's load leaves room. Off unless enabled.")
	an.Describe(&a.ListenerPriorityClassName, "The PriorityClass of the pool's listener pod, which ARC's controller runs to take the pool's jobs, e.g. a high one so runners cannot starve it. Default: none. The class must exist.")
	an.Describe(&a.ListenerResources, "The listener container's requests and limits. Default: none.")
	an.Describe(&a.GateClaim, "Claim each job's runner-gate reservation through the cluster's gate agent, after the fork check and before hook.script. A gate that cannot be reached lets the job run; an expired reservation fails it. Off unless set.")
}

func (c *PoolGateClaim) Annotate(an infer.Annotator) {
	an.Describe(&c.URL, "The claim endpoint's base URL, set as RUNNER_GATE_URL. Set exactly one of url or agentNamespace. With a networkPolicy, an in-cluster URL also needs the policy to allow it.")
	an.Describe(&c.AgentNamespace, "The GateAgent's namespace: the URL is http://runner-gate-agent.<namespace>.svc:8080, and it is the default for networkPolicy.gateAgentNamespace. Set exactly one of url or agentNamespace.")
}

func (s *PoolSlots) Annotate(an infer.Annotator) {
	an.Describe(&s.Enabled, "Request one runner slot per runner pod. Default false. Only nodes where the GateAgent (manageSlots) has published slots can run the pool's pods; until then they stay Pending and their jobs wait at GitHub.")
	an.Describe(&s.ResourceName, "The extended resource that counts a node's runner slots. Default "+defaultSlotResource+".")
}

func (m *PoolMaxRunnersAgent) Annotate(an infer.Annotator) {
	an.Describe(&m.Enabled, "Let the runner-gate agent own the live maxRunners. Default false. The AutoscalingRunnerSet is marked with the annotation "+maxRunnersAgentAnnotation+", maxRunners is written only when the scale set is created, and Pulumi ignores the live field after that.")
}

func (p *PoolNetworkPolicy) Annotate(an infer.Annotator) {
	an.Describe(&p.GateAgentNamespace, "The GateAgent's namespace: runners may always reach its agent pods on port 8080. Defaults to "+defaultGateAgentNamespace+".")
}

func (h *PoolHook) Annotate(an infer.Annotator) {
	an.Describe(&h.RejectForks, "Fail every job started by a pull request from a fork. Default true.")
	an.Describe(&h.Script, "A script run after the fork check and the gateClaim, if set; a non-zero exit fails the job.")
	an.Describe(&h.Files, "Extra files beside the script in /etc/arc-hooks.")
	an.Describe(&h.Env, "Environment for the runner container, which the hook sees.")
}
