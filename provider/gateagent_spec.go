package githubrunners

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	networkingv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	rbacv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/rbac/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	gateAgentName             = "runner-gate-agent"
	gateAgentPullSecretName   = gateAgentName + "-image-pull"
	gateAgentTokenKey         = "token"
	gateAgentPort             = 8080
	defaultGateAgentNamespace = "arc-systems"
	defaultGateAgentRegistry  = "git.antimatter-studios.com"
	defaultGateAgentURL       = "https://runner-gate.antimatter-studios.com"
	gateAgentImageRepository  = defaultGateAgentRegistry + "/antimatter-studios/antimatter-vps-gate-webservice"
)

var gateAgentImageTag = regexp.MustCompile(`^[0-9a-fA-F]{16}$`)

func checkGateAgent(a GateAgentArgs) error {
	if strings.TrimSpace(a.Cluster) == "" {
		return errors.New("gate agent: cluster is required")
	}
	if a.Token == nil {
		return errors.New("gate agent: token is required")
	}
	if tag, ok := strings.CutPrefix(a.Image, gateAgentImageRepository+":"); !ok || !gateAgentImageTag.MatchString(tag) {
		return fmt.Errorf("gate agent: image must be %s:<16-hex tag>", gateAgentImageRepository)
	}
	if len(a.RunnerNamespaces) == 0 {
		return errors.New("gate agent: runnerNamespaces must contain at least one namespace")
	}
	for _, namespace := range a.RunnerNamespaces {
		if strings.TrimSpace(namespace) == "" {
			return errors.New("gate agent: runnerNamespaces must not contain an empty namespace")
		}
	}
	if strings.TrimSpace(a.ImagePull.Username) == "" || a.ImagePull.Password == nil {
		return errors.New("gate agent: imagePull.username and imagePull.password are required")
	}
	if a.NetworkPolicy != nil {
		for _, source := range a.NetworkPolicy.Allow {
			// Kubernetes refuses a peer that selects nothing.
			if len(source.NamespaceLabels) == 0 && len(source.PodLabels) == 0 {
				return errors.New("gate agent: every networkPolicy.allow entry needs namespaceLabels or podLabels")
			}
		}
	}
	return nil
}

// gateAgentPodLabels are what the Service and the ingress policy select the agent by, and what a
// Pool's egress policy lets its runners reach.
func gateAgentPodLabels() pulumi.StringMap {
	return pulumi.StringMap{"app": pulumi.String(gateAgentName)}
}

func gateAgentClaimURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s.svc:%d", gateAgentName, namespace, gateAgentPort)
}

func gateAgentMetadata(name, namespace string) *metav1.ObjectMetaArgs {
	meta := &metav1.ObjectMetaArgs{Name: pulumi.String(name)}
	if namespace != "" {
		meta.Namespace = pulumi.String(namespace)
	}
	return meta
}

func gateAgentTokenSecret(namespace string, token pulumi.StringInput) *corev1.SecretArgs {
	return &corev1.SecretArgs{
		Metadata: gateAgentMetadata(gateAgentName, namespace),
		Type:     pulumi.String("Opaque"),
		StringData: pulumi.StringMap{
			gateAgentTokenKey: pulumi.ToSecret(token).(pulumi.StringOutput),
		},
	}
}

func gateAgentDockerConfig(registry, username, password string) (string, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	raw, err := json.Marshal(map[string]any{
		"auths": map[string]map[string]string{registry: {"auth": auth}},
	})
	return string(raw), err
}

func gateAgentImagePullSecret(namespace string, a GateAgentImagePull) *corev1.SecretArgs {
	registry := orDefault(a.Registry, defaultGateAgentRegistry)
	config := pulumi.ToSecret(a.Password).(pulumi.StringOutput).ApplyT(func(password string) (string, error) {
		return gateAgentDockerConfig(registry, a.Username, password)
	}).(pulumi.StringOutput)
	return &corev1.SecretArgs{
		Metadata:   gateAgentMetadata(gateAgentPullSecretName, namespace),
		Type:       pulumi.String("kubernetes.io/dockerconfigjson"),
		StringData: pulumi.StringMap{".dockerconfigjson": config},
	}
}

func gateAgentDeployment(a GateAgentArgs, namespace string) *appsv1.DeploymentArgs {
	labels := gateAgentPodLabels()
	listenerNamespace := orDefault(a.ListenerNamespace, defaultGateAgentNamespace)
	env := corev1.EnvVarArray{
		&corev1.EnvVarArgs{Name: pulumi.String("GATE_AGENT_URL"), Value: pulumi.String(orDefault(a.GateURL, defaultGateAgentURL))},
		&corev1.EnvVarArgs{Name: pulumi.String("GATE_AGENT_CLUSTER"), Value: pulumi.String(a.Cluster)},
		&corev1.EnvVarArgs{
			Name: pulumi.String("GATE_AGENT_TOKEN"),
			ValueFrom: &corev1.EnvVarSourceArgs{
				SecretKeyRef: &corev1.SecretKeySelectorArgs{
					Name: pulumi.String(gateAgentName),
					Key:  pulumi.String(gateAgentTokenKey),
				},
			},
		},
		&corev1.EnvVarArgs{Name: pulumi.String("LISTENER_NAMESPACE"), Value: pulumi.String(listenerNamespace)},
		&corev1.EnvVarArgs{Name: pulumi.String("METRICS_PORT"), Value: pulumi.String(strconv.Itoa(gateAgentPort))},
	}
	if interval := orDefault(a.Interval, ""); interval != "" {
		env = append(env, &corev1.EnvVarArgs{Name: pulumi.String("GATE_AGENT_INTERVAL"), Value: pulumi.String(interval)})
	}

	meta := gateAgentMetadata(gateAgentName, namespace)
	meta.Labels = labels
	return &appsv1.DeploymentArgs{
		Metadata: meta,
		Spec: &appsv1.DeploymentSpecArgs{
			Replicas: pulumi.Int(1),
			Selector: &metav1.LabelSelectorArgs{MatchLabels: labels},
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{Labels: labels},
				Spec: &corev1.PodSpecArgs{
					ServiceAccountName:           pulumi.String(gateAgentName),
					AutomountServiceAccountToken: pulumi.Bool(true),
					ImagePullSecrets: corev1.LocalObjectReferenceArray{
						&corev1.LocalObjectReferenceArgs{Name: pulumi.String(gateAgentPullSecretName)},
					},
					Containers: corev1.ContainerArray{&corev1.ContainerArgs{
						Name:  pulumi.String("agent"),
						Image: pulumi.String(a.Image),
						Args:  pulumi.ToStringArray([]string{"agent"}),
						Env:   env,
						Ports: corev1.ContainerPortArray{
							&corev1.ContainerPortArgs{ContainerPort: pulumi.Int(gateAgentPort)},
						},
						Resources: &corev1.ResourceRequirementsArgs{
							// Requests only, like runner pods: no CPU or memory limit by default.
							Requests: pulumi.ToStringMap(map[string]string{"cpu": "10m", "memory": "32Mi"}),
						},
						SecurityContext: &corev1.SecurityContextArgs{
							RunAsNonRoot:             pulumi.Bool(true),
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

func gateAgentService(namespace string) *corev1.ServiceArgs {
	return &corev1.ServiceArgs{
		Metadata: gateAgentMetadata(gateAgentName, namespace),
		Spec: &corev1.ServiceSpecArgs{
			Type:     pulumi.String("ClusterIP"),
			Selector: gateAgentPodLabels(),
			Ports: corev1.ServicePortArray{
				&corev1.ServicePortArgs{Port: pulumi.Int(gateAgentPort), TargetPort: pulumi.Int(gateAgentPort)},
			},
		},
	}
}

// gateAgentNetworkPolicy admits runner namespaces, and the listed extra sources, to the agent's
// port. The agent forwards a claim with its own token, so whoever reaches /claim acts with it. The
// policy is ingress only: the agent still reaches the gate and the API server.
func gateAgentNetworkPolicy(a GateAgentArgs, namespace string) *networkingv1.NetworkPolicyArgs {
	// By the label every Pool puts on its namespace, so a new pool needs no change here.
	from := networkingv1.NetworkPolicyPeerArray{&networkingv1.NetworkPolicyPeerArgs{
		NamespaceSelector: &metav1.LabelSelectorArgs{
			MatchExpressions: metav1.LabelSelectorRequirementArray{&metav1.LabelSelectorRequirementArgs{
				Key:      pulumi.String(poolNamespaceLabel),
				Operator: pulumi.String("Exists"),
			}},
		},
	}}
	if a.NetworkPolicy != nil {
		for _, source := range a.NetworkPolicy.Allow {
			peer := &networkingv1.NetworkPolicyPeerArgs{}
			if len(source.NamespaceLabels) > 0 {
				peer.NamespaceSelector = &metav1.LabelSelectorArgs{MatchLabels: pulumi.ToStringMap(source.NamespaceLabels)}
			}
			if len(source.PodLabels) > 0 {
				peer.PodSelector = &metav1.LabelSelectorArgs{MatchLabels: pulumi.ToStringMap(source.PodLabels)}
			}
			from = append(from, peer)
		}
	}
	return &networkingv1.NetworkPolicyArgs{
		Metadata: gateAgentMetadata(gateAgentName, namespace),
		Spec: &networkingv1.NetworkPolicySpecArgs{
			PodSelector: &metav1.LabelSelectorArgs{MatchLabels: gateAgentPodLabels()},
			PolicyTypes: pulumi.ToStringArray([]string{"Ingress"}),
			Ingress: networkingv1.NetworkPolicyIngressRuleArray{&networkingv1.NetworkPolicyIngressRuleArgs{
				From: from,
				Ports: networkingv1.NetworkPolicyPortArray{
					&networkingv1.NetworkPolicyPortArgs{Protocol: pulumi.String("TCP"), Port: pulumi.Int(gateAgentPort)},
				},
			}},
		},
	}
}

type gateAgentRoleBinding struct {
	suffix  string
	role    *rbacv1.RoleArgs
	binding *rbacv1.RoleBindingArgs
}

func gateAgentNamespacedRoles(a GateAgentArgs, agentNamespace string) []gateAgentRoleBinding {
	newPair := func(suffix, name, namespace string, rules rbacv1.PolicyRuleArray) gateAgentRoleBinding {
		return gateAgentRoleBinding{
			suffix: suffix,
			role: &rbacv1.RoleArgs{
				Metadata: gateAgentMetadata(name, namespace),
				Rules:    rules,
			},
			binding: &rbacv1.RoleBindingArgs{
				Metadata: gateAgentMetadata(name, namespace),
				RoleRef:  gateAgentRoleRef("Role", name),
				Subjects: gateAgentSubjects(agentNamespace),
			},
		}
	}
	listenerNamespace := orDefault(a.ListenerNamespace, defaultGateAgentNamespace)
	pairs := []gateAgentRoleBinding{
		newPair("listener", gateAgentName+"-listener", listenerNamespace, rbacv1.PolicyRuleArray{
			gateAgentListRule("", "pods"),
		}),
	}
	seen := map[string]bool{}
	for _, namespace := range a.RunnerNamespaces {
		if seen[namespace] {
			continue
		}
		seen[namespace] = true
		pairs = append(pairs, newPair("runner-"+namespace, gateAgentName+"-runner", namespace, rbacv1.PolicyRuleArray{
			gateAgentListRule("", "pods"),
			gateAgentListRule("metrics.k8s.io", "pods"),
			gateAgentListRule("actions.github.com", "ephemeralrunners"),
		}))
	}
	return pairs
}

func gateAgentClusterRole(namespace string) (*rbacv1.ClusterRoleArgs, *rbacv1.ClusterRoleBindingArgs) {
	return &rbacv1.ClusterRoleArgs{
		Metadata: gateAgentMetadata(gateAgentName, ""),
		Rules: rbacv1.PolicyRuleArray{
			gateAgentListRule("", "nodes"),
			gateAgentListRule("metrics.k8s.io", "nodes"),
		},
	}, &rbacv1.ClusterRoleBindingArgs{
		Metadata: gateAgentMetadata(gateAgentName, ""),
		RoleRef:  gateAgentRoleRef("ClusterRole", gateAgentName),
		Subjects: gateAgentSubjects(namespace),
	}
}

func gateAgentListRule(group, name string) *rbacv1.PolicyRuleArgs {
	return &rbacv1.PolicyRuleArgs{
		ApiGroups: pulumi.ToStringArray([]string{group}),
		Resources: pulumi.ToStringArray([]string{name}),
		Verbs:     pulumi.ToStringArray([]string{"list"}),
	}
}

func gateAgentRoleRef(kind, name string) *rbacv1.RoleRefArgs {
	return &rbacv1.RoleRefArgs{
		ApiGroup: pulumi.String("rbac.authorization.k8s.io"),
		Kind:     pulumi.String(kind),
		Name:     pulumi.String(name),
	}
}

func gateAgentSubjects(namespace string) rbacv1.SubjectArray {
	return rbacv1.SubjectArray{&rbacv1.SubjectArgs{
		Kind:      pulumi.String("ServiceAccount"),
		Name:      pulumi.String(gateAgentName),
		Namespace: pulumi.String(namespace),
	}}
}
