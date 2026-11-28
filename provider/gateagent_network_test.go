package githubrunners

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const testNetworkPolicyType = "kubernetes:networking.k8s.io/v1:NetworkPolicy"

func runGateAgent(t *testing.T, a GateAgentArgs) *mocks {
	t.Helper()
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewGateAgent(ctx, "homelab", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// matchLabels reports whether a selector's matchLabels are all present on the object, which is how
// Kubernetes reads them. An empty selector would match everything, so it does not count.
func matchLabels(selector, labels resource.PropertyMap) bool {
	want := obj(selector["matchLabels"])
	for k, v := range want {
		if !reflect.DeepEqual(labels[k], v) {
			return false
		}
	}
	return len(want) > 0
}

func TestGateAgentAdmitsOnlyRunnerNamespacesByDefault(t *testing.T) {
	a := testGateAgentArgs()
	a.Namespace = pulumi.StringPtr("gate")
	m := runGateAgent(t, a)

	policy := m.find(t, testNetworkPolicyType, "-ingress")
	checkGateAgentMetadata(t, policy, "runner-gate-agent", "gate")
	spec := obj(policy.inputs["spec"])
	// Ingress only: an Egress type would cut the agent off from the gate and the API server.
	if allStrings(spec["policyTypes"]) != "Ingress\n" || spec.HasValue("egress") {
		t.Fatalf("policy must restrict ingress only: %v", spec)
	}
	rules := spec["ingress"].ArrayValue()
	if len(rules) != 1 {
		t.Fatalf("ingress rules = %v", rules)
	}
	rule := obj(rules[0])
	ports := rule["ports"].ArrayValue()
	if len(ports) != 1 || obj(ports[0])["protocol"].StringValue() != "TCP" || obj(ports[0])["port"].NumberValue() != 8080 {
		t.Fatalf("ports = %v, want TCP 8080", ports)
	}
	// An absent or empty `from` admits everybody.
	from := rule["from"].ArrayValue()
	if len(from) != 1 {
		t.Fatalf("from = %v, want only runner namespaces", from)
	}
	peer := obj(from[0])
	if len(peer) != 1 {
		t.Fatalf("peer = %v, want a namespaceSelector alone", peer)
	}
	selector := obj(peer["namespaceSelector"])
	expressions := selector["matchExpressions"].ArrayValue()
	if len(selector) != 1 || len(expressions) != 1 ||
		obj(expressions[0])["key"].StringValue() != "github-runners.antimatter.studios/pool" ||
		obj(expressions[0])["operator"].StringValue() != "Exists" {
		t.Fatalf("namespaceSelector = %v", selector)
	}
}

// The two components never see each other, so this is what keeps their selectors true: each one
// selects what the other really creates.
func TestPoolAndGateAgentPoliciesSelectEachOther(t *testing.T) {
	a := testGateAgentArgs()
	a.Namespace = pulumi.StringPtr("gate")
	pool := basePool()
	pool.Token = stringInput("runner-pat")
	pool.NetworkPolicy = &PoolNetworkPolicy{GateAgentNamespace: pulumi.StringPtr("gate")}
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		if _, err := NewPool(ctx, "acme", pool); err != nil {
			return err
		}
		_, err := NewGateAgent(ctx, "homelab", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}

	namespaceLabels := obj(obj(m.find(t, "kubernetes:core/v1:Namespace", "-ns").inputs["metadata"])["labels"])
	ingress := obj(m.find(t, testNetworkPolicyType, "-ingress").inputs["spec"])
	from := obj(obj(ingress["ingress"].ArrayValue()[0])["from"].ArrayValue()[0])
	key := obj(obj(from["namespaceSelector"])["matchExpressions"].ArrayValue()[0])["key"].StringValue()
	if got := namespaceLabels[resource.PropertyKey(key)]; !got.IsString() || got.StringValue() != "acme" {
		t.Fatalf("the Pool's namespace labels %v lack %s, which the agent admits", namespaceLabels, key)
	}

	deployment := m.find(t, "kubernetes:apps/v1:Deployment", "-agent")
	podLabels := obj(obj(obj(obj(deployment.inputs["spec"])["template"])["metadata"])["labels"])
	if !matchLabels(obj(ingress["podSelector"]), podLabels) {
		t.Fatalf("agent policy selects %v, its pods carry %v", ingress["podSelector"], podLabels)
	}

	agentNamespace := obj(deployment.inputs["metadata"])["namespace"].StringValue()
	// Kubernetes puts this label on every namespace.
	agentNamespaceLabels := resource.PropertyMap{"kubernetes.io/metadata.name": resource.NewStringProperty(agentNamespace)}
	egress := obj(m.find(t, testNetworkPolicyType, "-egress").inputs["spec"])["egress"].ArrayValue()
	found := false
	for _, item := range egress {
		rule := obj(item)
		peer := obj(rule["to"].ArrayValue()[0])
		if !peer.HasValue("namespaceSelector") || !peer.HasValue("podSelector") ||
			!matchLabels(obj(peer["namespaceSelector"]), agentNamespaceLabels) ||
			!matchLabels(obj(peer["podSelector"]), podLabels) {
			continue
		}
		ports := rule["ports"].ArrayValue()
		found = len(ports) == 1 && obj(ports[0])["protocol"].StringValue() == "TCP" && obj(ports[0])["port"].NumberValue() == 8080
	}
	if !found {
		t.Fatalf("no Pool egress rule reaches the agent pods on 8080: %v", egress)
	}
}

func TestGateAgentNetworkPolicyOptOut(t *testing.T) {
	a := testGateAgentArgs()
	a.NetworkPolicy = &GateAgentNetworkPolicy{Enabled: bp(false)}
	if n := runGateAgent(t, a).count(testNetworkPolicyType); n != 0 {
		t.Fatalf("enabled false created %d policies", n)
	}
	for _, setting := range []*GateAgentNetworkPolicy{{}, {Enabled: bp(true)}} {
		a.NetworkPolicy = setting
		if n := runGateAgent(t, a).count(testNetworkPolicyType); n != 1 {
			t.Fatalf("%+v created %d policies, want 1", setting, n)
		}
	}
}

func TestGateAgentExtraSources(t *testing.T) {
	a := testGateAgentArgs()
	a.NetworkPolicy = &GateAgentNetworkPolicy{Allow: []GateAgentSource{
		{NamespaceLabels: map[string]string{"kubernetes.io/metadata.name": "monitoring"}},
		{PodLabels: map[string]string{"app": "debug"}},
		{NamespaceLabels: map[string]string{"team": "ops"}, PodLabels: map[string]string{"app": "prometheus"}},
	}}
	spec := obj(runGateAgent(t, a).find(t, testNetworkPolicyType, "-ingress").inputs["spec"])
	rules := spec["ingress"].ArrayValue()
	from := obj(rules[0])["from"].ArrayValue()
	// One rule, so the extra sources are held to port 8080 as the runners are.
	if len(rules) != 1 || len(from) != 4 {
		t.Fatalf("ingress = %v, want one rule with four peers", rules)
	}
	for i, want := range []struct{ namespace, pod string }{
		{namespace: "monitoring\n"},
		{pod: "debug\n"},
		{namespace: "ops\n", pod: "prometheus\n"},
	} {
		peer := obj(from[i+1])
		for key, value := range map[resource.PropertyKey]string{"namespaceSelector": want.namespace, "podSelector": want.pod} {
			if value == "" {
				if peer.HasValue(key) {
					t.Errorf("peer %d must not set %s: %v", i, key, peer)
				}
			} else if !peer.HasValue(key) || allStrings(obj(peer[key])["matchLabels"]) != value {
				t.Errorf("peer %d %s = %v, want %q", i, key, peer[key], value)
			}
		}
	}
}

func TestCheckGateAgentRefusesAnEmptySource(t *testing.T) {
	a := testGateAgentArgs()
	a.NetworkPolicy = &GateAgentNetworkPolicy{Allow: []GateAgentSource{{PodLabels: map[string]string{}}}}
	if err := checkGateAgent(a); err == nil || !strings.Contains(err.Error(), "networkPolicy.allow") {
		t.Fatalf("err = %v, want a refusal naming networkPolicy.allow", err)
	}
}

func TestPoolNetworkPolicyReachesTheGateAgent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    PoolNetworkPolicy
		namespace string
	}{
		{"default", PoolNetworkPolicy{}, "arc-systems"},
		{"custom namespace", PoolNetworkPolicy{GateAgentNamespace: pulumi.StringPtr("gate")}, "gate"},
		// An ipBlock does not match pods under every network plugin, so the rule stays.
		{"private ranges open", PoolNetworkPolicy{BlockPrivate: bp(false)}, "arc-systems"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := networkPolicySpec(tc.policy)
			rule := get(t, spec, "egress", 2)
			if get(t, rule, "to", 0, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != tc.namespace ||
				get(t, rule, "to", 0, "podSelector", "matchLabels", "app") != "runner-gate-agent" ||
				get(t, rule, "ports", 0, "protocol") != "TCP" || get(t, rule, "ports", 0, "port") != float64(8080) {
				t.Fatalf("agent rule %v", rule)
			}
		})
	}
}
