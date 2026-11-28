import * as pulumi from "@pulumi/pulumi";
import * as inputs from "../types/input";
export interface ContainerResourcesArgs {
    /**
     * CPU limit. Default: none.
     */
    cpuLimit?: pulumi.Input<string | undefined>;
    /**
     * CPU request, e.g. 50m.
     */
    cpuRequest?: pulumi.Input<string | undefined>;
    /**
     * Memory limit. Default: none.
     */
    memoryLimit?: pulumi.Input<string | undefined>;
    /**
     * Memory request, e.g. 128Mi.
     */
    memoryRequest?: pulumi.Input<string | undefined>;
}
export interface ControllerMonitoringArgs {
    /**
     * Create a PrometheusRule alerting on listeners that are not running and jobs queued without a runner. Off unless set; {} creates it. The cluster must already have the Prometheus Operator's CRDs.
     */
    alerts?: pulumi.Input<inputs.MonitoringAlertsArgs | undefined>;
    /**
     * Create a Grafana dashboard ConfigMap of the listener metrics. Off unless set; {} creates it.
     */
    dashboard?: pulumi.Input<inputs.MonitoringDashboardArgs | undefined>;
    /**
     * Admit only these scrapers, and the GateAgent, to the controller and listener pods' port 8080. Off unless set; without a policy in the namespace the pods are reachable by everything.
     */
    networkPolicy?: pulumi.Input<inputs.MonitoringNetworkPolicyArgs | undefined>;
    /**
     * Create a PodMonitor for the controller and listener pods. Off unless set; {} creates it. The cluster must already have the Prometheus Operator's CRDs.
     */
    podMonitor?: pulumi.Input<inputs.MonitoringPodMonitorArgs | undefined>;
    /**
     * Put prometheus.io/scrape, port and path annotations on the controller pod, for a scraper that discovers pods by them. The listeners' annotations are each Pool's listenerScrapeAnnotations.
     */
    scrapeAnnotations?: pulumi.Input<boolean | undefined>;
}
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
export interface MetricsSourceArgs {
    /**
     * Labels of the scraper's namespace. Unset, podLabels select only pods in the controller's namespace.
     */
    namespaceLabels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    /**
     * Labels of the scraper's pods. Unset, every pod in the matching namespaces.
     */
    podLabels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
}
export interface MonitoringAlertsArgs {
    /**
     * Labels on the PrometheusRule, for a Prometheus that selects rules by label.
     */
    labels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    /**
     * How long a listener must be down or missing before its alert fires. Default 5m.
     */
    listenerFor?: pulumi.Input<string | undefined>;
    /**
     * The poolIds expected to have a listener. Each gets an alert for having no listener metrics at all, which covers a listener that never started and a controller that is down.
     */
    poolIds?: pulumi.Input<pulumi.Input<string>[] | undefined>;
    /**
     * How long jobs must be assigned to a pool without running before the alert fires. Default 10m.
     */
    queuedFor?: pulumi.Input<string | undefined>;
}
export interface MonitoringDashboardArgs {
    /**
     * Labels on the ConfigMap, added to grafana_dashboard: "1", which a label of the same name replaces.
     */
    labels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
    /**
     * Existing namespace for the ConfigMap, where Grafana's sidecar looks. Defaults to the controller's namespace.
     */
    namespace?: pulumi.Input<string | undefined>;
}
export interface MonitoringNetworkPolicyArgs {
    /**
     * The scrapers allowed to reach port 8080, at least one, e.g. {namespaceLabels: {"kubernetes.io/metadata.name": "monitoring"}, podLabels: {"app.kubernetes.io/name": "prometheus"}}.
     */
    allow?: pulumi.Input<pulumi.Input<inputs.MetricsSourceArgs>[] | undefined>;
    /**
     * The GateAgent's namespace: its agent pods read the listeners' metrics, so they are always admitted. Defaults to arc-systems.
     */
    gateAgentNamespace?: pulumi.Input<string | undefined>;
}
export interface MonitoringPodMonitorArgs {
    /**
     * The scrape interval, e.g. 30s. Defaults to Prometheus's own.
     */
    interval?: pulumi.Input<string | undefined>;
    /**
     * Labels on the PodMonitor, for a Prometheus that selects monitors by label, e.g. {release: "kps"}.
     */
    labels?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    } | undefined>;
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
export interface PoolGateClaimArgs {
    /**
     * The GateAgent's namespace: the URL is http://runner-gate-agent.<namespace>.svc:8080, and it is the default for networkPolicy.gateAgentNamespace. Set exactly one of url or agentNamespace.
     */
    agentNamespace?: pulumi.Input<string | undefined>;
    /**
     * The claim endpoint's base URL, set as RUNNER_GATE_URL. Set exactly one of url or agentNamespace. With a networkPolicy, an in-cluster URL also needs the policy to allow it.
     */
    url?: pulumi.Input<string | undefined>;
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
     * A script run after the fork check and the gateClaim, if set; a non-zero exit fails the job.
     */
    script?: pulumi.Input<string | undefined>;
}
export interface PoolMaxRunnersAgentArgs {
    /**
     * Let the runner-gate agent own the live maxRunners. Default false. maxRunners then becomes the ceiling, published on the AutoscalingRunnerSet as the annotation runners.antimatter.studios/max-runners-ceiling.
     */
    enabled?: pulumi.Input<boolean | undefined>;
    /**
     * The maxRunners a new scale set starts at, before the agent raises it. Default 1, or minRunners if higher. Written only when the scale set is created: an existing pool keeps its live value, and a later change to floor is not applied.
     */
    floor?: pulumi.Input<number | undefined>;
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