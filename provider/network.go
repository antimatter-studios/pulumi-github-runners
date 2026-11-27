package githubrunners

// privateRanges are the addresses runner pods may not reach by default: the LAN, the cluster's own
// pod and service networks, carrier-grade NAT and link-local (which includes cloud metadata).
var privateRanges = []any{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16"}

// networkPolicySpec keeps runner pods to the internet, cluster DNS and the gate agent's claim
// endpoint, plus the pool's exceptions.
// A job is somebody's code, and the machine's neighbours are not its business. Containers a job
// starts through the podman sidecar share the pod's network namespace, so this covers them too.
func networkPolicySpec(n PoolNetworkPolicy) map[string]any {
	egress := []any{
		map[string]any{
			"to": []any{map[string]any{
				"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}},
				"podSelector":       map[string]any{"matchLabels": map[string]any{"k8s-app": "kube-dns"}},
			}},
			"ports": []any{
				map[string]any{"protocol": "UDP", "port": 53},
				map[string]any{"protocol": "TCP", "port": 53},
			},
		},
	}
	internet := map[string]any{"cidr": "0.0.0.0/0"}
	if n.BlockPrivate == nil || *n.BlockPrivate {
		internet["except"] = privateRanges
	}
	egress = append(egress, map[string]any{"to": []any{map[string]any{"ipBlock": internet}}})
	for _, allow := range n.Allow {
		peer := map[string]any{}
		if len(allow.NamespaceLabels) > 0 {
			peer["namespaceSelector"] = map[string]any{"matchLabels": stringMap(allow.NamespaceLabels)}
		}
		if len(allow.PodLabels) > 0 {
			peer["podSelector"] = map[string]any{"matchLabels": stringMap(allow.PodLabels)}
		}
		rule := map[string]any{"to": []any{peer}}
		if len(allow.Ports) > 0 {
			ports := make([]any, len(allow.Ports))
			for i, p := range allow.Ports {
				ports[i] = map[string]any{"protocol": "TCP", "port": p}
			}
			rule["ports"] = ports
		}
		egress = append(egress, rule)
	}
	// The job-started hook claims its job from the gate agent, whose addresses are private. A
	// namespaceSelector as well, because a podSelector alone looks only in the runners' own namespace.
	egress = append(egress, map[string]any{
		"to": []any{map[string]any{
			"namespaceSelector": map[string]any{"matchLabels": map[string]any{
				"kubernetes.io/metadata.name": orDefault(n.GateAgentNamespace, defaultGateAgentNamespace),
			}},
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": gateAgentName}},
		}},
		"ports": []any{map[string]any{"protocol": "TCP", "port": gateAgentPort}},
	})
	return map[string]any{
		"podSelector": map[string]any{},
		"policyTypes": []any{"Ingress", "Egress"},
		"ingress":     []any{},
		"egress":      egress,
	}
}

func stringMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
