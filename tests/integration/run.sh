#!/usr/bin/env bash
# Deploys every component to a throwaway kind cluster with `pulumi up`, checks the cluster with
# kubectl, changes inputs and checks again, then runs `pulumi destroy` and checks that nothing is
# left. CI's integration job runs it; it runs the same on a workstation with Docker.
#
# KEEP_CLUSTER=1 keeps the cluster and the stack's state for a look afterwards.
#
# Needs: kind, kubectl, jq, docker, pulumi, go, and `pnpm install --frozen-lockfile` at the
# repository root (the program loads @pulumi/pulumi from there and the committed SDK by path).
#
# Nothing reaches GitHub: the credentials are dummies. ARC's controller cannot register the scale
# set, so no listener or runner pod ever starts; that is expected and not checked.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
cluster="${KIND_CLUSTER:-github-runners-it}"
work="$(mktemp -d)"

export KUBECONFIG="$work/kubeconfig"
export PULUMI_BACKEND_URL="file://$work/state"
export PULUMI_CONFIG_PASSPHRASE="integration-test"
export PULUMI_SKIP_UPDATE_CHECK=true
# Plugins too: the 0.0.0-dev plugin installed below must be this checkout's, not whatever another
# checkout last installed under the same version in ~/.pulumi.
export PULUMI_HOME="$work/pulumi-home"
mkdir -p "$work/state" "$PULUMI_HOME"

# The GateAgent requires its image from the private registry, which CI cannot pull from. A stand-in
# (pause, 3.10) is tagged in the node's containerd under that exact name, so the kubelet never pulls (IfNotPresent: the tag is
# not latest) and the Deployment really becomes Available. pause runs as a non-root user, keeps no
# files, and ignores the agent's arguments.
agent_image="git.antimatter-studios.com/antimatter-studios/antimatter-vps-gate-webservice:0123456789abcdef"
stand_in="registry.k8s.io/pause@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a"

step() { printf '\n==> %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

diagnose() {
  status=$?
  if [ "$status" -ne 0 ]; then
    step "diagnostics (exit $status)"
    kubectl get all,networkpolicy,role,rolebinding -A 2>&1 | grep -v kube-system || true
    kubectl get events -A --sort-by=.lastTimestamp 2>&1 | tail -40 || true
  fi
  if [ -n "${KEEP_CLUSTER:-}" ]; then
    echo "kept: cluster $cluster, KUBECONFIG=$KUBECONFIG PULUMI_BACKEND_URL=$PULUMI_BACKEND_URL PULUMI_HOME=$PULUMI_HOME"
  else
    kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
    rm -rf "$work" "$here/Pulumi.it.yaml"
  fi
  exit "$status"
}
trap diagnose EXIT

step "kind cluster $cluster"
kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 120s
node="$cluster-control-plane"
platform="linux/$(docker exec "$node" uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
# The node's containerd can restart once just after the cluster reports ready; retry through it.
for attempt in $(seq 30); do
  if docker exec "$node" ctr -n k8s.io images pull --platform "$platform" "$stand_in" >/dev/null 2>&1; then break; fi
  [ "$attempt" -lt 30 ] || fail "could not pull $stand_in into the node"
  sleep 2
done
docker exec "$node" ctr -n k8s.io images tag --force "$stand_in" "$agent_image"
kubectl wait --for=condition=Ready node --all --timeout=120s >/dev/null

step "plugin"
(cd "$root/provider" && go build -o bin/pulumi-resource-github-runners ./cmd/pulumi-resource-github-runners)
pulumi plugin install resource github-runners 0.0.0-dev -f "$root/provider/bin/pulumi-resource-github-runners"

cd "$here"
# The stack's config file is generated per run, with its own passphrase salt.
rm -f Pulumi.it.yaml
pulumi stack init it --non-interactive

# ---- checks ----

# rules_of KIND NAME [NAMESPACE]: the role's rules, normalised and sorted so they compare as text.
rules_of() {
  kubectl get "$1" "$2" ${3:+-n "$3"} -o json |
    jq -c '[.rules[] | {apiGroups: (.apiGroups | sort), resources: (.resources | sort), verbs: (.verbs | sort)}] | sort'
}
# rule GROUP RESOURCE VERB[,VERB...]
rule() { jq -cn --arg g "$1" --arg r "$2" --arg v "$3" '{apiGroups: [$g], resources: [$r], verbs: ($v | split(",") | sort)}'; }
expect_rules() { # KIND NAME NAMESPACE RULE...
  local kind=$1 name=$2 namespace=$3; shift 3
  local want got
  want="$(printf '%s\n' "$@" | jq -sc 'sort')"
  got="$(rules_of "$kind" "$name" "$namespace")"
  [ "$got" = "$want" ] || fail "$kind $name${namespace:+ in $namespace}: rules are $got, want $want"
  pass "$kind $name${namespace:+ in $namespace} has exactly its rules"
}

# The monitoring NetworkPolicy shuts every unlisted source out of the metrics port, and the GateAgent
# reads the listeners' metrics there. So one of its peers must select the agent's running pods: the
# peer's own selectors are put to the cluster, not compared with what the plugin meant to write.
check_metrics_policy() {
  local policy agent controller peer namespaces pods selector admitted=
  policy="$(kubectl get networkpolicy arc-metrics -n arc-systems -o json)" || fail "no monitoring NetworkPolicy arc-metrics"
  jq -e '
    .spec.policyTypes == ["Ingress"] and (.spec.ingress | length) == 1 and
    .spec.ingress[0].ports == [{protocol: "TCP", port: 8080}]
  ' <<<"$policy" >/dev/null || fail "monitoring NetworkPolicy arc-metrics: $(jq -c .spec <<<"$policy")"
  # It has to select the controller's pod, or it restricts nothing.
  controller="$(kubectl get pods -n arc-systems -o name -l "$(jq -r '
    .spec.podSelector.matchExpressions | map("\(.key) \(.operator | ascii_downcase) (\(.values | join(",")))") | join(",")' <<<"$policy")")"
  grep -q '^pod/arc-gha-rs-controller-' <<<"$controller" || fail "arc-metrics does not select the controller pod, only: $controller"
  agent="$(kubectl get pods -n arc-systems -l app=runner-gate-agent --field-selector=status.phase=Running -o name)"
  [ -n "$agent" ] || fail "no running GateAgent pod"
  # A peer admits the agent when its selectors match both the agent's namespace and its pods. A peer
  # without a namespaceSelector looks in the policy's own namespace.
  selector='to_entries | map("\(.key)=\(.value)") | join(",")'
  while read -r peer; do
    namespaces="$(jq -r ".namespaceSelector.matchLabels // {} | $selector" <<<"$peer")"
    pods="$(jq -r ".podSelector.matchLabels // {} | $selector" <<<"$peer")"
    if [ -n "$namespaces" ]; then
      kubectl get namespaces -l "$namespaces" -o name | grep -qx namespace/arc-systems || continue
    fi
    [ -n "$pods" ] || continue
    [ "$(kubectl get pods -n arc-systems -l "$pods" --field-selector=status.phase=Running -o name)" = "$agent" ] || continue
    admitted=yes
  done < <(jq -c '.spec.ingress[0].from[]' <<<"$policy")
  [ -n "$admitted" ] || fail "arc-metrics admits no source selecting the GateAgent's pods: $(jq -c '.spec.ingress[0].from' <<<"$policy")"
  pass "monitoring NetworkPolicy arc-metrics admits the GateAgent's pods on 8080"
}

check_deployed() {
  local manage_slots=$1 manage_max_runners=$2

  kubectl wait --for=condition=Available deployment/arc-gha-rs-controller -n arc-systems --timeout=120s >/dev/null
  pass "ARC controller Deployment is Available"
  kubectl wait --for=condition=Available deployment/runner-gate-agent -n arc-systems --timeout=120s >/dev/null
  pass "GateAgent Deployment is Available"
  for ds in fake-device-plugin runner-image-prepull; do
    kubectl rollout status "daemonset/$ds" -n arc-systems --timeout=120s >/dev/null
    pass "DaemonSet $ds is ready"
  done

  [ "$(kubectl get namespace arc-runners-it -o jsonpath='{.metadata.labels.github-runners\.antimatter\.studios/pool}')" = it ] ||
    fail "namespace arc-runners-it lacks label github-runners.antimatter.studios/pool=it"
  pass "pool namespace carries github-runners.antimatter.studios/pool=it"

  local set
  set="$(kubectl get autoscalingrunnerset kind-linux-x64 -n arc-runners-it -o json)" || fail "no AutoscalingRunnerSet kind-linux-x64"
  jq -e '
    .metadata.labels["actions.github.com/scale-set-name"] == "kind-linux-x64" and
    .metadata.labels["actions.github.com/scale-set-namespace"] == "arc-runners-it" and
    .metadata.labels["app.kubernetes.io/component"] == "autoscaling-runner-set" and
    .metadata.annotations["actions.github.com/cleanup-manager-role-name"] == "kind-linux-x64-gha-rs-manager" and
    (.metadata.annotations["actions.github.com/values-hash"] | length > 0) and
    .spec.githubConfigUrl == "https://github.com/integration-test-org" and
    .spec.githubConfigSecret == "arc-github" and
    .spec.runnerScaleSetName == "kind-linux-x64" and
    .spec.minRunners == 0 and .spec.maxRunners == 2
  ' <<<"$set" >/dev/null || fail "AutoscalingRunnerSet metadata or spec: $(jq -c '{labels: .metadata.labels, annotations: .metadata.annotations, spec: (.spec | del(.template))}' <<<"$set")"
  pass "AutoscalingRunnerSet kind-linux-x64 has its name, labels, annotations and spec"

  local pod
  pod="$(jq '.spec.template.spec' <<<"$set")"
  jq -e '.hostUsers == false and .automountServiceAccountToken == false' <<<"$pod" >/dev/null ||
    fail "runner pod: hostUsers or automountServiceAccountToken: $(jq -c '{hostUsers, automountServiceAccountToken}' <<<"$pod")"
  pass "runner pod has hostUsers false"
  jq -e '[.containers[].name] == ["runner", "docker"]' <<<"$pod" >/dev/null || fail "runner pod containers: $(jq -c '[.containers[].name]' <<<"$pod")"
  for c in runner:10Gi docker:20Gi; do
    local name=${c%%:*} storage=${c#*:} limits
    limits="$(jq -c --arg n "$name" '.containers[] | select(.name == $n) | .resources.limits' <<<"$pod")"
    jq -e --arg s "$storage" '
      (has("cpu") | not) and (has("memory") | not) and
      .["ephemeral-storage"] == $s and .["devic.es/fake"] == "1"
    ' <<<"$limits" >/dev/null || fail "$name container limits: $limits"
    pass "$name container: no CPU or memory limit, ephemeral-storage $storage, devic.es/fake 1"
  done

  kubectl get networkpolicy runners-egress -n arc-runners-it -o json | jq -e '
    .spec.policyTypes == ["Ingress", "Egress"] and (.spec.ingress // []) == [] and
    any(.spec.egress[]; .to[0].ipBlock.cidr == "0.0.0.0/0" and (.to[0].ipBlock.except | index("10.0.0.0/8") != null)) and
    any(.spec.egress[]; .to[0].namespaceSelector.matchLabels["kubernetes.io/metadata.name"] == "arc-systems" and
      .to[0].podSelector.matchLabels.app == "runner-gate-agent" and .ports == [{protocol: "TCP", port: 8080}])
  ' >/dev/null || fail "pool NetworkPolicy runners-egress: $(kubectl get networkpolicy runners-egress -n arc-runners-it -o jsonpath='{.spec}')"
  pass "pool NetworkPolicy runners-egress blocks private ranges and admits the agent"
  kubectl get networkpolicy runner-gate-agent -n arc-systems -o json | jq -e '
    .spec.policyTypes == ["Ingress"] and
    .spec.ingress[0].from[0].namespaceSelector.matchExpressions == [{key: "github-runners.antimatter.studios/pool", operator: "Exists"}] and
    .spec.ingress[0].ports == [{protocol: "TCP", port: 8080}]
  ' >/dev/null || fail "agent NetworkPolicy runner-gate-agent"
  pass "agent NetworkPolicy runner-gate-agent admits pool namespaces on 8080"

  check_metrics_policy

  # Every binding to the agent's service account, cluster-wide: exactly these three.
  local bindings
  bindings="$(kubectl get rolebindings,clusterrolebindings -A -o json | jq -c '
    [.items[] | select(any(.subjects[]?; .kind == "ServiceAccount" and .name == "runner-gate-agent" and .namespace == "arc-systems"))
     | "\(.kind)/\(.metadata.namespace // "")/\(.metadata.name)->\(.roleRef.kind)/\(.roleRef.name)"] | sort')"
  [ "$bindings" = '["ClusterRoleBinding//runner-gate-agent->ClusterRole/runner-gate-agent","RoleBinding/arc-runners-it/runner-gate-agent-runner->Role/runner-gate-agent-runner","RoleBinding/arc-systems/runner-gate-agent-listener->Role/runner-gate-agent-listener"]' ] ||
    fail "agent bindings: $bindings"
  pass "agent service account has exactly its three bindings"
  expect_rules role runner-gate-agent-listener arc-systems "$(rule "" pods list)"
  local runner_rules=("$(rule "" pods list)" "$(rule metrics.k8s.io pods list)" "$(rule actions.github.com ephemeralrunners list)")
  if [ "$manage_max_runners" = true ]; then runner_rules+=("$(rule actions.github.com autoscalingrunnersets list,patch)"); fi
  expect_rules role runner-gate-agent-runner arc-runners-it "${runner_rules[@]}"
  expect_rules clusterrole runner-gate-agent "" "$(rule "" nodes list)" "$(rule metrics.k8s.io nodes list)"

  # The probe: a DaemonSet on every node, read-only but for nodes/status with manageSlots.
  kubectl rollout status daemonset/runner-gate-probe -n arc-systems --timeout=120s >/dev/null || fail "probe DaemonSet not rolled out"
  local slots
  slots="$(kubectl get daemonset runner-gate-probe -n arc-systems -o json | jq -r '.spec.template.spec.containers[0].env[] | select(.name == "PROBE_SLOTS") | .value')"
  local want_slots=dry-run
  if [ "$manage_slots" = true ]; then want_slots=apply; fi
  [ "$slots" = "$want_slots" ] || fail "probe PROBE_SLOTS $slots, want $want_slots"
  pass "probe DaemonSet runs, PROBE_SLOTS=$slots"
  expect_rules role runner-gate-probe arc-systems "$(rule "" pods list)"
  expect_rules role runner-gate-probe arc-runners-it "$(rule "" pods list)"
  local probe_rules=("$(rule "" nodes get)")
  if [ "$manage_slots" = true ]; then probe_rules+=("$(rule "" nodes/status patch)"); fi
  expect_rules clusterrole runner-gate-probe "" "${probe_rules[@]}"
}

check_gone() {
  for ns in arc-systems arc-runners-it; do
    kubectl wait --for=delete "namespace/$ns" --timeout=180s >/dev/null 2>&1 || true
    if kubectl get namespace "$ns" >/dev/null 2>&1; then fail "namespace $ns is still there"; fi
  done
  pass "namespaces arc-systems and arc-runners-it are gone"
  local left
  left="$(kubectl get clusterroles,clusterrolebindings -o name | grep -E 'runner-gate-agent|runner-gate-probe|gha-rs' || true)"
  [ -z "$left" ] || fail "cluster RBAC left behind: $left"
  pass "no cluster roles or bindings left"
  # Helm never removes the CRDs a chart installed; any custom resource of theirs would be ours.
  left="$(kubectl get autoscalingrunnersets,autoscalinglisteners,ephemeralrunnersets,ephemeralrunners -A -o name 2>/dev/null || true)"
  [ -z "$left" ] || fail "ARC resources left behind: $left"
  pass "no ARC resources left"
  [ "$(pulumi stack export | jq '.deployment.resources // [] | length')" = 0 ] || fail "the stack still holds resources"
  pass "the stack holds no resources"
}

# Each permission on its own, then neither: a rule appears only with its own input, and goes with it.
deploy() { # manageSlots manageMaxRunners
  step "pulumi up (manageSlots: $1, manageMaxRunners: $2)"
  pulumi config set manageSlots "$1"
  pulumi config set manageMaxRunners "$2"
  pulumi up --yes --skip-preview --non-interactive
  check_deployed "$1" "$2"
}

deploy true false
deploy false true
deploy false false

step "pulumi destroy"
pulumi destroy --yes --skip-preview --non-interactive
check_gone

step "passed"
