package githubrunners

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type registered struct {
	typ, name string
	inputs    resource.PropertyMap
}

type mocks struct {
	mu  sync.Mutex
	all []registered
}

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.all = append(m.all, registered{args.TypeToken, args.Name, args.Inputs})
	m.mu.Unlock()
	return args.Name + "-id", args.Inputs, nil
}

func (m *mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func (m *mocks) find(t *testing.T, typ, suffix string) registered {
	t.Helper()
	for _, r := range m.all {
		if r.typ == typ && strings.HasSuffix(r.name, suffix) {
			return r
		}
	}
	t.Fatalf("no %s named *%s among %d resources", typ, suffix, len(m.all))
	return registered{}
}

func (m *mocks) count(typ string) int {
	n := 0
	for _, r := range m.all {
		if r.typ == typ {
			n++
		}
	}
	return n
}

func runPool(t *testing.T, a PoolArgs) *mocks {
	t.Helper()
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewPool(ctx, "acme", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPoolWithAPATAndAGroup(t *testing.T) {
	a := basePool()
	a.Token = stringInput("runner-pat")
	a.AdminToken = stringInput("admin-pat")
	a.RunnerGroup = &RunnerGroup{Name: "ryzen", AllowsPublicRepositories: bp(true)}
	a.ForkPrApproval = sp("all_external_contributors")
	a.NetworkPolicy = &PoolNetworkPolicy{}
	m := runPool(t, a)

	ns := m.find(t, "kubernetes:core/v1:Namespace", "-ns")
	if ns.inputs["metadata"].ObjectValue()["name"].StringValue() != "arc-runners-acme" {
		t.Fatalf("namespace %v", ns.inputs)
	}
	secret := m.find(t, "kubernetes:core/v1:Secret", "-github")
	data := obj(secret.inputs["stringData"])
	if len(data) != 1 || !data.HasValue("github_token") {
		t.Fatalf("ARC's secret holds %v: only github_token belongs there", data.StableKeys())
	}
	for _, r := range m.all {
		if r.typ != "pulumi:providers:github" && r.typ != "github-runners:index:ForkPrApproval" && strings.Contains(allStrings(resource.NewObjectProperty(r.inputs)), "admin-pat") {
			t.Fatalf("the admin token reached %s %s", r.typ, r.name)
		}
	}
	group := m.find(t, "github:index/actionsRunnerGroup:ActionsRunnerGroup", "-group")
	if group.inputs["name"].StringValue() != "ryzen" || group.inputs["visibility"].StringValue() != "all" || !group.inputs["allowsPublicRepositories"].BoolValue() {
		t.Fatalf("group %v", group.inputs)
	}
	m.find(t, "github-runners:index:ForkPrApproval", "-fork-pr-approval")
	m.find(t, "kubernetes:networking.k8s.io/v1:NetworkPolicy", "-egress")
	hooks := m.find(t, "kubernetes:core/v1:ConfigMap", "-hooks")
	if !hooks.inputs["data"].ObjectValue().HasValue("reject-forks.sh") {
		t.Fatal("the fork check is on by default")
	}
	release := m.find(t, "kubernetes:helm.sh/v3:Release", "-scale-set")
	values := release.inputs["values"].ObjectValue()
	if values["runnerGroup"].StringValue() != "ryzen" || values["githubConfigSecret"].StringValue() != "arc-github" {
		t.Fatalf("values %v", values)
	}
	if release.inputs["name"].StringValue() != "ryzen-linux-x64" || release.inputs["namespace"].StringValue() != "arc-runners-acme" {
		t.Fatalf("release %v", release.inputs)
	}
}

func TestPoolWithAGitHubAppAndNoGroup(t *testing.T) {
	a := basePool()
	a.ScaleSetName = "antimatter-vps-linux-x64"
	a.App = &GitHubApp{AppID: "5674189", InstallationID: "180017203", PrivateKey: stringInput("-----BEGIN KEY-----")}
	a.Hook = &PoolHook{RejectForks: bp(false), Script: sp("#!/bin/sh\n"), Files: map[string]string{"allowed-actors": "christhomas\n"}}
	m := runPool(t, a)
	data := obj(m.find(t, "kubernetes:core/v1:Secret", "-github").inputs["stringData"])
	for _, k := range []resource.PropertyKey{"github_app_id", "github_app_installation_id", "github_app_private_key"} {
		if !data.HasValue(k) {
			t.Errorf("missing %s", k)
		}
	}
	if m.count("pulumi:providers:github") != 0 || m.count("github:index/actionsRunnerGroup:ActionsRunnerGroup") != 0 || m.count("kubernetes:networking.k8s.io/v1:NetworkPolicy") != 0 {
		t.Fatal("nothing optional was asked for")
	}
	if _, ok := m.find(t, "kubernetes:helm.sh/v3:Release", "-scale-set").inputs["values"].ObjectValue()["runnerGroup"]; ok {
		t.Fatal("no group: the Default group")
	}
	files := m.find(t, "kubernetes:core/v1:ConfigMap", "-hooks").inputs["data"].ObjectValue()
	if files.HasValue("reject-forks.sh") || !files.HasValue("allowed-actors") || !files.HasValue("custom.sh") {
		t.Fatalf("hook files %v", files.StableKeys())
	}
}

func TestAnInvalidPoolCreatesNothing(t *testing.T) {
	a := basePool()
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewPool(ctx, "acme", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err == nil || !strings.Contains(err.Error(), "exactly one of token") || len(m.all) != 0 {
		t.Fatalf("err %v, %d resources", err, len(m.all))
	}
}

func TestController(t *testing.T) {
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewController(ctx, "arc", ControllerArgs{Devices: []DeviceOffer{
			{Name: "fuse", Paths: []string{"/dev/fuse"}},
			{Name: "kvm", Paths: []string{"/dev/kvm"}, Count: ip(8)},
		}})
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	release := m.find(t, "kubernetes:helm.sh/v3:Release", "-controller")
	if release.inputs["version"].StringValue() != DefaultARCVersion || release.inputs["namespace"].StringValue() != "arc-systems" {
		t.Fatalf("release %v", release.inputs)
	}
	metrics := release.inputs["values"].ObjectValue()["metrics"].ObjectValue()
	if metrics["listenerAddr"].StringValue() != ":8080" || metrics["listenerEndpoint"].StringValue() != "/metrics" {
		t.Fatalf("listener metrics %v: the runner-gate reads them", metrics)
	}
	kvm := m.find(t, "kubernetes:apps/v1:DaemonSet", "-kvm-device-plugin")
	if !strings.Contains(allStrings(resource.NewObjectProperty(kvm.inputs)), `"count":8`) || !strings.Contains(allStrings(resource.NewObjectProperty(kvm.inputs)), "/dev/kvm") {
		t.Fatalf("kvm plugin %v", kvm.inputs)
	}
	m.find(t, "kubernetes:apps/v1:DaemonSet", "-fuse-device-plugin")
}

// obj is an object property, through any secret wrapping.
func obj(v resource.PropertyValue) resource.PropertyMap {
	for v.IsSecret() {
		v = v.SecretValue().Element
	}
	return v.ObjectValue()
}

// allStrings is every string in a property tree, secrets included, joined: what a leak check reads.
func allStrings(v resource.PropertyValue) string {
	var b strings.Builder
	var walk func(resource.PropertyValue)
	walk = func(v resource.PropertyValue) {
		switch {
		case v.IsSecret():
			walk(v.SecretValue().Element)
		case v.IsString():
			b.WriteString(v.StringValue() + "\n")
		case v.IsObject():
			for _, k := range v.ObjectValue().StableKeys() {
				walk(v.ObjectValue()[k])
			}
		case v.IsArray():
			for _, e := range v.ArrayValue() {
				walk(e)
			}
		}
	}
	walk(v)
	return b.String()
}
