package githubrunners

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/pkg/v3/codegen/schema"
)

// Every component input the SDK types as pulumi.Input must take an Output in Go, at every depth:
// otherwise a program passes one, its type check agrees, and the deployment fails at Construct.
// The SDK types an input plain only where the schema says plain.
func TestEveryInputTheSDKTakesAsAnOutputTakesOne(t *testing.T) {
	server, err := integration.NewServer(context.Background(), Name, semver.MustParse("0.0.0"), integration.WithProvider(Provider()))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.GetSchema(p.GetSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var spec schema.PackageSpec
	if err := json.Unmarshal([]byte(resp.Schema), &spec); err != nil {
		t.Fatal(err)
	}
	for token, res := range spec.Resources {
		if _, ok := componentArgs[token]; res.IsComponent && !ok {
			t.Errorf("component %s is missing from componentArgs", token)
		}
	}
	checked := 0
	plain := map[string]bool{}
	var walk func(path string, goType reflect.Type, props map[string]schema.PropertySpec)
	walk = func(path string, goType reflect.Type, props map[string]schema.PropertySpec) {
		for _, field := range reflect.VisibleFields(goType) {
			name, _, _ := strings.Cut(field.Tag.Get("pulumi"), ",")
			prop, ok := props[name]
			if name == "" || !ok {
				continue
			}
			checked++
			at := path + "." + name
			plain[at] = prop.Plain
			if takesOutput(field.Type) {
				continue
			}
			if !prop.Plain {
				t.Errorf("%s: the SDK takes an Output, and the Go field (%s) does not", at, field.Type)
			}
			// Into the object type the plain field holds, through pointers, arrays and maps.
			ft, ts := field.Type, prop.TypeSpec
			for {
				for ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ts.Items != nil {
					if !ts.Items.Plain {
						t.Errorf("%s: the SDK takes an Output for an element, and the Go field (%s) does not", at, field.Type)
					}
					ft, ts = ft.Elem(), *ts.Items
					continue
				}
				if ts.AdditionalProperties != nil {
					if !ts.AdditionalProperties.Plain {
						t.Errorf("%s: the SDK takes an Output for a value, and the Go field (%s) does not", at, field.Type)
					}
					ft, ts = ft.Elem(), *ts.AdditionalProperties
					continue
				}
				break
			}
			if token, ok := strings.CutPrefix(ts.Ref, "#/types/"); ok {
				walk(at, ft, spec.Types[token].Properties)
			}
		}
	}
	for token, args := range componentArgs {
		walk(token, args, spec.Resources[token].InputProperties)
	}
	if checked < 50 {
		t.Fatalf("checked only %d inputs", checked)
	}
	// The gate claim, monitoring, priority, namespace and ceiling inputs: the walk reaches them,
	// and each is typed as intended. Namespaces and URLs another resource produces take Outputs;
	// the rest decide what is created, or are settings a program writes itself.
	const (
		controller = "github-runners:index:Controller."
		pool       = "github-runners:index:Pool."
		agent      = "github-runners:index:GateAgent."
	)
	for path, wantPlain := range map[string]bool{
		pool + "gateClaim.url":                                     false,
		pool + "gateClaim.agentNamespace":                          false,
		controller + "monitoring.dashboard.namespace":              false,
		controller + "monitoring.networkPolicy.gateAgentNamespace": false,

		controller + "createNamespace":                          true,
		controller + "priorityClassName":                        true,
		controller + "resources":                                true,
		controller + "resources.cpuRequest":                     true,
		controller + "monitoring":                               true,
		controller + "monitoring.scrapeAnnotations":             true,
		controller + "monitoring.podMonitor.labels":             true,
		controller + "monitoring.podMonitor.interval":           true,
		controller + "monitoring.alerts.poolIds":                true,
		controller + "monitoring.alerts.listenerFor":            true,
		controller + "monitoring.dashboard.labels":              true,
		controller + "monitoring.networkPolicy.allow":           true,
		controller + "monitoring.networkPolicy.allow.podLabels": true,
		pool + "listenerPriorityClassName":                      true,
		pool + "listenerResources.memoryLimit":                  true,
		pool + "listenerScrapeAnnotations":                      true,
		pool + "gateClaim":                                      true,
		pool + "maxRunnersAgent.enabled":                        true,
		pool + "maxRunnersAgent.floor":                          true,
		agent + "manageMaxRunners":                              true,
	} {
		if got, ok := plain[path]; !ok {
			t.Errorf("%s: the walk did not reach it", path)
		} else if got != wantPlain {
			t.Errorf("%s: plain = %v, want %v", path, got, wantPlain)
		}
	}
}
