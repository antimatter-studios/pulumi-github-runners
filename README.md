# pulumi-github-runners

A Pulumi provider plugin, written in Go, for self-hosted GitHub Actions runners on Kubernetes. Each
cluster that runs runners gets them the same way: Actions Runner Controller, one runner pool per
organisation and machine, Docker for jobs without privileged containers, and a job-started hook
chosen per pool.

The design, its rules and the order of work are in the tracking issue,
[#1](https://github.com/antimatter-studios/pulumi-github-runners/issues/1).

## Resources

| Type | What it is |
|---|---|
| `Controller` (component) | ARC's controller for one cluster, as a Helm release, with listener metrics and optional device plugins offering host devices as `devic.es/<name>`. |
| `Controller.createNamespace` | Default `true`. `false` installs into an existing `namespace` (it must exist) and creates no Namespace resource. Setting it on a Controller that created its namespace deletes that namespace and everything in it. |
| `Controller.monitoring` | Optional PodMonitor, alert rules, Grafana dashboard, scrape annotations and metrics NetworkPolicy; see [Monitoring and alerts](#monitoring-and-alerts-13). |
| `Controller.prePull` | Optional DaemonSet that keeps runner images on every node, so a pod scaled from zero does not wait for its pulls. `{}` pre-pulls the default runner and podman images; list a pool's own `image` in `images` if it sets one. Each image runs `true` once as an unprivileged init container, then the pod idles on `pause` with tiny requests and a 32Mi memory limit. `priorityClassName` is optional. |
| `Controller.priorityClassName`, `Controller.resources` | The controller manager pod's PriorityClass and its requests and limits. See [Priority and resources](#priority-and-resources-of-arcs-own-pods). |
| `Pool` (component) | One organisation's runners on one machine, named `<machine>-<os>-<arch>`: credentials, runner group, scale set, the rootless podman sidecar, the job-started hook and an optional NetworkPolicy. |
| `Pool.gateClaim` | `{agentNamespace: "arc-systems"}` or `{url: "..."}`, exactly one. Every job claims its runner-gate reservation before its first step: fork check, then claim, then `hook.script`. The plugin ships the claim script and sets `RUNNER_GATE_URL` (`http://runner-gate-agent.<agentNamespace>.svc:8080`). A gate that cannot be reached lets the job run; an expired reservation fails it. Off unless set. `agentNamespace` is also the default for `networkPolicy.gateAgentNamespace`; a pool using `url` with a `networkPolicy` must make sure the policy lets its runners reach that address. |
| `ForkPrApproval` | An organisation's "Approval for running fork pull request workflows from contributors" setting. Deleting the resource leaves the setting as it is, so removing it never loosens an organisation. |

## Using it

Not on npm. Build the plugin from a checkout, install it as the development version, and link the
SDK:

```sh
cd provider && go build -o bin/pulumi-resource-github-runners ./cmd/pulumi-resource-github-runners
pulumi plugin install resource github-runners 0.0.0-dev -f bin/pulumi-resource-github-runners
```

```json
"@antimatter-studios/github-runners": "link:../pulumi-github-runners/sdk/nodejs"
```

and run `pnpm install --frozen-lockfile` in this checkout too, so the SDK's own import of
`@pulumi/pulumi` resolves (the root `package.json` exists only for that).

Releases attach the plugin for darwin-arm64, linux-amd64 and linux-arm64 to a GitHub release, which
Pulumi downloads by itself when a program pins that version.

### Network policies

`GateAgent` forwards every claim with its own token, so by default a NetworkPolicy admits only
namespaces labelled `github-runners.antimatter.studios/pool` to its port 8080. `Pool` puts that label
on its namespace. Other sources, such as a metrics scraper or a runner namespace no `Pool` created,
go in `networkPolicy.allow`; `networkPolicy.enabled: false` removes the policy. The agent's own
egress is not restricted. Both policies need a network plugin that enforces NetworkPolicy.

A pool with a `networkPolicy` always lets its runners reach the agent in `arc-systems` on port 8080.
Set `networkPolicy.gateAgentNamespace` when the agent runs elsewhere.

### Priority and resources of ARC's own pods

Runners can starve the controller and the listeners that hand them jobs. Give both a high
PriorityClass, which must already exist, and measured requests:

```ts
new Controller("arc", {
    priorityClassName: "infra-critical",
    resources: { cpuRequest: "50m", memoryRequest: "128Mi" },
});

new Pool("acme", {
    // ...
    listenerPriorityClassName: "infra-critical",
    listenerResources: { cpuRequest: "10m", memoryRequest: "64Mi" },
});
```

Both resource blocks also take `cpuLimit` and `memoryLimit`. By default the controller requests 100m
CPU and 128Mi with limits of 500m and 512Mi; `resources` replaces that block whole, so a limit left
out is no limit. Listeners have no PriorityClass, requests or limits by default.

The listener settings are per pool because ARC's controller chart has none for listeners: only a
scale set's `listenerTemplate` reaches a listener pod.

### Runner slots (load-aware capacity, #22)

`Pool.slots: {enabled: true}` makes each runner pod's runner container request and limit one
`runners.antimatter.studios/slot`, an extended resource that each node advertises from its measured
load. The scheduler places a runner pod only on a node with a free slot; a pod with none stays
Pending and its job waits at GitHub. Lowering a node's count never evicts a running pod, it only
stops new placements.

The runner-gate agent publishes the count on each node, which needs `GateAgent.manageSlots: true`
(RBAC: `patch` on `nodes/status`, nothing more). The plugin does not seed the count: a node without
the agent advertises no slots, so a pool with slots enabled never lands there by accident. Enable
the agent, let it publish, then enable `slots` on the pools that should follow it. Both are off by
default.

### Monitoring and alerts (#13)

The listeners always serve metrics on `:8080/metrics`, but nothing scrapes or alerts on them by
itself. `Controller.monitoring` wires that up. All of it is off by default. Setting the block, even
as `{}`, also serves the controller's own metrics on its pod's port 8080, and creates nothing else.
It needs `listenerMetrics` left on.

```ts
new Controller("arc", {
    monitoring: {
        podMonitor: {},
        alerts: { poolIds: ["acme"] },
        dashboard: {},
        networkPolicy: {
            allow: [{
                namespaceLabels: { "kubernetes.io/metadata.name": "monitoring" },
                podLabels: { "app.kubernetes.io/name": "prometheus" },
            }],
        },
    },
});
```

| Option | What it creates | Needs |
|---|---|---|
| `podMonitor` | A `PodMonitor` for the controller and listener pods, with `honorLabels`, so a metric's `namespace` stays the pool's namespace. `labels` and `interval` are optional. | The Prometheus Operator's CRDs. |
| `alerts` | A `PrometheusRule` with the alerts below. `labels`, `listenerFor` (default `5m`) and `queuedFor` (default `10m`) are optional. | The Prometheus Operator's CRDs. |
| `dashboard` | A ConfigMap labelled `grafana_dashboard: "1"`: assigned and running jobs, registered and idle runners, job startup and execution durations. `namespace` and `labels` are optional. | Grafana's dashboard sidecar. |
| `scrapeAnnotations` | `prometheus.io/scrape`, `port` and `path` annotations on the controller pod. `Pool.listenerScrapeAnnotations` does the same for a pool's listener. | A scraper that discovers pods by these annotations. |
| `networkPolicy` | An ingress policy on the controller and listener pods: port 8080 from the `allow` sources and the GateAgent only. `gateAgentNamespace` defaults to `arc-systems`. | A network plugin that enforces NetworkPolicy. |

The component does not look for the CRDs: asking for a `PodMonitor` or `PrometheusRule` on a cluster
without them fails the deployment. Use either the `PodMonitor` or the annotations for one Prometheus,
not both, or every series is scraped twice.

| Alert | Fires when | Severity |
|---|---|---|
| `ARCListenerNotRunning` | The controller reports a listener pod as not running for `listenerFor`. | critical |
| `ARCListenerMissing` | A pool in `poolIds` has had no listener metrics for `listenerFor`: the listener never started, is gone or is not scraped, or the controller is down and has not replaced it. | critical |
| `ARCJobsQueuedWithoutRunner` | Jobs assigned to a pool have not been running for `queuedFor`. | warning |

- List every pool in `alerts.poolIds`. A listener that never started, for example because of a bad
  credential, has no series for the other two alerts to read.
- A job waiting at GitHub because the pool is already at `maxRunners` is not assigned to the pool,
  so the queued alert does not see it.
- After a pool is removed, the controller can keep reporting its listener as not running until the
  controller restarts.
- Delivery (email, webhook, an uptime service's heartbeat) is the Alertmanager's configuration, not
  the plugin's.

The GateAgent's port 8080 has its own policy. To let a scraper reach it, add the scraper to the
GateAgent's `networkPolicy.allow`, with the same `namespaceLabels` and `podLabels` as above. The
`PodMonitor` here does not select the agent.
### Live maxRunners (load-aware capacity, #22)

`Pool.maxRunnersAgent: {enabled: true}` hands the pool's live `maxRunners` to the runner-gate agent.
`maxRunners` then becomes the ceiling, published on the AutoscalingRunnerSet as the annotation
`runners.antimatter.studios/max-runners-ceiling`, and updated on every deploy. Pulumi writes the
live `spec.maxRunners` once, at `floor` (default 1, or `minRunners` if higher), when it creates the
scale set, and then ignores it (`ignoreChanges: ["values.maxRunners"]` on the Helm release). Helm
patches the scale set with a merge of its old and new render, so an unchanged field is left out
and the agent's value stands across deploys. A pool switched over keeps its current live value.
A Helm release that is *replaced* rather than upgraded (a change Pulumi can only make by recreating
the release) recreates the AutoscalingRunnerSet at the floor; the agent raises it again from there.

The agent applies `spec.maxRunners` with server-side apply under its own field manager
(`runner-gate`). Helm last wrote the field, so the agent's first apply has to force ownership. It
needs `GateAgent.manageMaxRunners: true`: `list` and `patch` on `autoscalingrunnersets` in
`runnerNamespaces` only. Both are off by default, and without them Pulumi owns `maxRunners` as before.

### Pool credentials

Set exactly one of `app` or `token` on each pool. The recommended credential is a GitHub App
with `appId`, `installationId` and a secret PEM `privateKey`. A secret fine-grained PAT
(`token`) is also supported. The pool uses its credential for ARC and, if configured, to create
its `runnerGroup`. Runner groups require an organisation `configUrl`. The App needs only the
organisation **Self-hosted runners: write** permission for runner group management.

Manage the organisation's fork pull request approval policy with the standalone
`ForkPrApproval` resource.

## Developing

- `cd provider && go vet ./... && go test -race ./...`
- `scripts/gen-sdk.sh` after any change to a resource: CI fails when `sdk/nodejs` is stale.
- Every change adds a line under `## Unreleased` in CHANGELOG.md. Commit subjects are
  conventional (`feat:`, `fix:`), which decides the next version.
