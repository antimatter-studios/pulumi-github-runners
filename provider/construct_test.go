package githubrunners

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// These tests drive the components the way a program in another language does: through the
// provider's Construct, with inputs that are Outputs (secrets, values with dependencies, unknowns in
// a preview). A plain Go field rejects an Output there ("copying input ...: expected destination
// type to implement pulumi.Input or pulumi.Output"), which a Go program calling NewGateAgent never
// sees, so only this path catches it.

// upstream stands for a resource the program created first, such as a Pool or a Controller.
const upstream = urn.URN("urn:pulumi:stack::project::github-runners:index:Pool::runners-acme")

func construct(t *testing.T, typ string, inputs map[string]property.Value, dryRun bool) (*mocks, p.ConstructResponse) {
	t.Helper()
	m := &mocks{}
	server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.0"),
		integration.WithProvider(Provider()), integration.WithMocks(m))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Construct(p.ConstructRequest{
		Urn:    resource.NewURN("stack", "project", "", tokens.Type("github-runners:index:"+typ), "under-test"),
		Inputs: property.NewMap(inputs),
		DryRun: dryRun,
	})
	if err != nil {
		t.Fatalf("construct %s: %v", typ, err)
	}
	return m, resp
}

func str(s string) property.Value { return property.New(s) }

func secret(v property.Value) property.Value { return v.WithSecret(true) }

// fromUpstream is a value another resource produced: an Output that carries its dependency.
func fromUpstream(v property.Value) property.Value { return v.WithDependencies([]urn.URN{upstream}) }

func strs(values ...string) property.Value {
	out := make([]property.Value, len(values))
	for i, v := range values {
		out[i] = str(v)
	}
	return property.New(out)
}

func gateAgentInputs() map[string]property.Value {
	return map[string]property.Value{
		"cluster": str("homelab"),
		"image":   str(testGateAgentImage),
		"token":   secret(str(testGateAgentToken)),
		// A whole credentials object read with getSecretObject: every field arrives secret.
		"imagePull": secret(property.New(map[string]property.Value{
			"username": str("agent"),
			"password": str(testGateAgentPassword),
		})),
		// The Pools' namespace outputs, gathered with pulumi.all: one Output for the whole list.
		"runnerNamespaces": fromUpstream(strs("arc-runners-acme", "arc-runners-other")),
		"namespace":        fromUpstream(str("arc-systems")),
	}
}

func TestGateAgentTakesOutputs(t *testing.T) {
	m, resp := construct(t, "GateAgent", gateAgentInputs(), false)

	for _, ns := range []string{"arc-runners-acme", "arc-runners-other"} {
		role := m.find(t, "kubernetes:rbac.authorization.k8s.io/v1:Role", "-runner-"+ns+"-role")
		if got := obj(role.inputs["metadata"])["namespace"]; allStrings(got) != ns+"\n" {
			t.Fatalf("runner role in %v, want %s", got, ns)
		}
	}

	token := m.find(t, "kubernetes:core/v1:Secret", "-token")
	checkSecretString(t, token.inputs["stringData"], "token", testGateAgentToken)

	pull := m.find(t, "kubernetes:core/v1:Secret", "-image-pull")
	config := obj(pull.inputs["stringData"])[".dockerconfigjson"]
	if !pull.inputs["stringData"].ContainsSecrets() {
		t.Fatalf("docker config is not secret: %v", config)
	}
	var parsed struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(allStrings(config), "\n")), &parsed); err != nil {
		t.Fatalf("docker config %v: %v", config, err)
	}
	auth, _ := base64.StdEncoding.DecodeString(parsed.Auths[defaultGateAgentRegistry].Auth)
	if string(auth) != "agent:"+testGateAgentPassword {
		t.Fatalf("docker config auth %q", auth)
	}

	if got := resp.State.Get("claimUrl"); !strings.Contains(stateString(got), ".arc-systems.svc:") {
		t.Fatalf("claimUrl %v", got)
	}
}

func TestGateAgentTakesOneOutputPerRunnerNamespace(t *testing.T) {
	inputs := gateAgentInputs()
	// pools.map((pool) => pool.namespace): a plain list of Outputs.
	inputs["runnerNamespaces"] = property.New([]property.Value{
		fromUpstream(str("arc-runners-acme")),
		fromUpstream(str("arc-runners-other")),
	})
	// Credentials with only the password secret, as a program wraps one field itself.
	inputs["imagePull"] = property.New(map[string]property.Value{
		"username": str("agent"),
		"password": secret(str(testGateAgentPassword)),
	})
	m, _ := construct(t, "GateAgent", inputs, false)
	m.find(t, "kubernetes:rbac.authorization.k8s.io/v1:Role", "-runner-arc-runners-acme-role")
	m.find(t, "kubernetes:rbac.authorization.k8s.io/v1:Role", "-runner-arc-runners-other-role")
}

func TestGateAgentPreviewsWithUnknownInputs(t *testing.T) {
	inputs := gateAgentInputs()
	// A preview before the Pools exist: their namespaces are not known yet.
	inputs["runnerNamespaces"] = fromUpstream(property.New(property.Computed))
	inputs["namespace"] = fromUpstream(property.New(property.Computed))
	inputs["image"] = fromUpstream(property.New(property.Computed))
	m, _ := construct(t, "GateAgent", inputs, true)
	m.find(t, "kubernetes:apps/v1:Deployment", "-agent")
	// Only the listener's Role: the runner namespaces' wait for the update, which knows them.
	if n := m.count("kubernetes:rbac.authorization.k8s.io/v1:Role"); n != 1 {
		t.Fatalf("%d Roles in a preview that does not know the runner namespaces", n)
	}
}

func TestPoolTakesOutputs(t *testing.T) {
	cases := map[string]property.Value{
		// The App's fields from a secret config object: all secret.
		"whole app secret": secret(property.New(map[string]property.Value{
			"appId":          str("123"),
			"installationId": str("456"),
			"privateKey":     str("-----BEGIN KEY-----"),
		})),
		// pulumi.secret around the key only.
		"private key secret": property.New(map[string]property.Value{
			"appId":          str("123"),
			"installationId": str("456"),
			"privateKey":     secret(str("-----BEGIN KEY-----")),
		}),
	}
	for name, app := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := construct(t, "Pool", map[string]property.Value{
				"poolId":       str("acme"),
				"configUrl":    str("https://github.com/acme"),
				"scaleSetName": str("ryzen-linux-x64"),
				"app":          app,
				"runnerGroup":  property.New(map[string]property.Value{"name": str("ryzen")}),
				"hook": property.New(map[string]property.Value{
					// GateAgent.claimUrl, an Output of another component.
					"env": property.New(map[string]property.Value{"RUNNER_GATE_URL": fromUpstream(str("http://runner-gate-agent.arc-systems.svc:8080"))}),
				}),
				"networkPolicy": property.New(map[string]property.Value{
					// Controller.namespace.
					"gateAgentNamespace": fromUpstream(str("arc-systems")),
				}),
			}, false)
			secretRes := m.find(t, "kubernetes:core/v1:Secret", "-github")
			checkSecretString(t, secretRes.inputs["stringData"], "github_app_private_key", "-----BEGIN KEY-----")
			if got := allStrings(obj(secretRes.inputs["stringData"])["github_app_id"]); got != "123\n" {
				t.Fatalf("github_app_id %q", got)
			}
			release := m.find(t, "kubernetes:helm.sh/v3:Release", "-scale-set")
			if !strings.Contains(allStrings(release.inputs["values"]), "http://runner-gate-agent.arc-systems.svc:8080") {
				t.Fatalf("hook env missing from values: %v", release.inputs["values"])
			}
			policy := m.find(t, testNetworkPolicyType, "-egress")
			if !strings.Contains(allStrings(policy.inputs["spec"]), "arc-systems") {
				t.Fatalf("gate agent namespace missing from policy: %v", policy.inputs["spec"])
			}
		})
	}
}

func TestPoolTakesASecretToken(t *testing.T) {
	m, _ := construct(t, "Pool", map[string]property.Value{
		"poolId":       str("acme"),
		"configUrl":    str("https://github.com/acme"),
		"scaleSetName": str("ryzen-linux-x64"),
		"token":        secret(fromUpstream(str("runner-pat"))),
	}, false)
	secretRes := m.find(t, "kubernetes:core/v1:Secret", "-github")
	checkSecretString(t, secretRes.inputs["stringData"], "github_token", "runner-pat")
}

// The claim endpoint is another component's Output: GateAgent.claimUrl, or the namespace a
// Controller or GateAgent was put in. It must reach the runner's environment, and the namespace the
// egress policy too.
func TestPoolTakesAGateClaimFromOutputs(t *testing.T) {
	cases := map[string]struct {
		claim     map[string]property.Value
		url       string
		namespace string
	}{
		"url": {
			claim:     map[string]property.Value{"url": fromUpstream(str("http://runner-gate-agent.gate.svc:8080"))},
			url:       "http://runner-gate-agent.gate.svc:8080",
			namespace: defaultGateAgentNamespace,
		},
		"secret url": {
			claim:     map[string]property.Value{"url": secret(str("http://gate.example:9000/"))},
			url:       "http://gate.example:9000\n",
			namespace: defaultGateAgentNamespace,
		},
		"agent namespace": {
			claim:     map[string]property.Value{"agentNamespace": fromUpstream(str("gate"))},
			url:       "http://runner-gate-agent.gate.svc:8080",
			namespace: "gate",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := construct(t, "Pool", map[string]property.Value{
				"poolId":        str("acme"),
				"configUrl":     str("https://github.com/acme"),
				"scaleSetName":  str("ryzen-linux-x64"),
				"token":         secret(str("runner-pat")),
				"gateClaim":     property.New(c.claim),
				"networkPolicy": property.New(map[string]property.Value{}),
			}, false)
			release := m.find(t, "kubernetes:helm.sh/v3:Release", "-scale-set")
			if got := allStrings(release.inputs["values"]); !strings.Contains(got, c.url) {
				t.Fatalf("claim URL %q missing from values: %v", c.url, got)
			}
			policy := m.find(t, testNetworkPolicyType, "-egress")
			if got := "\n" + allStrings(policy.inputs["spec"]); !strings.Contains(got, "\n"+c.namespace+"\n") {
				t.Fatalf("namespace %q missing from policy: %v", c.namespace, got)
			}
		})
	}
}

func TestPoolPreviewsWithAnUnknownGateClaim(t *testing.T) {
	for _, field := range []string{"url", "agentNamespace"} {
		t.Run(field, func(t *testing.T) {
			m, _ := construct(t, "Pool", map[string]property.Value{
				"poolId":        str("acme"),
				"configUrl":     str("https://github.com/acme"),
				"scaleSetName":  str("ryzen-linux-x64"),
				"token":         str("runner-pat"),
				"gateClaim":     property.New(map[string]property.Value{field: fromUpstream(property.New(property.Computed))}),
				"networkPolicy": property.New(map[string]property.Value{}),
			}, true)
			m.find(t, "kubernetes:helm.sh/v3:Release", "-scale-set")
		})
	}
}

func TestControllerMonitoringTakesOutputs(t *testing.T) {
	m, _ := construct(t, "Controller", map[string]property.Value{
		"monitoring": property.New(map[string]property.Value{
			"dashboard": property.New(map[string]property.Value{"namespace": fromUpstream(str("grafana"))}),
			"networkPolicy": property.New(map[string]property.Value{
				// GateAgent's or a Namespace's Output.
				"gateAgentNamespace": fromUpstream(str("gate")),
				"allow": property.New([]property.Value{property.New(map[string]property.Value{
					"podLabels": property.New(map[string]property.Value{"app": str("prometheus")}),
				})}),
			}),
		}),
	}, false)
	policy := m.find(t, testNetworkPolicyType, "-metrics-ingress")
	if got := "\n" + allStrings(policy.inputs["spec"]); !strings.Contains(got, "\ngate\n") {
		t.Fatalf("gate agent namespace missing from policy: %v", got)
	}
	dashboard := m.find(t, "kubernetes:core/v1:ConfigMap", "-dashboard")
	if got := allStrings(obj(dashboard.inputs["metadata"])["namespace"]); got != "grafana\n" {
		t.Fatalf("dashboard namespace %q", got)
	}
}

// stateString reads a string out of a component's state, through any secret or output wrapper.
func stateString(v property.Value) string {
	if v.IsString() {
		return v.AsString()
	}
	return ""
}
