import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
/**
 * Reports one cluster's runner pools to the public runner-gate and serves its in-cluster claim endpoint.
 */
export declare class GateAgent extends pulumi.ComponentResource {
    /**
     * Returns true if the given object is an instance of GateAgent.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is GateAgent;
    /**
     * The in-cluster base URL for the runner's claim hook (RUNNER_GATE_URL).
     */
    readonly claimUrl: pulumi.Output<string>;
    /**
     * Create a GateAgent resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: GateAgentArgs, opts?: pulumi.ComponentResourceOptions);
}
/**
 * The set of arguments for constructing a GateAgent resource.
 */
export interface GateAgentArgs {
    /**
     * The cluster name registered with the public runner-gate, e.g. homelab.
     */
    cluster: pulumi.Input<string>;
    /**
     * The public runner-gate URL. Defaults to https://runner-gate.antimatter-studios.com.
     */
    gateUrl?: pulumi.Input<string | undefined>;
    /**
     * The agent image, pinned to a 16-hex tag: git.antimatter-studios.com/antimatter-studios/antimatter-vps-gate-webservice:<tag>.
     */
    image: pulumi.Input<string>;
    /**
     * Registry credentials for the agent image's pull Secret.
     */
    imagePull: pulumi.Input<inputs.GateAgentImagePullArgs>;
    /**
     * How often to push a pool snapshot, e.g. 10s. Defaults to the agent's interval.
     */
    interval?: string;
    /**
     * Existing namespace of the ARC listener pods. Defaults to arc-systems.
     */
    listenerNamespace?: pulumi.Input<string | undefined>;
    /**
     * Let the agent read and patch the AutoscalingRunnerSets in runnerNamespaces, so it can set each pool's live maxRunners under its ceiling. Default false; granted in runnerNamespaces only.
     */
    manageMaxRunners?: boolean;
    /**
     * Let the probe publish each node's runner slots (the extended resource runners.antimatter.studios/slot) for pools with slots enabled: PROBE_SLOTS=apply and patch on nodes/status for the probe. Needs probe.enabled. Default false (dry run); nothing broader than nodes/status patch is granted.
     */
    manageSlots?: boolean;
    /**
     * The agent's live maxRunners: off, dry-run (log the changes) or apply (needs manageMaxRunners). Default apply with manageMaxRunners, else off.
     */
    maxRunnersMode?: string;
    /**
     * The agent's AGENT_MAX_RUNNERS_FLOOR, _STEP and _INTERVAL. Defaults 1, 1 and 1m.
     */
    maxRunnersSettings?: {
        [key: string]: string;
    };
    /**
     * Existing namespace in which to deploy the agent. Defaults to arc-systems.
     */
    namespace?: pulumi.Input<string | undefined>;
    /**
     * Who may reach the agent's port 8080. By default only pods in namespaces labelled github-runners.antimatter.studios/pool, which every Pool's namespace is.
     */
    networkPolicy?: inputs.GateAgentNetworkPolicyArgs;
    /**
     * The per-node probe (runner-gate probe): AIMD slots and youngest-runner eviction. Off unless enabled.
     */
    probe?: inputs.GateAgentProbeArgs;
    /**
     * Existing runner namespaces in which the agent can list pods, pod metrics and ephemeral runners.
     */
    runnerNamespaces: pulumi.Input<string[]>;
    /**
     * The public runner-gate's agent token for this cluster.
     */
    token: pulumi.Input<string>;
}
//# sourceMappingURL=gateAgent.d.ts.map