# Changelog

## Unreleased

- `Controller.registryMirror`: a Docker Hub pull-through cache in the cluster, and `Pool.registryMirror`
  to pull docker.io images through it. Each runner pod's podman starts with empty storage, so every
  job pulled its images from Docker Hub again, and a machine's runners share one IPv4 address and its
  100 anonymous pulls an hour. On the Ryzen at 12 runners the quota ran out
  (`ratelimit-remaining: 0;w=3600`): podman's pull got `toomanyrequests` and rest-mail's E2E jobs failed
  at the next step with `image not known`. With the mirror, Docker Hub serves each image once. Pools
  without `registryMirror` are unchanged.
- The default runner image now has udev (`udevadm`), kmod (`modprobe`, `lsmod`), fdisk (`sfdisk`,
  `fdisk`) and acl (`setfacl`, `getfacl`), as GitHub's ubuntu-24.04 image does. KVM jobs failed in
  fs-linux-test-harness's `ci-setup-linux.sh` with `sudo: udevadm: command not found`. A pool that
  sets its own `image` is unaffected.
- `Pool.nodeSelector`: only nodes with these labels run the pool's runners. One pool per node lets the
  runner gate pick the node by picking the pool's label.

- **Changed:** with `Pool.maxRunnersAgent`, the agent's `maxRunners` has no cap (#22). The plugin marks
  the AutoscalingRunnerSet with `runners.antimatter.studios/max-runners-agent: "true"` instead of the
  `max-runners-ceiling` annotation, and `maxRunners` is the value a new scale set starts at, not a
  ceiling. Pulumi still ignores the live field after creation.
- **Removed:** `Pool.maxRunnersAgent.floor`, `AGENT_MAX_RUNNERS_FLOOR` in `GateAgent.maxRunnersSettings`,
  and `PROBE_FLOOR` and `PROBE_CEILING` in `probe.settings`, which the plugin now refuses. The only
  settings are how the loop reacts: pressure thresholds, rates and interval.
- The probe's ClusterRole lists `autoscalingrunnersets` cluster-wide, to bound its node's slots by the
  runner pods' requests. It writes none.
- **Changed:** the agent gets `patch` on `autoscalingrunnersets` only in `maxRunnersMode: apply`;
  `manageMaxRunners` with `dry-run` now grants `list` only.

- `GateAgent.probe`: runs `runner-gate probe` on every node (#23), dry run by default, with its
  `PROBE_*` settings in `probe.settings`. `probe.evict` (`off`, `dry-run` by default, `on`) is the
  youngest-runner eviction when memory runs out (#31); `on` grants `delete` on pods in the runner
  namespaces only.
- **Changed:** `GateAgent.manageSlots` now grants `patch` on `nodes/status` to the probe, not the
  agent, and turns the probe's `PROBE_SLOTS` to `apply`. It needs `probe.enabled`.
- `GateAgent.maxRunnersMode` (`off`, `dry-run`, `apply`) and `maxRunnersSettings`: the agent's live
  `maxRunners` (#26) from the probes' slots. `apply` needs `manageMaxRunners`, and is the default with
  it; `dry-run` grants only `list` on the scale sets.

- The default runner image now has the GitHub CLI (`gh`). Jobs that called it, release jobs among
  them, failed with `gh: command not found`. A pool that sets its own `image` is unaffected.
- The podman sidecar removes inherited default POSIX ACLs from its storage, `/home/runner/_work`
  and `/tmp` before podman starts. A default ACL makes the kernel ignore the creator's umask, so on a
  node whose k3s data sits under one (the Ryzen's `/mnt/storage`), files and sockets in job
  containers lost permission bits they asked for: dovecot's 0666 login socket came out 0664 and
  refused every IMAP client. Nodes without default ACLs are unchanged.

- `Controller.createNamespace`: set `false` to install into a namespace that already exists, which
  pulumi-kubernetes refuses to create. Default `true`. Setting it on a Controller that created its
  namespace deletes that namespace and everything in it.
- `Controller.priorityClassName` and `Controller.resources` for the controller manager pod, and
  `Pool.listenerPriorityClassName` and `Pool.listenerResources` for a pool's listener pod, so runners
  cannot starve them. All unset by default. `Controller.resources` replaces the controller's default
  requests and limits whole.
- `Pool.gateClaim` (`{agentNamespace}` or `{url}`): the plugin ships the runner-gate claim script,
  runs it after the fork check and before `hook.script`, and sets `RUNNER_GATE_URL`. It fails open
  when the gate cannot be reached. Pools no longer need their own copy of the script.
- `Controller.monitoring`: opt-in wiring for a scraper (#13): a `PodMonitor`, a `PrometheusRule`
  alerting on listeners that are not running or missing and on jobs queued without a runner, a
  Grafana dashboard ConfigMap, scrape annotations on the controller pod, and a NetworkPolicy that
  admits only the listed scrapers and the GateAgent to the metrics port. Setting it also serves the
  controller's own metrics. Off by default.
- `Pool.listenerScrapeAnnotations`: Prometheus scrape annotations on the pool's listener pod. Off
  by default.
- `Pool.maxRunnersAgent`: the runner-gate agent owns the pool's live `maxRunners`. `maxRunners`
  becomes the ceiling, published as the annotation `runners.antimatter.studios/max-runners-ceiling`;
  Pulumi creates the scale set at `floor` and then ignores the live field. Off by default.
- `GateAgent.manageMaxRunners`: grants the agent `list` and `patch` on `autoscalingrunnersets` in its
  runner namespaces only. Off by default.
- Component inputs a program may pass as Outputs now take them: `GateAgent.cluster`, `image`,
  `gateUrl`, `namespace`, `listenerNamespace`, `runnerNamespaces` and `imagePull` (each field, or the
  whole object as one secret Output); `Pool.app` (likewise), `env`, `hook.env`,
  `networkPolicy.gateAgentNamespace`, `gateClaim.url` and `gateClaim.agentNamespace`; and
  `Controller.monitoring.networkPolicy.gateAgentNamespace` and `monitoring.dashboard.namespace`.
  Before, an Output there (a secret, a Pool's namespace) failed the deployment at Construct:
  "copying input ...: expected destination type to implement pulumi.Input or pulumi.Output".
- Every other input is plain in the schema, so the SDK types it as a plain value and a program's type
  check refuses an Output there, instead of the deployment failing.
- CI's `integration` job deploys every component to a kind cluster with `pulumi up`, checks the
  result with kubectl (controller, scale set, runner pod template, network policies, agent RBAC),
  and checks that `pulumi destroy` leaves nothing behind. `tests/integration/run.sh` runs it locally.
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
