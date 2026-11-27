import * as pulumi from "@pulumi/pulumi";
import * as inputs from "../types/input";
export interface DeviceOfferArgs {
    count?: pulumi.Input<number | undefined>;
    name: pulumi.Input<string>;
    paths: pulumi.Input<pulumi.Input<string>[]>;
}
export interface GateAgentImagePullArgs {
    password: pulumi.Input<string>;
    /**
     * Registry host for the image pull credentials. Defaults to git.antimatter-studios.com.
     */
    registry?: pulumi.Input<string | undefined>;
    username: pulumi.Input<string>;
}
export interface GateAgentNetworkPolicyArgs {
    /**
     * Extra sources allowed to reach port 8080, e.g. a metrics scraper: {namespaceLabels: {"kubernetes.io/metadata.name": "monitoring"}}. Runner namespaces not created by a Pool need an entry too.
     */
    allow?: pulumi.Input<pulumi.Input<inputs.GateAgentSourceArgs>[] | undefined>;
    /**
     * Create the agent's ingress NetworkPolicy. Default true; false lets any pod reach the agent.
     */
    enabled?: pulumi.Input<boolean | undefined>;
}
export interface GateAgentSourceArgs {
    /**
     * Labels of the source's namespace. Unset, podLabels select only pods in the agent's own namespace.
     */
    namespaceLabels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    /**
     * Labels of the source pods. Unset, every pod in the matching namespaces.
     */
    podLabels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
}
export interface GitHubAppArgs {
    appId: pulumi.Input<string>;
    installationId: pulumi.Input<string>;
    privateKey: pulumi.Input<string>;
}
export interface NetworkAllowArgs {
    namespaceLabels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    podLabels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    ports?: pulumi.Input<pulumi.Input<number>[] | undefined>;
}
export interface PodResourcesArgs {
    cpuLimit?: pulumi.Input<string | undefined>;
    cpuRequest?: pulumi.Input<string | undefined>;
    memoryLimit?: pulumi.Input<string | undefined>;
    memoryRequest?: pulumi.Input<string | undefined>;
}
export interface PoolHookArgs {
    /**
     * Environment for the runner container, which the hook sees.
     */
    env?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    /**
     * Extra files beside the script in /etc/arc-hooks.
     */
    files?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    /**
     * Fail every job started by a pull request from a fork. Default true.
     */
    rejectForks?: pulumi.Input<boolean | undefined>;
    /**
     * A script run after the fork check; a non-zero exit fails the job.
     */
    script?: pulumi.Input<string | undefined>;
}
export interface PoolNetworkPolicyArgs {
    allow?: pulumi.Input<pulumi.Input<inputs.NetworkAllowArgs>[] | undefined>;
    blockPrivate?: pulumi.Input<boolean | undefined>;
    /**
     * The GateAgent's namespace: runners may always reach its agent pods on port 8080. Defaults to arc-systems.
     */
    gateAgentNamespace?: pulumi.Input<string | undefined>;
}
export interface PoolSlotsArgs {
    /**
     * Request one runner slot per runner pod. Default false. Only nodes where the GateAgent (manageSlots) has published slots can run the pool's pods; until then they stay Pending and their jobs wait at GitHub.
     */
    enabled?: pulumi.Input<boolean | undefined>;
    /**
     * The extended resource that counts a node's runner slots. Default runners.antimatter.studios/slot.
     */
    resourceName?: pulumi.Input<string | undefined>;
}
export interface PrePullArgs {
    /**
     * Images kept on every node, each of which must have `true` on its PATH. Default: the default runner and podman images. List a pool's own image here if it sets one.
     */
    images?: pulumi.Input<pulumi.Input<string>[] | undefined>;
    /**
     * The pre-pull pods' PriorityClass, e.g. a low one so they are evicted first. Default: none. The class must exist.
     */
    priorityClassName?: pulumi.Input<string | undefined>;
}
export interface RunnerGroupArgs {
    allowsPublicRepositories?: pulumi.Input<boolean | undefined>;
    name: pulumi.Input<string>;
    visibility?: pulumi.Input<string | undefined>;
}
//# sourceMappingURL=input.d.ts.map