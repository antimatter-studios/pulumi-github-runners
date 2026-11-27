package githubrunners

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
)

const (
	testGateAgentToken    = "unique-agent-token-test-value"
	testGateAgentPassword = "registry-password-with:\"quotes\""
	testGateAgentImage    = "git.antimatter-studios.com/antimatter-studios/antimatter-vps-gate-webservice:0123456789abcdef"
)

func testGateAgentArgs() GateAgentArgs {
	return GateAgentArgs{
		Cluster:          "homelab",
		Token:            pulumi.String(testGateAgentToken),
		Image:            testGateAgentImage,
		RunnerNamespaces: []string{"arc-runners-ryzen"},
		ImagePull: GateAgentImagePull{
			Username: "agent",
			Password: pulumi.String(testGateAgentPassword),
		},
	}
}

func TestCheckGateAgent(t *testing.T) {
	if err := checkGateAgent(testGateAgentArgs()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*GateAgentArgs)
	}{
		{"cluster", func(a *GateAgentArgs) { a.Cluster = "" }},
		{"token", func(a *GateAgentArgs) { a.Token = nil }},
		{"image", func(a *GateAgentArgs) { a.Image = "" }},
		{"image tag", func(a *GateAgentArgs) { a.Image = gateAgentImageRepository + ":latest" }},
		{"runnerNamespaces", func(a *GateAgentArgs) { a.RunnerNamespaces = nil }},
		{"empty runner namespace", func(a *GateAgentArgs) { a.RunnerNamespaces = []string{""} }},
		{"imagePull username", func(a *GateAgentArgs) { a.ImagePull.Username = "" }},
		{"imagePull password", func(a *GateAgentArgs) { a.ImagePull.Password = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testGateAgentArgs()
			tc.edit(&a)
			if err := checkGateAgent(a); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

func TestGateAgentDockerConfig(t *testing.T) {
	raw, err := gateAgentDockerConfig("registry.example:5000", "agent", testGateAgentPassword)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"auths":{"registry.example:5000":{"auth":"` +
		base64.StdEncoding.EncodeToString([]byte("agent:"+testGateAgentPassword)) + `"}}}`
	if raw != want {
		t.Fatal("the pull config must contain the registry's Docker auth credential")
	}
}

func TestAnInvalidGateAgentCreatesNothing(t *testing.T) {
	a := testGateAgentArgs()
	a.RunnerNamespaces = nil
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewGateAgent(ctx, "homelab", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err == nil || !strings.Contains(err.Error(), "runnerNamespaces") || len(m.all) != 0 {
		t.Fatalf("err = %v, registered %d resources", err, len(m.all))
	}
}

func TestGateAgentDeploymentDefaults(t *testing.T) {
	a := testGateAgentArgs()
	args := gateAgentDeployment(a, "arc-systems")
	spec := args.Spec.(*appsv1.DeploymentSpecArgs)
	containers := spec.Template.(*corev1.PodTemplateSpecArgs).Spec.(*corev1.PodSpecArgs).Containers.(corev1.ContainerArray)
	env := containers[0].(*corev1.ContainerArgs).Env.(corev1.EnvVarArray)
	want := map[string]pulumi.StringPtrInput{
		"GATE_AGENT_URL":     pulumi.String("https://runner-gate.antimatter-studios.com"),
		"GATE_AGENT_CLUSTER": pulumi.String("homelab"),
		"GATE_AGENT_TOKEN":   nil,
		"LISTENER_NAMESPACE": pulumi.String("arc-systems"),
		"METRICS_PORT":       pulumi.String("8080"),
	}
	got := make(map[string]pulumi.StringPtrInput, len(env))
	for _, input := range env {
		entry := input.(*corev1.EnvVarArgs)
		got[string(entry.Name.(pulumi.String))] = entry.Value
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default environment = %v, want %v", got, want)
	}
}

func TestGateAgent(t *testing.T) {
	a := testGateAgentArgs()
	a.Namespace = sp("gate")
	a.GateURL = sp("https://gate.example")
	a.ListenerNamespace = sp("listeners")
	a.RunnerNamespaces = []string{"arc-runners-ryzen", "listeners", "arc-runners-ryzen"}
	a.Interval = sp("10s")

	m := &mocks{}
	var agent *GateAgent
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		var err error
		agent, err = NewGateAgent(ctx, "homelab", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	out, err := internals.UnsafeAwaitOutput(t.Context(), agent.ClaimURL)
	if err != nil || !out.Known || out.Secret || out.Value != "http://runner-gate-agent.gate.svc:8080" {
		t.Fatalf("claimUrl = %+v, err = %v", out, err)
	}
	m.find(t, "github-runners:index:GateAgent", "homelab")

	token := m.find(t, "kubernetes:core/v1:Secret", "-token")
	checkGateAgentMetadata(t, token, "runner-gate-agent", "gate")
	if token.inputs["type"].StringValue() != "Opaque" || len(obj(token.inputs["stringData"])) != 1 {
		t.Fatal("agent token Secret must be Opaque and contain only its token")
	}
	checkSecretString(t, token.inputs["stringData"], "token", testGateAgentToken)
	pull := m.find(t, "kubernetes:core/v1:Secret", "-image-pull")
	checkGateAgentMetadata(t, pull, "runner-gate-agent-image-pull", "gate")
	config := pull.inputs["stringData"]
	if pull.inputs["type"].StringValue() != "kubernetes.io/dockerconfigjson" ||
		len(obj(config)) != 1 || !(config.IsSecret() || obj(config)[".dockerconfigjson"].IsSecret()) {
		t.Fatal("pull Secret must carry a secret .dockerconfigjson input")
	}
	var dockerConfig struct {
		Auths map[string]struct{ Auth string }
	}
	if err := json.Unmarshal([]byte(allStrings(obj(config)[".dockerconfigjson"])), &dockerConfig); err != nil {
		t.Fatal("pull Secret does not contain Docker config JSON")
	}
	auth := dockerConfig.Auths["git.antimatter-studios.com"].Auth
	if len(dockerConfig.Auths) != 1 || auth != base64.StdEncoding.EncodeToString([]byte("agent:"+testGateAgentPassword)) {
		t.Fatal("the pull Secret must authenticate to the default registry")
	}
	for _, r := range m.all {
		if r.name != token.name && strings.Contains(allStrings(resource.NewObjectProperty(r.inputs)), testGateAgentToken) {
			t.Fatalf("agent token leaked into %s %s", r.typ, r.name)
		}
	}
	checkGateAgentMetadata(t, m.find(t, "kubernetes:core/v1:ServiceAccount", "-account"), "runner-gate-agent", "gate")
	checkGateAgentWorkload(t, m)
	checkGateAgentRBAC(t, m)
}

func checkGateAgentMetadata(t *testing.T, r registered, name, namespace string) {
	t.Helper()
	meta := obj(r.inputs["metadata"])
	if meta["name"].StringValue() != name || (namespace == "" && meta.HasValue("namespace")) ||
		(namespace != "" && meta["namespace"].StringValue() != namespace) {
		t.Errorf("%s metadata = %v, want %s in %q", r.typ, meta, name, namespace)
	}
}

func checkGateAgentWorkload(t *testing.T, m *mocks) {
	t.Helper()
	deployment := m.find(t, "kubernetes:apps/v1:Deployment", "-agent")
	checkGateAgentMetadata(t, deployment, "runner-gate-agent", "gate")
	spec := obj(deployment.inputs["spec"])
	template := obj(spec["template"])
	labels := obj(obj(template["metadata"])["labels"])
	if spec["replicas"].NumberValue() != 1 || labels["app"].StringValue() != "runner-gate-agent" ||
		!reflect.DeepEqual(labels, obj(obj(spec["selector"])["matchLabels"])) {
		t.Fatal("agent Deployment must select one replica with the agent label")
	}
	pod := obj(template["spec"])
	if pod["serviceAccountName"].StringValue() != "runner-gate-agent" || !pod["automountServiceAccountToken"].BoolValue() ||
		obj(pod["imagePullSecrets"].ArrayValue()[0])["name"].StringValue() != "runner-gate-agent-image-pull" {
		t.Fatal("pod lacks its service account token or image pull Secret")
	}
	containers := pod["containers"].ArrayValue()
	container := obj(containers[0])
	if len(containers) != 1 || container["name"].StringValue() != "agent" || container["image"].StringValue() != testGateAgentImage ||
		container.HasValue("command") || allStrings(container["args"]) != "agent\n" ||
		obj(container["ports"].ArrayValue()[0])["containerPort"].NumberValue() != 8080 {
		t.Fatal("pod must run the agent image entrypoint in agent mode on port 8080")
	}
	resources := obj(container["resources"])
	requests := obj(resources["requests"])
	if requests["cpu"].StringValue() != "10m" || requests["memory"].StringValue() != "32Mi" ||
		resources.HasValue("limits") {
		t.Errorf("agent resources = %v", resources)
	}
	security := obj(container["securityContext"])
	if !security["runAsNonRoot"].BoolValue() ||
		!security["allowPrivilegeEscalation"].IsBool() || security["allowPrivilegeEscalation"].BoolValue() ||
		!security["readOnlyRootFilesystem"].BoolValue() || allStrings(obj(security["capabilities"])["drop"]) != "ALL\n" {
		t.Errorf("agent security context = %v", security)
	}

	env := map[string]resource.PropertyMap{}
	for _, item := range container["env"].ArrayValue() {
		entry := obj(item)
		env[entry["name"].StringValue()] = entry
	}
	for name, want := range map[string]string{
		"GATE_AGENT_URL":      "https://gate.example",
		"GATE_AGENT_CLUSTER":  "homelab",
		"GATE_AGENT_INTERVAL": "10s",
		"LISTENER_NAMESPACE":  "listeners",
		"METRICS_PORT":        "8080",
	} {
		if got := env[name]["value"]; !got.IsString() || got.StringValue() != want {
			t.Errorf("agent environment %s = %v, want %q", name, got, want)
		}
	}
	ref := env["GATE_AGENT_TOKEN"]
	keyRef := obj(obj(ref["valueFrom"])["secretKeyRef"])
	if ref.HasValue("value") || keyRef["name"].StringValue() != "runner-gate-agent" || keyRef["key"].StringValue() != "token" {
		t.Fatal("GATE_AGENT_TOKEN must come from the token Secret")
	}

	service := m.find(t, "kubernetes:core/v1:Service", "-service")
	checkGateAgentMetadata(t, service, "runner-gate-agent", "gate")
	serviceSpec := obj(service.inputs["spec"])
	port := obj(serviceSpec["ports"].ArrayValue()[0])
	if serviceSpec["type"].StringValue() != "ClusterIP" || !reflect.DeepEqual(labels, obj(serviceSpec["selector"])) ||
		port["port"].NumberValue() != 8080 || port["targetPort"].NumberValue() != 8080 {
		t.Error("Service must route port 8080 to the agent pod on port 8080")
	}
}

func checkGateAgentRBAC(t *testing.T, m *mocks) {
	t.Helper()
	const prefix = "kubernetes:rbac.authorization.k8s.io/v1:"
	if m.count(prefix+"Role") != 3 || m.count(prefix+"RoleBinding") != 3 ||
		m.count(prefix+"ClusterRole") != 1 || m.count(prefix+"ClusterRoleBinding") != 1 {
		t.Fatal("expected one Role and RoleBinding per listener and unique runner namespace, and one cluster pair")
	}
	for _, want := range []struct {
		suffix, name, namespace, kind string
		rules                         []string
	}{
		{"listener", "runner-gate-agent-listener", "listeners", "Role", []string{"/pods"}},
		{"runner-arc-runners-ryzen", "runner-gate-agent-runner", "arc-runners-ryzen", "Role", []string{"/pods", "metrics.k8s.io/pods", "actions.github.com/ephemeralrunners"}},
		{"runner-listeners", "runner-gate-agent-runner", "listeners", "Role", []string{"/pods", "metrics.k8s.io/pods", "actions.github.com/ephemeralrunners"}},
		{"node", "runner-gate-agent", "", "ClusterRole", []string{"/nodes", "metrics.k8s.io/nodes"}},
	} {
		role := m.find(t, prefix+want.kind, "-"+want.suffix+"-role")
		checkGateAgentMetadata(t, role, want.name, want.namespace)
		var got []string
		for _, value := range role.inputs["rules"].ArrayValue() {
			rule := obj(value)
			groups, resources := rule["apiGroups"].ArrayValue(), rule["resources"].ArrayValue()
			if len(groups) != 1 || len(resources) != 1 || allStrings(rule["verbs"]) != "list\n" {
				t.Fatalf("%s grants an unexpected rule: %v", role.name, rule)
			}
			got = append(got, groups[0].StringValue()+"/"+resources[0].StringValue())
		}
		if !reflect.DeepEqual(got, want.rules) {
			t.Errorf("%s rules = %v, want %v", role.name, got, want.rules)
		}

		binding := m.find(t, prefix+want.kind+"Binding", "-"+want.suffix+"-binding")
		checkGateAgentMetadata(t, binding, want.name, want.namespace)
		ref := obj(binding.inputs["roleRef"])
		subjects := binding.inputs["subjects"].ArrayValue()
		subject := obj(subjects[0])
		if ref["apiGroup"].StringValue() != "rbac.authorization.k8s.io" ||
			ref["kind"].StringValue() != want.kind || ref["name"].StringValue() != want.name ||
			len(subjects) != 1 || subject["kind"].StringValue() != "ServiceAccount" ||
			subject["namespace"].StringValue() != "gate" || subject["name"].StringValue() != "runner-gate-agent" {
			t.Errorf("%s must bind its role to the agent ServiceAccount", binding.name)
		}
	}
}
