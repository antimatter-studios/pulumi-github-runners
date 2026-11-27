package githubrunners

import (
	"context"
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
