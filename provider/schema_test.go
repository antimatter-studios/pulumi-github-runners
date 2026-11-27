package githubrunners

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/pkg/v3/codegen/schema"
)

// The plugin's schema is what the TypeScript SDK is generated from, so it has to build, name the
// package as consumers import it, and carry every resource token.
func TestSchemaNamesThePackageAndItsResources(t *testing.T) {
	server, err := integration.NewServer(context.Background(), Name, semver.MustParse("0.0.0"), integration.WithProvider(providerWith("http://unused")))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.GetSchema(p.GetSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Compared without whitespace: the server returns the schema compact, the CLI pretty-prints it.
	compact := strings.Join(strings.Fields(resp.Schema), "")
	for _, want := range []string{
		`"name":"github-runners"`,
		`"packageName":"@antimatter-studios/github-runners"`,
		`"github-runners:index:ForkPrApproval"`,
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("schema lacks %s", want)
		}
	}

	var spec schema.PackageSpec
	if err := json.Unmarshal([]byte(resp.Schema), &spec); err != nil {
		t.Fatal(err)
	}
	agent := spec.Resources["github-runners:index:GateAgent"]
	for _, name := range []string{"cluster", "token", "image", "runnerNamespaces", "imagePull"} {
		if !slices.Contains(agent.RequiredInputs, name) {
			t.Errorf("GateAgent schema must require %s", name)
		}
	}
	pull := spec.Types[strings.TrimPrefix(agent.InputProperties["imagePull"].Ref, "#/types/")]
	if !agent.InputProperties["token"].Secret || !pull.Properties["password"].Secret {
		t.Error("GateAgent schema must mark the token and image pull password as secret")
	}
	if agent.Properties["claimUrl"].Type != "string" {
		t.Error("GateAgent schema must expose a string claimUrl")
	}
}

// No component input may be called `id` or `urn`: Pulumi reserves both on every resource and strips
// them from a component's inputs, so the component sees them empty. A Pool declared with `id` failed
// every real preview with `pool id "" must be a DNS-safe name`, while every unit test passed.
func TestNoComponentInputUsesAReservedName(t *testing.T) {
	server, err := integration.NewServer(context.Background(), Name, semver.MustParse("0.0.0"), integration.WithProvider(providerWith("http://unused")))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.GetSchema(p.GetSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Resources map[string]struct {
			IsComponent     bool                       `json:"isComponent"`
			InputProperties map[string]json.RawMessage `json:"inputProperties"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(resp.Schema), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Resources["github-runners:index:Pool"].InputProperties["poolId"]; !ok {
		t.Fatal("Pool has no poolId input")
	}
	for token, r := range schema.Resources {
		for _, reserved := range []string{"id", "urn"} {
			if _, ok := r.InputProperties[reserved]; ok {
				t.Errorf("%s has an input called %q, which Pulumi strips", token, reserved)
			}
		}
	}
}
