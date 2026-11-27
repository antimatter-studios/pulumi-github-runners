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

Releases attach the plugin for darwin-arm64, linux-amd64 and linux-arm64 to a GitHub release, which
Pulumi downloads by itself when a program pins that version.

## Developing

- `cd provider && go vet ./... && go test -race ./...`
- `scripts/gen-sdk.sh` after any change to a resource: CI fails when `sdk/nodejs` is stale.
- Every change adds a line under `## Unreleased` in CHANGELOG.md. Commit subjects are
  conventional (`feat:`, `fix:`), which decides the next version.
