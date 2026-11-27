# Changelog

## Unreleased

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
