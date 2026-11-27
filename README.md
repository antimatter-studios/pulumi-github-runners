# pulumi-github-runners

A Pulumi provider plugin, written in Go, for self-hosted GitHub Actions runners on Kubernetes. Each
cluster that runs runners gets them the same way: Actions Runner Controller, one runner pool per
organisation and machine, Docker for jobs without privileged containers, and a job-started hook
chosen per pool. Gitea Actions runners come from the same plugin, as `GiteaRunnerPool`.

The design, its rules and the order of work are in the tracking issue,
[#1](https://github.com/antimatter-studios/pulumi-github-runners/issues/1).

## Resources

| Type | What it is |
|---|---|
| `Controller` (component) | ARC's controller for one cluster, as a Helm release, with listener metrics and optional device plugins offering host devices as `devic.es/<name>`. |
| `Controller.prePull` | Optional DaemonSet that keeps runner images on every node, so a pod scaled from zero does not wait for its pulls. `{}` pre-pulls the default runner and podman images; list a pool's own `image` in `images` if it sets one. Each image runs `true` once as an unprivileged init container, then the pod idles on `pause` with tiny requests and a 32Mi memory limit. `priorityClassName` is optional. |
| `Pool` (component) | One organisation's runners on one machine, named `<machine>-<os>-<arch>`: credentials, runner group, scale set, the rootless podman sidecar, the job-started hook and an optional NetworkPolicy. |
| `GiteaRunnerPool` (component) | Gitea Actions runners for one repository, organisation, user or instance: a fixed-size StatefulSet with a persistent registration per runner and a Docker sidecar. See [Gitea runners](#gitea-runners). |
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

### Pool credentials

Set exactly one of `app` or `token` on each pool. The recommended credential is a GitHub App
with `appId`, `installationId` and a secret PEM `privateKey`. A secret fine-grained PAT
(`token`) is also supported. The pool uses its credential for ARC and, if configured, to create
its `runnerGroup`. Runner groups require an organisation `configUrl`. The App needs only the
organisation **Self-hosted runners: write** permission for runner group management.

Manage the organisation's fork pull request approval policy with the standalone
`ForkPrApproval` resource.

## Gitea runners

ARC cannot drive Gitea's runners, which speak another protocol, so `GiteaRunnerPool` shares
`Pool`'s pod design and not its controller. It needs no `Controller`.

```ts
new githubRunners.GiteaRunnerPool("gitea-runners", {
    poolId: "ryzen",
    instanceUrl: "https://git.example.com",
    scope: "repo", // instance, org, user or repo
    owner: "acme",
    repo: "server",
    registrationToken: config.requireSecret("giteaRegistrationToken"),
    replicas: 2,
    capacity: 1,
    env: { GOMAXPROCS: "2" },
}, { provider: kubernetes });
```

Each pool gets a namespace `gitea-runners-<poolId>`, a Secret with the token, a ConfigMap with the
runner's configuration and a StatefulSet whose runners register as `<poolId>-0`, `<poolId>-1`, ...
Each runner keeps its registration on its own `/data` claim, so a restart does not register again.
A new instance or scope does. Runners removed by scaling down stay listed in Gitea until deleted
there, and their claims are deleted with them.

### Registration token

Mint it for the pool's scope, with a credential that administers that scope:

```sh
curl -X POST -H "Authorization: token $GITEA_API_TOKEN" \
  https://git.example.com/api/v1/repos/{owner}/{repo}/actions/runners/registration-token
```

| `scope` | Inputs | Endpoint under `/api/v1` |
|---|---|---|
| `repo` | `owner`, `repo` | `/repos/{owner}/{repo}/actions/runners/registration-token` |
| `org` | `owner` | `/orgs/{org}/actions/runners/registration-token` |
| `user` | `owner` | `/user/actions/runners/registration-token`, asked as that user |
| `instance` | none | `/admin/actions/runners/registration-token` |

The pool's `registrationTokenEndpoint` output is the full URL. One token registers any number of
runners. It is read only when a runner registers, by an init container, so no job can read it.

### Job modes

A label decides where its jobs run. **Both modes are supported, and neither is proven on a cluster
yet: prove the one you mean to use on a pool before moving real jobs to it.**

**`docker://` labels (default).** `ubuntu-latest:docker://<image>` runs each job in a container of
that image, started through the Docker socket of the sidecar. Each StatefulSet ordinal keeps the
sidecar's images on its own volume (`engineStorageSize`, default 20Gi), so a restart pulls nothing
again. Nothing prunes that volume: run `docker system prune` from a scheduled workflow, or delete
the claim when it fills. `persistentEngineStorage: false` uses an `emptyDir` instead.

**`:host` labels.** `linux:host`, or a bare `linux`, runs the steps in the runner container. The
sidecar serves the `docker` CLI and jobs that set `container:`. The runner (2.0.1) starts a job's
`services:` only when the job also sets `container:`; a host job starts what it needs with
`docker run`. Host labels need `image`: one built on
[antimatter-studios/actions-runner-image](https://github.com/antimatter-studios/actions-runner-image),
plus `gitea-runner`, `tini` and a `node` on `PATH` for JavaScript actions.

```dockerfile
FROM ghcr.io/antimatter-studios/actions-runner:2.338.2-31c1bb8@sha256:5500713dffc79e980dbba54f9bd4b91f4f6d911caa163db49fcc70291773bd5f
USER root
RUN apt-get update && apt-get install -y --no-install-recommends tini && rm -rf /var/lib/apt/lists/*
COPY --from=docker.gitea.com/runner:2.0.1 /usr/local/bin/gitea-runner /usr/local/bin/gitea-runner
COPY --from=docker.io/library/node:24-bookworm-slim /usr/local/bin/node /usr/local/bin/node
USER runner
```

Push it to a registry and give the pool its digest.

### The Docker sidecar

`engine: "podman"` (default) is the rootless podman sidecar `Pool` uses, with `hostUsers: false`
and no privileged container. It needs Kubernetes user namespaces, and storage classes for both
claims that support idmapped mounts (and overlayfs, for the engine's). Its default image is podman
5.8, not `Pool`'s 6.x: 6.1.3 fails every `uses:` step of a `docker://` job.

`engine: "dind"` is Docker in a privileged container, for a cluster or a job rootless podman cannot
serve.

### Scheduling and permissions

- No CPU or memory limits unless `runnerResources` or `engineResources` set them. Ephemeral storage
  limits stay.
- Runners spread evenly across nodes, hard: with a node down, a runner that would unbalance the
  others stays Pending. `topologySpread: false` removes the constraint. `nodeSelector` and
  `priorityClassName` are optional.
- No Kubernetes API token is mounted, and the pool creates no ServiceAccount, role or binding. Jobs
  that deploy need `automountServiceAccountToken: true`, and usually a `serviceAccountName` that you
  create in the pool's namespace with only the rights those jobs need. Every job of the pool can
  read that token.
- `updatePartition` keeps the runners below an ordinal on their old pod spec, so a deploy running on
  them does not restart them.
- There is no NetworkPolicy and no fork guard: do not give a pool to repositories whose pull
  requests you do not trust.

### Later

This version is a fixed number of long-lived runners. The next step is ephemeral runners
(`gitea-runner register --ephemeral`, one job each) started by a poller of the instance's queued
jobs, taking node slots from the same budget as `Pool`'s runners (#22-#28, `Pool.slots`).

## Developing

- `cd provider && go vet ./... && go test -race ./...`
- `scripts/gen-sdk.sh` after any change to a resource: CI fails when `sdk/nodejs` is stale.
- Every change adds a line under `## Unreleased` in CHANGELOG.md. Commit subjects are
  conventional (`feat:`, `fix:`), which decides the next version.
