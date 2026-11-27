# Changelog

## Unreleased

- The plugin, `pulumi-resource-github-runners`, with a generated TypeScript SDK
  (`@antimatter-studios/github-runners`).
- `ForkPrApproval`: an organisation's fork pull request approval policy. Deleting it leaves the
  setting as it is.
- `Controller`: Actions Runner Controller for one cluster, installed as a Helm release so its CRDs
  exist before it starts, with listener metrics for the runner-gate and device plugins (FUSE, KVM).
- `Pool`: one organisation's runners on one machine. Credentials are a PAT or a GitHub App, with an
  optional runner group and fork PR approval policy managed with a separate admin token that never
  enters the cluster. The pod is antimatter-vps's design: rootless podman sidecar, `hostUsers:
  false`, `ndots: 1`, a digest-pinned runner image, device-plugin resources, optional hard topology
  spread, and every size as an input, including pod-level resources. The job-started hook refuses
  forks by default and can add the pool's own script; a NetworkPolicy option keeps runners off
  private addresses.
