package githubrunners

import (
	"reflect"
	"strings"
	"testing"
)

const rbacPrefix = "kubernetes:rbac.authorization.k8s.io/v1:"

// grants is each of a role's rules as "group/resource:verb,verb".
func grants(t *testing.T, r registered) []string {
	t.Helper()
	var out []string
	for _, value := range r.inputs["rules"].ArrayValue() {
		rule := obj(value)
		groups, resources := rule["apiGroups"].ArrayValue(), rule["resources"].ArrayValue()
		if len(groups) != 1 || len(resources) != 1 {
			t.Fatalf("%s: a rule over several groups or resources: %v", r.name, rule)
		}
		verbs := ""
		for i, v := range rule["verbs"].ArrayValue() {
			if i > 0 {
				verbs += ","
			}
			verbs += v.StringValue()
		}
		out = append(out, groups[0].StringValue()+"/"+resources[0].StringValue()+":"+verbs)
	}
	return out
}

func TestGateAgentPatchesNodeStatusOnlyWhenItManagesSlots(t *testing.T) {
	readOnly := []string{"/nodes:list", "metrics.k8s.io/nodes:list"}
	for _, manage := range []*bool{nil, bp(false)} {
		a := testGateAgentArgs()
		a.ManageSlots = manage
		got := grants(t, runGateAgent(t, a).find(t, rbacPrefix+"ClusterRole", "-node-role"))
		if !reflect.DeepEqual(got, readOnly) {
			t.Fatalf("manageSlots %v: cluster rules %v, want only %v", manage, got, readOnly)
		}
	}
	a := testGateAgentArgs()
	a.ManageSlots = bp(true)
	m := runGateAgent(t, a)
	// The node's status alone: not the node itself, whose spec holds its taints.
	want := append(readOnly, "/nodes/status:patch")
	if got := grants(t, m.find(t, rbacPrefix+"ClusterRole", "-node-role")); !reflect.DeepEqual(got, want) {
		t.Fatalf("cluster rules %v, want %v", got, want)
	}
	if m.count(rbacPrefix+"ClusterRole") != 1 {
		t.Fatal("slots need no second cluster role")
	}
	for _, r := range m.all {
		if r.typ == rbacPrefix+"Role" {
			for _, g := range grants(t, r) {
				if !strings.HasSuffix(g, ":list") {
					t.Fatalf("%s grants %s: slots need no namespaced write", r.name, g)
				}
			}
		}
	}
}

func TestGateAgentPatchesScaleSetsOnlyInRunnerNamespacesWhenItManagesMaxRunners(t *testing.T) {
	readOnly := []string{"/pods:list", "metrics.k8s.io/pods:list", "actions.github.com/ephemeralrunners:list"}
	for _, manage := range []*bool{nil, bp(false)} {
		a := testGateAgentArgs()
		a.ManageMaxRunners = manage
		got := grants(t, runGateAgent(t, a).find(t, rbacPrefix+"Role", "-runner-arc-runners-ryzen-role"))
		if !reflect.DeepEqual(got, readOnly) {
			t.Fatalf("manageMaxRunners %v: runner rules %v, want only %v", manage, got, readOnly)
		}
	}
	a := testGateAgentArgs()
	a.ListenerNamespace = sp("listeners")
	a.RunnerNamespaces = []string{"arc-runners-ryzen", "arc-runners-acme"}
	a.ManageMaxRunners = bp(true)
	m := runGateAgent(t, a)
	// list to read the ceiling annotation, patch to write the live value: nothing else, not
	// update or delete, and not the runner sets in any other namespace.
	want := append(readOnly, "actions.github.com/autoscalingrunnersets:list,patch")
	for _, ns := range a.RunnerNamespaces {
		if got := grants(t, m.find(t, rbacPrefix+"Role", "-runner-"+ns+"-role")); !reflect.DeepEqual(got, want) {
			t.Errorf("%s rules %v, want %v", ns, got, want)
		}
	}
	for _, kind := range []string{"Role", "ClusterRole"} {
		for _, r := range m.all {
			if r.typ != rbacPrefix+kind || strings.Contains(r.name, "-runner-") {
				continue
			}
			for _, g := range grants(t, r) {
				if strings.Contains(g, "autoscalingrunnersets") {
					t.Errorf("%s %s grants %s outside the runner namespaces", kind, r.name, g)
				}
			}
		}
	}
}
