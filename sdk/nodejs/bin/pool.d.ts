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
    devices?: pulumi.Input<{
        [key: string]: string;
    } | undefined>;
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
     * What runs before every job's first step.
     */
    hook?: pulumi.Input<inputs.PoolHookArgs | undefined>;
    /**
     * The runner image, pinned by digest. Default: the antimatter actions-runner image.
     */
    image?: string;
    /**
     * Jobs at once. Default 2.
     */
    maxRunners?: number;
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
    networkPolicy?: pulumi.Input<inputs.PoolNetworkPolicyArgs | undefined>;
    /**
     * One budget the runner and its Docker sidecar share. A pod-level CPU or memory limit replaces the corresponding container limits.
     */
    podResources?: pulumi.Input<inputs.PodResourcesArgs | undefined>;
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
     * The organisation's runner group the runners join, created with the pool's GitHub App or PAT. Requires an organisation configUrl.
     */
    runnerGroup?: pulumi.Input<inputs.RunnerGroupArgs | undefined>;
    /**
     * The runs-on label: <machine>-<os>-<arch>, e.g. ryzen-linux-x64.
     */
    scaleSetName: string;
    /**
     * Take one of the node's runner slots per runner pod, so runners are placed only where the node's load leaves room. Off unless enabled.
     */
    slots?: pulumi.Input<inputs.PoolSlotsArgs | undefined>;
    /**
     * Groups the runner pod's processes join.
     */
    supplementalGroups?: pulumi.Input<number[] | undefined>;
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