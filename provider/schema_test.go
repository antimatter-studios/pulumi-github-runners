package githubrunners

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
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
