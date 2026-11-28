package githubrunners

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
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

func TestTheProbePatchesNodeStatusOnlyWhenItManagesSlots(t *testing.T) {
	// The agent's role by its whole name: "-node-role" alone also matches the probe's
	// "homelab-probe-node-role", and which one find returned depended on registration order.
	agentReadOnly := []string{"/nodes:list", "metrics.k8s.io/nodes:list"}
	probeReadOnly := []string{"/nodes:get", "actions.github.com/autoscalingrunnersets:list"}
	for _, manage := range []*bool{nil, bp(false)} {
		a := testGateAgentArgs()
		a.ManageSlots = manage
		a.Probe = &GateAgentProbe{Enabled: bp(true)}
		m := runGateAgent(t, a)
		if got := grants(t, m.find(t, rbacPrefix+"ClusterRole", "homelab-node-role")); !reflect.DeepEqual(got, agentReadOnly) {
			t.Fatalf("manageSlots %v: agent cluster rules %v, want only %v", manage, got, agentReadOnly)
		}
		if got := grants(t, m.find(t, rbacPrefix+"ClusterRole", "-probe-node-role")); !reflect.DeepEqual(got, probeReadOnly) {
			t.Fatalf("manageSlots %v: probe cluster rules %v", manage, got)
		}
	}
	a := testGateAgentArgs()
	a.ManageSlots = bp(true)
	a.Probe = &GateAgentProbe{Enabled: bp(true)}
	m := runGateAgent(t, a)
	// The node's status alone: not the node itself, whose spec holds its taints; and the probe's, not the agent's.
	if got := grants(t, m.find(t, rbacPrefix+"ClusterRole", "-probe-node-role")); !reflect.DeepEqual(got, append(probeReadOnly, "/nodes/status:patch")) {
		t.Fatalf("probe cluster rules %v", got)
	}
	if got := grants(t, m.find(t, rbacPrefix+"ClusterRole", "homelab-node-role")); !reflect.DeepEqual(got, agentReadOnly) {
		t.Fatalf("agent cluster rules %v", got)
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
	b := testGateAgentArgs()
	b.ManageSlots = bp(true)
	if err := checkGateAgent(b); err == nil || !strings.Contains(err.Error(), "manageSlots needs probe.enabled") {
		t.Fatalf("manageSlots without the probe: %v", err)
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
	a.ListenerNamespace = pulumi.StringPtr("listeners")
	runnerNamespaces := []string{"arc-runners-ryzen", "arc-runners-acme"}
	a.RunnerNamespaces = pulumi.ToStringArray(runnerNamespaces)
	a.ManageMaxRunners = bp(true)
	a.Probe = &GateAgentProbe{Enabled: bp(true)}
	m := runGateAgent(t, a)
	// list to find the marked pools, patch to write the live value: nothing else, not update or
	// delete, and not the runner sets in any other namespace.
	want := append(readOnly, "actions.github.com/autoscalingrunnersets:list,patch")
	for _, ns := range runnerNamespaces {
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
				// The probe lists them everywhere, to bound its node's slots; it writes none.
				if strings.Contains(g, "autoscalingrunnersets") && !(strings.HasSuffix(r.name, "-probe-node-role") && g == "actions.github.com/autoscalingrunnersets:list") {
					t.Errorf("%s %s grants %s outside the runner namespaces", kind, r.name, g)
				}
			}
		}
	}
}

func envOf(t *testing.T, r registered, container string) map[string]string {
	t.Helper()
	out := map[string]string{}
	spec := obj(obj(obj(r.inputs["spec"])["template"])["spec"])
	for _, c := range spec["containers"].ArrayValue() {
		if obj(c)["name"].StringValue() != container {
			continue
		}
		for _, e := range obj(c)["env"].ArrayValue() {
			v := obj(e)["value"]
			if v.IsString() {
				out[obj(e)["name"].StringValue()] = v.StringValue()
			} else {
				out[obj(e)["name"].StringValue()] = "<from>"
			}
		}
	}
	return out
}

func TestTheProbeIsOffByDefault(t *testing.T) {
	m := runGateAgent(t, testGateAgentArgs())
	if m.count("kubernetes:apps/v1:DaemonSet") != 0 {
		t.Fatal("a probe without probe.enabled")
	}
	env := envOf(t, m.find(t, "kubernetes:apps/v1:Deployment", "-agent"), "agent")
	if _, ok := env["PROBE_NAMESPACE"]; ok {
		t.Fatal("PROBE_NAMESPACE without a probe")
	}
	if _, ok := env["AGENT_MAX_RUNNERS"]; ok {
		t.Fatal("AGENT_MAX_RUNNERS while off")
	}
}

func TestTheProbeRunsOnEveryNodeInDryRunWithItsSettings(t *testing.T) {
	a := testGateAgentArgs()
	a.Probe = &GateAgentProbe{Enabled: bp(true), Settings: map[string]string{"PROBE_PSI_MEMORY_SOME_HIGH": "15", "PROBE_INCREASE_STEP": "2"}}
	a.MaxRunnersMode = sp("dry-run")
	a.MaxRunnersSettings = map[string]string{"AGENT_MAX_RUNNERS_INTERVAL": "2m"}
	m := runGateAgent(t, a)
	ds := m.find(t, "kubernetes:apps/v1:DaemonSet", "-probe")
	env := envOf(t, ds, "probe")
	want := map[string]string{"NODE_NAME": "<from>", "LISTENER_NAMESPACE": "arc-systems", "PROBE_SLOTS": "dry-run",
		"PROBE_EVICT": "dry-run", "PROBE_PSI_MEMORY_SOME_HIGH": "15", "PROBE_INCREASE_STEP": "2"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("probe env %v, want %v", env, want)
	}
	spec := obj(obj(obj(ds.inputs["spec"])["template"])["spec"])
	if tol := spec["tolerations"].ArrayValue(); len(tol) != 1 || obj(tol[0])["operator"].StringValue() != "Exists" {
		t.Fatalf("tolerations %v", tol)
	}
	if spec["serviceAccountName"].StringValue() != "runner-gate-probe" {
		t.Fatal(spec["serviceAccountName"])
	}
	agent := envOf(t, m.find(t, "kubernetes:apps/v1:Deployment", "-agent"), "agent")
	if agent["PROBE_NAMESPACE"] != "arc-systems" || agent["AGENT_MAX_RUNNERS"] != "dry-run" || agent["AGENT_MAX_RUNNERS_INTERVAL"] != "2m" {
		t.Fatalf("agent env %v", agent)
	}
	// Dry run: the probe and the agent write nothing; the agent lists the scale sets.
	for _, r := range m.all {
		if r.typ == rbacPrefix+"Role" || r.typ == rbacPrefix+"ClusterRole" {
			for _, g := range grants(t, r) {
				if !strings.HasSuffix(g, ":list") && !strings.HasSuffix(g, ":get") {
					t.Fatalf("%s grants %s in dry run", r.name, g)
				}
			}
		}
	}
	got := grants(t, m.find(t, rbacPrefix+"Role", "-runner-arc-runners-ryzen-role"))
	if got[len(got)-1] != "actions.github.com/autoscalingrunnersets:list" {
		t.Fatalf("agent runner rules %v", got)
	}
	if got := grants(t, m.find(t, rbacPrefix+"Role", "-probe-runner-arc-runners-ryzen-role")); !reflect.DeepEqual(got, []string{"/pods:list"}) {
		t.Fatalf("probe runner rules %v", got)
	}
}

func TestTheProbeDeletesRunnerPodsOnlyWithEvictOn(t *testing.T) {
	a := testGateAgentArgs()
	a.Probe = &GateAgentProbe{Enabled: bp(true), Evict: sp("on")}
	m := runGateAgent(t, a)
	if got := grants(t, m.find(t, rbacPrefix+"Role", "-probe-runner-arc-runners-ryzen-role")); !reflect.DeepEqual(got, []string{"/pods:list", "/pods:delete"}) {
		t.Fatalf("probe runner rules %v", got)
	}
	if got := grants(t, m.find(t, rbacPrefix+"Role", "-probe-listener-role")); !reflect.DeepEqual(got, []string{"/pods:list"}) {
		t.Fatalf("never outside the runner namespaces: %v", got)
	}
	if envOf(t, m.find(t, "kubernetes:apps/v1:DaemonSet", "-probe"), "probe")["PROBE_EVICT"] != "on" {
		t.Fatal("PROBE_EVICT")
	}
}

func TestCapacitySettingsAreChecked(t *testing.T) {
	cases := []struct {
		set  func(*GateAgentArgs)
		want string
	}{
		{func(a *GateAgentArgs) { a.Probe = &GateAgentProbe{Enabled: bp(true), Evict: sp("yes")} }, "probe.evict"},
		{func(a *GateAgentArgs) {
			a.Probe = &GateAgentProbe{Enabled: bp(true), Settings: map[string]string{"CEILING": "4"}}
		}, "not a PROBE_* setting"},
		{func(a *GateAgentArgs) {
			a.Probe = &GateAgentProbe{Enabled: bp(true), Settings: map[string]string{"PROBE_SLOTS": "apply"}}
		}, "set by the component"},
		{func(a *GateAgentArgs) { a.Probe = &GateAgentProbe{Enabled: bp(true)}; a.MaxRunnersMode = sp("apply") }, "needs manageMaxRunners"},
		{func(a *GateAgentArgs) { a.MaxRunnersMode = sp("dry-run") }, "needs probe.enabled"},
		{func(a *GateAgentArgs) { a.Probe = &GateAgentProbe{Enabled: bp(true)}; a.MaxRunnersMode = sp("on") }, "must be off, dry-run or apply"},
		{func(a *GateAgentArgs) {
			a.Probe = &GateAgentProbe{Enabled: bp(true)}
			a.MaxRunnersMode = sp("dry-run")
			a.MaxRunnersSettings = map[string]string{"AGENT_MAX_RUNNERS": "apply"}
		}, "AGENT_MAX_RUNNERS_STEP or _INTERVAL"},
		// No counts or caps: the bounds come from the hardware, the settings are how the loop reacts.
		{func(a *GateAgentArgs) {
			a.Probe = &GateAgentProbe{Enabled: bp(true)}
			a.MaxRunnersMode = sp("dry-run")
			a.MaxRunnersSettings = map[string]string{"AGENT_MAX_RUNNERS_FLOOR": "1"}
		}, "no counts or caps"},
		{func(a *GateAgentArgs) {
			a.Probe = &GateAgentProbe{Enabled: bp(true), Settings: map[string]string{"PROBE_CEILING": "8"}}
		}, "fixed slot count"},
		{func(a *GateAgentArgs) {
			a.Probe = &GateAgentProbe{Enabled: bp(true), Settings: map[string]string{"PROBE_FLOOR": "1"}}
		}, "fixed slot count"},
	}
	for _, c := range cases {
		a := testGateAgentArgs()
		c.set(&a)
		if err := checkGateAgent(a); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
	a := testGateAgentArgs()
	a.ManageMaxRunners = bp(true)
	a.Probe = &GateAgentProbe{Enabled: bp(true)}
	if err := checkGateAgent(a); err != nil || maxRunnersMode(a) != "apply" {
		t.Fatalf("manageMaxRunners alone means apply: %v %s", err, maxRunnersMode(a))
	}
}

// The agent patches the scale sets only in apply mode: manageMaxRunners with a dry run lists them.
func TestTheAgentPatchesScaleSetsOnlyInApplyMode(t *testing.T) {
	for mode, want := range map[string]string{"dry-run": "actions.github.com/autoscalingrunnersets:list",
		"apply": "actions.github.com/autoscalingrunnersets:list,patch"} {
		a := testGateAgentArgs()
		a.ManageMaxRunners = bp(true)
		a.MaxRunnersMode = sp(mode)
		a.Probe = &GateAgentProbe{Enabled: bp(true)}
		m := runGateAgent(t, a)
		got := grants(t, m.find(t, rbacPrefix+"Role", "-runner-arc-runners-ryzen-role"))
		if got[len(got)-1] != want {
			t.Errorf("%s: agent runner rules %v, want %s last", mode, got, want)
		}
		if got := grants(t, m.find(t, rbacPrefix+"ClusterRole", "-probe-node-role")); !reflect.DeepEqual(got,
			[]string{"/nodes:get", "actions.github.com/autoscalingrunnersets:list"}) {
			t.Errorf("%s: probe cluster rules %v: it lists the scale sets cluster-wide and writes none", mode, got)
		}
	}
}
