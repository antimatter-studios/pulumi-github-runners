import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
/**
 * One organisation's self-hosted runners on one machine: credentials, runner group, scale set, Docker sidecar and job-started hook.
 */
export declare class Pool extends pulumi.ComponentResource {
    /**
     * Returns true if the given object is an instance of Pool.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Pool;
    readonly namespace: pulumi.Output<string>;
    readonly scaleSetName: pulumi.Output<string>;
    /**
     * Create a Pool resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: PoolArgs, opts?: pulumi.ComponentResourceOptions);
}
/**
 * The set of arguments for constructing a Pool resource.
 */
export interface PoolArgs {
    /**
     * Recommended GitHub App credential for ARC and the optional runner group. Set exactly one of app or token. For runner group management, the App needs organisation Self-hosted runners: write.
     */
    app?: pulumi.Input<inputs.GitHubAppArgs | undefined>;
    /**
     * https://github.com/<org>, or a repository URL for repository runners.
     */
    configUrl: string;
    /**
     * The runner container's CPU limit. Defaults to no container CPU limit. A pod-level CPU limit takes precedence.
     */
    cpuLimit?: string;
    cpuRequest?: string;
    /**
     * Device-plugin resources each runner pod gets, in both the runner and its Docker sidecar (so a pod takes the count twice), e.g. {"devic.es/fuse": "1"}.
     */
    devices?: {
        [key: string]: string;
    };
    /**
     * The Docker sidecar's memory limit. Defaults to none. A pod-level memory limit takes precedence.
     */
    dockerMemoryLimit?: string;
    /**
     * The Docker sidecar's memory request. Default 128Mi. Docker builds and job services run here, so with LimitedSwap this request is also what sizes the sidecar's swap.
     */
    dockerMemoryRequest?: string;
    dockerStorageLimit?: string;
    /**
     * Every job's default environment, which a job can override, e.g. {GOMAXPROCS: "2", CARGO_BUILD_JOBS: "2", FLTH_RESOURCE_PROFILE: "low"}: with no CPU limit, jobs otherwise size their parallelism to the whole node.
     */
    env?: pulumi.Input<{
        [key: string]: string;
    } | undefined>;
    ephemeralStorageLimit?: string;
    ephemeralStorageRequest?: string;
    /**
     * Claim each job's runner-gate reservation through the cluster's gate agent, after the fork check and before hook.script. A gate that cannot be reached lets the job run; an expired reservation fails it. Off unless set.
     */
    gateClaim?: inputs.PoolGateClaimArgs;
    /**
     * What runs before every job's first step.
     */
    hook?: inputs.PoolHookArgs;
    /**
     * The runner image, pinned by digest. Default: the antimatter actions-runner image.
     */
    image?: string;
    /**
     * The PriorityClass of the pool's listener pod, which ARC's controller runs to take the pool's jobs, e.g. a high one so runners cannot starve it. Default: none. The class must exist.
     */
    listenerPriorityClassName?: string;
    /**
     * The listener container's requests and limits. Default: none.
     */
    listenerResources?: inputs.ContainerResourcesArgs;
    /**
     * Put prometheus.io/scrape, port and path annotations on the pool's listener pod, for a scraper that discovers pods by them. Default false. Needs the Controller's listener metrics.
     */
    listenerScrapeAnnotations?: boolean;
    /**
     * Jobs at once. Default 2. With maxRunnersAgent enabled, only the value a new scale set starts at: the agent then owns it, and a later change here is not applied.
     */
    maxRunners?: number;
    /**
     * Let the runner-gate agent set the live maxRunners from the cluster's load, with no cap. Off unless enabled.
     */
    maxRunnersAgent?: inputs.PoolMaxRunnersAgentArgs;
    /**
     * The runner container's memory limit. Defaults to none. A pod-level memory limit takes precedence.
     */
    memoryLimit?: string;
    memoryRequest?: string;
    /**
     * Idle runners kept ready. Default 0.
     */
    minRunners?: number;
    /**
     * Keep runner pods off private addresses.
     */
    networkPolicy?: inputs.PoolNetworkPolicyArgs;
    /**
     * Only nodes with these labels run the pool's runners, e.g. {"kubernetes.io/hostname": "s2"} for one pool per node, so whatever picks the pool's label (the runner gate) picks the node.
     */
    nodeSelector?: {
        [key: string]: string;
    };
    /**
     * One budget the runner and its Docker sidecar share. A pod-level CPU or memory limit replaces the corresponding container limits.
     */
    podResources?: inputs.PodResourcesArgs;
    podmanImage?: string;
    /**
     * A DNS-safe key for the pool; its runners live in namespace arc-runners-<poolId>.
     */
    poolId: string;
    /**
     * The runner pods' PriorityClass, e.g. a low one so the cluster's own services win under pressure and runners are evicted first. The class must exist.
     */
    priorityClassName?: string;
    /**
     * Pull docker.io images through the Controller's registryMirror, falling back to Docker Hub when it cannot answer. With a networkPolicy, runners may reach the mirror's pods on its port. Off unless set.
     */
    registryMirror?: inputs.PoolRegistryMirrorArgs;
    /**
     * The organisation's runner group the runners join, created with the pool's GitHub App or PAT. Requires an organisation configUrl.
     */
    runnerGroup?: inputs.RunnerGroupArgs;
    /**
     * The runs-on label: <machine>-<os>-<arch>, e.g. ryzen-linux-x64.
     */
    scaleSetName: string;
    /**
     * Take one of the node's runner slots per runner pod, so runners are placed only where the node's load leaves room. Off unless enabled.
     */
    slots?: inputs.PoolSlotsArgs;
    /**
     * Groups the runner pod's processes join.
     */
    supplementalGroups?: number[];
    /**
     * Alternative fine-grained PAT for ARC and the optional runner group. Set exactly one of app or token. For ARC, grant Self-hosted runners read and write, and repository Administration read.
     */
    token?: pulumi.Input<string | undefined>;
    /**
     * Spread the pool's runners evenly across nodes, hard. For multi-node clusters.
     */
    topologySpread?: boolean;
    workVolumeSize?: string;
}
//# sourceMappingURL=pool.d.ts.map