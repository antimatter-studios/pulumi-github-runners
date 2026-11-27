# Changelog

## Unreleased

- `Controller.monitoring`: opt-in wiring for a scraper (#13): a `PodMonitor`, a `PrometheusRule`
  alerting on listeners that are not running or missing and on jobs queued without a runner, a
  Grafana dashboard ConfigMap, scrape annotations on the controller pod, and a NetworkPolicy that
  admits only the listed scrapers and the GateAgent to the metrics port. Setting it also serves the
  controller's own metrics. Off by default.
- `Pool.listenerScrapeAnnotations`: Prometheus scrape annotations on the pool's listener pod. Off
  by default.
- `Pool.slots`: each runner pod requests and limits one `runners.antimatter.studios/slot` (or
  `slots.resourceName`) on its runner container, so runners are placed only on nodes whose
  published slots have room. Off by default. Nodes advertise no slots until the runner-gate agent
  publishes them, so enable the agent first.
- `GateAgent.manageSlots`: grants the agent `patch` on `nodes/status`, and nothing else, to publish
  each node's slot count. Off by default.
- `GateAgent` admits only pool namespaces (labelled `github-runners.antimatter.studios/pool`, which
  `Pool` now sets) to its port 8080, on by default; `networkPolicy.allow` adds sources and
  `networkPolicy.enabled: false` opts out. A pool's `networkPolicy` now lets its runners reach the
  agent, in `networkPolicy.gateAgentNamespace` (default `arc-systems`).
- `Pool.dockerMemoryRequest`: the Docker sidecar's memory request (default 128Mi). Docker builds and job
  services run in the sidecar, so under the kubelet's LimitedSwap its request is what sizes its swap.
- `Controller.prePull`: an optional DaemonSet that keeps the runner and podman images (or the
  listed `images`) on every node, so pools scaling from zero skip the image pull.
- `Pool.env`: every job's default environment, e.g. parallelism hints (GOMAXPROCS, CARGO_BUILD_JOBS,
  FLTH_RESOURCE_PROFILE) so jobs without CPU limits stop sizing themselves to the whole node.
- Runner pods have no CPU or memory limit unless a pool sets one; the memory limits that defaulted
  to 4Gi on the runner and the podman sidecar are gone. Disk (ephemeral-storage) limits stay.
- The plugin, `pulumi-resource-github-runners`, with a generated TypeScript SDK
  (`@antimatter-studios/github-runners`).
- `ForkPrApproval`: an organisation's fork pull request approval policy. Deleting it leaves the
  setting as it is.
- `Controller`: Actions Runner Controller for one cluster, installed as a Helm release so its CRDs
  exist before it starts, with listener metrics for the runner-gate and device plugins (FUSE, KVM).
- `Pool`: one organisation's runners on one machine, with an optional runner group.
  The pod is antimatter-vps's design: rootless podman sidecar, `hostUsers:
  false`, `ndots: 1`, a digest-pinned runner image, device-plugin resources, optional hard topology
  spread, and every size as an input, including pod-level resources. The job-started hook refuses
  forks by default and can add the pool's own script; a NetworkPolicy option keeps runners off
  private addresses.
- Pool credentials now favour a GitHub App, with a PAT as an alternative. The same credential
  manages the runner group. Manage fork pull request approval with the standalone resource.
- Runners wait for the podman socket before starting. Pod-level CPU and memory limits take
  precedence over the corresponding container limits.
- `GateAgent` reports a cluster's runner pools to the public runner-gate and serves the in-cluster
  claim endpoint using a dedicated ServiceAccount with list-only permissions. Like runner pods, the
  agent requests CPU and memory but has no limit.
