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
| `Controller.prePull` | Optional DaemonSet that keeps runner images on every node, so a pod scaled from zero does not wait for its pulls. `{}` pre-pulls the default runner and podman images; list a pool's own `image` in `images` if it sets one. Each image runs `true` once as an unprivileged init container, then the pod idles on `pause` with tiny requests and a 32Mi memory limit. `priorityClassName` is optional. |
| `Pool` (component) | One organisation's runners on one machine, named `<machine>-<os>-<arch>`: credentials, runner group, scale set, the rootless podman sidecar, the job-started hook and an optional NetworkPolicy. |
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

### Live maxRunners (load-aware capacity, #22)

`Pool.maxRunnersAgent: {enabled: true}` hands the pool's live `maxRunners` to the runner-gate agent.
`maxRunners` then becomes the ceiling, published on the AutoscalingRunnerSet as the annotation
`runners.antimatter.studios/max-runners-ceiling`, and updated on every deploy. Pulumi writes the
live `spec.maxRunners` once, at `floor` (default 1, or `minRunners` if higher), when it creates the
scale set, and then ignores it (`ignoreChanges: ["values.maxRunners"]` on the Helm release). Helm
patches the scale set with a merge of its old and new render, so an unchanged field is left out
and the agent's value stands across deploys. A pool switched over keeps its current live value.

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
