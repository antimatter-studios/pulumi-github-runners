package githubrunners

import (
	"fmt"
	"regexp"
	"sort"

	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	rbacv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/rbac/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The per-node probe (runner-gate probe, #23 and #31): one pod per node reads the node's pressure,
// memory and runner pods, works out its runner slots (AIMD) and, when memory runs out, evicts the
// youngest runner pod. The agent reads each probe's slots from its /metrics.
const (
	gateProbeName     = "runner-gate-probe"
	gateProbeLabelKey = "app.kubernetes.io/name"
	gateProbePort     = 8080
)

var (
	probeSettingName      = regexp.MustCompile(`^PROBE_[A-Z0-9_]+$`)
	maxRunnersSettingName = regexp.MustCompile(`^AGENT_MAX_RUNNERS_(FLOOR|STEP|INTERVAL)$`)
	// Set by the component, from its own inputs.
	probeReservedSettings = map[string]bool{"PROBE_SLOTS": true, "PROBE_EVICT": true, "PROBE_NAMESPACE": true,
		"PROBE_SELECTOR": true, "PROBE_PORT": true, "PROBE_ROOT": true}
)

func probeEnabled(a GateAgentArgs) bool {
	return a.Probe != nil && a.Probe.Enabled != nil && *a.Probe.Enabled
}

func probeEvict(a GateAgentArgs) string {
	if a.Probe != nil && a.Probe.Evict != nil {
		return *a.Probe.Evict
	}
	return "dry-run"
}

func manageSlots(a GateAgentArgs) bool { return a.ManageSlots != nil && *a.ManageSlots }

func manageMaxRunners(a GateAgentArgs) bool { return a.ManageMaxRunners != nil && *a.ManageMaxRunners }

// maxRunnersMode is the agent's AGENT_MAX_RUNNERS.
func maxRunnersMode(a GateAgentArgs) string {
	if a.MaxRunnersMode != nil {
		return *a.MaxRunnersMode
	}
	if manageMaxRunners(a) {
		return "apply"
	}
	return "off"
}

// checkGateAgentCapacity refuses settings that do not fit together.
func checkGateAgentCapacity(a GateAgentArgs) error {
	if manageSlots(a) && !probeEnabled(a) {
		return fmt.Errorf("gate agent: manageSlots needs probe.enabled: the probe publishes the slots")
	}
	switch e := probeEvict(a); e {
	case "off", "dry-run", "on":
	default:
		return fmt.Errorf("gate agent: probe.evict %q must be off, dry-run or on", e)
	}
	if a.Probe != nil {
		for k := range a.Probe.Settings {
			if !probeSettingName.MatchString(k) {
				return fmt.Errorf("gate agent: probe.settings %q is not a PROBE_* setting", k)
			}
			if probeReservedSettings[k] {
				return fmt.Errorf("gate agent: probe.settings %s is set by the component", k)
			}
		}
	}
	switch m := maxRunnersMode(a); m {
	case "off", "dry-run":
	case "apply":
		if !manageMaxRunners(a) {
			return fmt.Errorf("gate agent: maxRunnersMode apply needs manageMaxRunners")
		}
	default:
		return fmt.Errorf("gate agent: maxRunnersMode %q must be off, dry-run or apply", m)
	}
	for k := range a.MaxRunnersSettings {
		if !maxRunnersSettingName.MatchString(k) {
			return fmt.Errorf("gate agent: maxRunnersSettings %q must be AGENT_MAX_RUNNERS_FLOOR, _STEP or _INTERVAL", k)
		}
	}
	if maxRunnersMode(a) != "off" && !probeEnabled(a) {
		return fmt.Errorf("gate agent: maxRunnersMode %s needs probe.enabled: maxRunners follows the probes' slots", maxRunnersMode(a))
	}
	return nil
}

// sortedEnv is a settings map as env, in key order.
func sortedEnv(settings map[string]string) corev1.EnvVarArray {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := corev1.EnvVarArray{}
	for _, k := range keys {
		out = append(out, &corev1.EnvVarArgs{Name: pulumi.String(k), Value: pulumi.String(settings[k])})
	}
	return out
}

// gateAgentCapacityEnv is what the agent's Deployment gets for the probes and the live maxRunners.
func gateAgentCapacityEnv(a GateAgentArgs, namespace pulumi.StringInput) corev1.EnvVarArray {
	out := corev1.EnvVarArray{}
	if probeEnabled(a) {
		out = append(out, &corev1.EnvVarArgs{Name: pulumi.String("PROBE_NAMESPACE"), Value: namespace})
	}
	if mode := maxRunnersMode(a); mode != "off" {
		out = append(out, &corev1.EnvVarArgs{Name: pulumi.String("AGENT_MAX_RUNNERS"), Value: pulumi.String(mode)})
		out = append(out, sortedEnv(a.MaxRunnersSettings)...)
	}
	return out
}

func gateProbeLabels() pulumi.StringMap {
	return pulumi.StringMap{gateProbeLabelKey: pulumi.String(gateProbeName)}
}

// gateProbeDaemonSet is the probe on every node, whatever its taints: a node without one has no slots.
func gateProbeDaemonSet(a GateAgentArgs, namespace pulumi.StringInput) *appsv1.DaemonSetArgs {
	slots := "dry-run"
	if manageSlots(a) {
		slots = "apply"
	}
	env := corev1.EnvVarArray{
		&corev1.EnvVarArgs{Name: pulumi.String("NODE_NAME"), ValueFrom: &corev1.EnvVarSourceArgs{
			FieldRef: &corev1.ObjectFieldSelectorArgs{FieldPath: pulumi.String("spec.nodeName")}}},
		&corev1.EnvVarArgs{Name: pulumi.String("LISTENER_NAMESPACE"), Value: stringOr(a.ListenerNamespace, defaultGateAgentNamespace)},
		&corev1.EnvVarArgs{Name: pulumi.String("PROBE_SLOTS"), Value: pulumi.String(slots)},
		&corev1.EnvVarArgs{Name: pulumi.String("PROBE_EVICT"), Value: pulumi.String(probeEvict(a))},
	}
	if a.Probe != nil {
		env = append(env, sortedEnv(a.Probe.Settings)...)
	}
	labels := gateProbeLabels()
	meta := gateAgentMetadata(gateProbeName, namespace)
	meta.Labels = labels
	return &appsv1.DaemonSetArgs{
		Metadata: meta,
		Spec: &appsv1.DaemonSetSpecArgs{
			Selector: &metav1.LabelSelectorArgs{MatchLabels: labels},
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{Labels: labels},
				Spec: &corev1.PodSpecArgs{
					ServiceAccountName:           pulumi.String(gateProbeName),
					AutomountServiceAccountToken: pulumi.Bool(true),
					ImagePullSecrets: corev1.LocalObjectReferenceArray{
						&corev1.LocalObjectReferenceArgs{Name: pulumi.String(gateAgentPullSecretName)},
					},
					Tolerations: corev1.TolerationArray{&corev1.TolerationArgs{Operator: pulumi.String("Exists")}},
					Containers: corev1.ContainerArray{&corev1.ContainerArgs{
						Name:  pulumi.String("probe"),
						Image: a.Image,
						Args:  pulumi.ToStringArray([]string{"probe"}),
						Env:   env,
						Ports: corev1.ContainerPortArray{
							&corev1.ContainerPortArgs{Name: pulumi.String("metrics"), ContainerPort: pulumi.Int(gateProbePort)},
						},
						Resources: &corev1.ResourceRequirementsArgs{
							Requests: pulumi.ToStringMap(map[string]string{"cpu": "10m", "memory": "16Mi"}),
						},
						SecurityContext: &corev1.SecurityContextArgs{
							RunAsNonRoot:             pulumi.Bool(true),
							RunAsUser:                pulumi.Int(65534),
							AllowPrivilegeEscalation: pulumi.Bool(false),
							ReadOnlyRootFilesystem:   pulumi.Bool(true),
							Capabilities:             &corev1.CapabilitiesArgs{Drop: pulumi.ToStringArray([]string{"ALL"})},
							SeccompProfile:           &corev1.SeccompProfileArgs{Type: pulumi.String("RuntimeDefault")},
						},
					}},
				},
			},
		},
	}
}

// gateProbeRoles are the probe's Roles: list pods in the listener namespace and in each runner
// namespace, and, with evict on, delete pods in the runner namespaces only.
func gateProbeRoles(a GateAgentArgs, probeNamespace pulumi.StringInput, runners resolved[[]string]) []gateAgentRoleBinding {
	pair := func(suffix string, namespace pulumi.StringInput, rules rbacv1.PolicyRuleArray, deps []pulumi.Resource) gateAgentRoleBinding {
		return gateAgentRoleBinding{
			suffix: suffix,
			role:   &rbacv1.RoleArgs{Metadata: gateAgentMetadata(gateProbeName, namespace), Rules: rules},
			binding: &rbacv1.RoleBindingArgs{
				Metadata: gateAgentMetadata(gateProbeName, namespace),
				RoleRef:  gateAgentRoleRef("Role", gateProbeName),
				Subjects: rbacv1.SubjectArray{&rbacv1.SubjectArgs{Kind: pulumi.String("ServiceAccount"),
					Name: pulumi.String(gateProbeName), Namespace: probeNamespace}},
			},
			deps: deps,
		}
	}
	out := []gateAgentRoleBinding{pair("probe-listener", stringOr(a.ListenerNamespace, defaultGateAgentNamespace),
		rbacv1.PolicyRuleArray{gateAgentListRule("", "pods")}, nil)}
	seen := map[string]bool{}
	for _, namespace := range runners.Value {
		if seen[namespace] {
			continue
		}
		seen[namespace] = true
		var ns pulumi.StringInput = pulumi.String(namespace)
		if runners.Secret {
			ns = pulumi.ToSecret(ns).(pulumi.StringOutput)
		}
		rules := rbacv1.PolicyRuleArray{gateAgentListRule("", "pods")}
		if probeEvict(a) == "on" {
			rules = append(rules, &rbacv1.PolicyRuleArgs{
				ApiGroups: pulumi.ToStringArray([]string{""}),
				Resources: pulumi.ToStringArray([]string{"pods"}),
				Verbs:     pulumi.ToStringArray([]string{"delete"}),
			})
		}
		out = append(out, pair("probe-runner-"+namespace, ns, rules, runners.Deps))
	}
	return out
}

// gateProbeClusterRole: get nodes (its own node's published slots) and, with manageSlots, patch
// nodes/status. Not nodes itself, so the probe cannot touch a node's spec, labels or taints.
func gateProbeClusterRole(a GateAgentArgs, namespace pulumi.StringInput) (*rbacv1.ClusterRoleArgs, *rbacv1.ClusterRoleBindingArgs) {
	rules := rbacv1.PolicyRuleArray{&rbacv1.PolicyRuleArgs{
		ApiGroups: pulumi.ToStringArray([]string{""}),
		Resources: pulumi.ToStringArray([]string{"nodes"}),
		Verbs:     pulumi.ToStringArray([]string{"get"}),
	}}
	if manageSlots(a) {
		rules = append(rules, &rbacv1.PolicyRuleArgs{
			ApiGroups: pulumi.ToStringArray([]string{""}),
			Resources: pulumi.ToStringArray([]string{"nodes/status"}),
			Verbs:     pulumi.ToStringArray([]string{"patch"}),
		})
	}
	return &rbacv1.ClusterRoleArgs{Metadata: gateAgentMetadata(gateProbeName, nil), Rules: rules},
		&rbacv1.ClusterRoleBindingArgs{
			Metadata: gateAgentMetadata(gateProbeName, nil),
			RoleRef:  gateAgentRoleRef("ClusterRole", gateProbeName),
			Subjects: rbacv1.SubjectArray{&rbacv1.SubjectArgs{Kind: pulumi.String("ServiceAccount"),
				Name: pulumi.String(gateProbeName), Namespace: namespace}},
		}
}

// newGateProbe creates the probe's account, roles and DaemonSet.
func newGateProbe(ctx *pulumi.Context, name string, a GateAgentArgs, namespace pulumi.StringInput, runners resolved[[]string],
	pullSecret pulumi.Resource, child pulumi.ResourceOption) error {
	account, err := corev1.NewServiceAccount(ctx, name+"-probe-account", &corev1.ServiceAccountArgs{
		Metadata: gateAgentMetadata(gateProbeName, namespace),
	}, child)
	if err != nil {
		return err
	}
	deps := []pulumi.Resource{account, pullSecret}
	for _, pair := range gateProbeRoles(a, namespace, runners) {
		role, err := rbacv1.NewRole(ctx, name+"-"+pair.suffix+"-role", pair.role, child, pulumi.DependsOn(pair.deps))
		if err != nil {
			return err
		}
		binding, err := rbacv1.NewRoleBinding(ctx, name+"-"+pair.suffix+"-binding", pair.binding,
			child, pulumi.DependsOn(append([]pulumi.Resource{role, account}, pair.deps...)))
		if err != nil {
			return err
		}
		deps = append(deps, binding)
	}
	roleArgs, bindingArgs := gateProbeClusterRole(a, namespace)
	role, err := rbacv1.NewClusterRole(ctx, name+"-probe-node-role", roleArgs, child)
	if err != nil {
		return err
	}
	binding, err := rbacv1.NewClusterRoleBinding(ctx, name+"-probe-node-binding", bindingArgs,
		child, pulumi.DependsOn([]pulumi.Resource{role, account}))
	if err != nil {
		return err
	}
	deps = append(deps, binding)
	_, err = appsv1.NewDaemonSet(ctx, name+"-probe", gateProbeDaemonSet(a, namespace), child, pulumi.DependsOn(deps))
	return err
}
