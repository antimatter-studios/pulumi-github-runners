package githubrunners

import (
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type registered struct {
	typ, name, provider string
	inputs              resource.PropertyMap
}

type mocks struct {
	mu  sync.Mutex
	all []registered
}

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.all = append(m.all, registered{args.TypeToken, args.Name, args.Provider, args.Inputs})
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
	a.RunnerGroup = &RunnerGroup{Name: "ryzen", AllowsPublicRepositories: bp(true)}
	a.NetworkPolicy = &PoolNetworkPolicy{}
	m := runPool(t, a)

	ns := m.find(t, "kubernetes:core/v1:Namespace", "-ns")
	if ns.inputs["metadata"].ObjectValue()["name"].StringValue() != "arc-runners-acme" {
		t.Fatalf("namespace %v", ns.inputs)
	}
	secret := m.find(t, "kubernetes:core/v1:Secret", "-github")
	data := obj(secret.inputs["stringData"])
	if len(data) != 1 {
		t.Fatalf("ARC's PAT secret keys: %v", data.StableKeys())
	}
	checkSecretString(t, secret.inputs["stringData"], "github_token", "runner-pat")
	gh := m.find(t, "pulumi:providers:github", "-github-admin")
	if gh.inputs["owner"].StringValue() != "acme" || gh.inputs.HasValue("appAuth") {
		t.Fatal("the GitHub provider should use PAT auth for acme")
	}
	checkSecretString(t, resource.NewObjectProperty(gh.inputs), "token", "runner-pat")
	checkCredentialPlacement(t, m, secret, gh, "runner-pat")
	group := m.find(t, "github:index/actionsRunnerGroup:ActionsRunnerGroup", "-group")
	if group.inputs["name"].StringValue() != "ryzen" || group.inputs["visibility"].StringValue() != "all" || !group.inputs["allowsPublicRepositories"].BoolValue() {
		t.Fatalf("group %v", group.inputs)
	}
	if !strings.HasSuffix(group.provider, "::"+gh.name+"::"+gh.name+"-id") {
		t.Fatalf("runner group provider %q, want %s", group.provider, gh.name)
	}
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

func TestPoolWithAGitHubAppAndAGroup(t *testing.T) {
	a := basePool()
	a.App = &GitHubApp{AppID: "123", InstallationID: "456", PrivateKey: stringInput("app-key")}
	a.RunnerGroup = &RunnerGroup{Name: "ryzen", Visibility: sp("selected")}
	m := runPool(t, a)

	secret := m.find(t, "kubernetes:core/v1:Secret", "-github")
	data := obj(secret.inputs["stringData"])
	if len(data) != 3 || data["github_app_id"].StringValue() != "123" || data["github_app_installation_id"].StringValue() != "456" {
		t.Fatalf("ARC's App secret keys: %v", data.StableKeys())
	}
	checkSecretString(t, secret.inputs["stringData"], "github_app_private_key", "app-key")
	gh := m.find(t, "pulumi:providers:github", "-github-admin")
	if gh.inputs["owner"].StringValue() != "acme" || gh.inputs.HasValue("token") {
		t.Fatal("the GitHub provider should use App auth for acme")
	}
	auth := obj(gh.inputs["appAuth"])
	if auth["id"].StringValue() != "123" || auth["installationId"].StringValue() != "456" {
		t.Fatal("the GitHub provider should use the pool's App and installation IDs")
	}
	checkSecretString(t, gh.inputs["appAuth"], "pemFile", "app-key")
	checkCredentialPlacement(t, m, secret, gh, "app-key")
	group := m.find(t, "github:index/actionsRunnerGroup:ActionsRunnerGroup", "-group")
	if group.inputs["visibility"].StringValue() != "selected" ||
		!strings.HasSuffix(group.provider, "::"+gh.name+"::"+gh.name+"-id") {
		t.Fatalf("runner group %v uses provider %q", group.inputs, group.provider)
	}
}

func checkSecretString(t *testing.T, parent resource.PropertyValue, name resource.PropertyKey, want string) {
	t.Helper()
	prop := obj(parent)[name]
	if !(parent.IsSecret() || prop.IsSecret()) || allStrings(prop) != want+"\n" {
		t.Errorf("%s did not receive the pool credential as a secret", name)
	}
}

func checkCredentialPlacement(t *testing.T, m *mocks, secret, gh registered, credential string) {
	t.Helper()
	for _, r := range m.all {
		if r.typ == "github-runners:index:ForkPrApproval" {
			t.Fatal("Pool must not create ForkPrApproval")
		}
		if r.typ != secret.typ && r.typ != gh.typ && strings.Contains(allStrings(resource.NewObjectProperty(r.inputs)), credential) {
			t.Fatalf("the credential reached %s %s", r.typ, r.name)
		}
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
	if err == nil || !strings.Contains(err.Error(), "exactly one of app") || len(m.all) != 0 {
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

// A namespace something else owns is used as it is: pulumi-kubernetes refuses to create one that exists.
func TestControllerInAnExistingNamespace(t *testing.T) {
	m := runController(t, ControllerArgs{
		Namespace:       sp("arc-systems"),
		CreateNamespace: bp(false),
		Devices:         []DeviceOffer{{Name: "fuse", Paths: []string{"/dev/fuse"}}, {Name: "kvm", Paths: []string{"/dev/kvm"}}},
		PrePull:         &PrePull{},
	})
	checkControllerNamespace(t, m, "arc-systems", 0)

	m = runController(t, ControllerArgs{Namespace: sp("arc"), CreateNamespace: bp(false), PrePull: &PrePull{},
		Devices: []DeviceOffer{{Name: "fuse", Paths: []string{"/dev/fuse"}}}})
	checkControllerNamespace(t, m, "arc", 0)
}

func TestControllerCreatesItsNamespaceUnlessToldNotTo(t *testing.T) {
	for _, create := range []*bool{nil, bp(true)} {
		m := runController(t, ControllerArgs{Namespace: sp("arc"), CreateNamespace: create, PrePull: &PrePull{},
			Devices: []DeviceOffer{{Name: "fuse", Paths: []string{"/dev/fuse"}}}})
		checkControllerNamespace(t, m, "arc", 1)
		ns := m.find(t, "kubernetes:core/v1:Namespace", "-ns")
		if got := ns.inputs["metadata"].ObjectValue()["name"].StringValue(); got != "arc" {
			t.Fatalf("namespace %q", got)
		}
	}
}

func TestControllerAndPoolPassPriorityAndResourcesToTheirCharts(t *testing.T) {
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		if _, err := NewController(ctx, "arc", ControllerArgs{
			PriorityClassName: sp("infra-critical"),
			Resources:         &ContainerResources{MemoryRequest: sp("96Mi")},
		}); err != nil {
			return err
		}
		a := basePool()
		a.Token = stringInput("t")
		a.ListenerPriorityClassName = sp("infra-critical")
		a.ListenerResources = &ContainerResources{MemoryRequest: sp("64Mi")}
		_, err := NewPool(ctx, "acme", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	controller := obj(m.find(t, "kubernetes:helm.sh/v3:Release", "-controller").inputs["values"])
	if controller["priorityClassName"].StringValue() != "infra-critical" || obj(obj(controller["resources"])["requests"])["memory"].StringValue() != "96Mi" {
		t.Errorf("controller values %v", controller)
	}
	set := obj(m.find(t, "kubernetes:helm.sh/v3:Release", "-scale-set").inputs["values"])
	listener := allStrings(set["listenerTemplate"])
	if !strings.Contains(listener, "infra-critical") || !strings.Contains(listener, "64Mi") {
		t.Errorf("listener template %q", listener)
	}
}

// An empty resources block would silently remove the controller's default requests and limits.
func TestEmptyResourcesCreateNothing(t *testing.T) {
	pool := basePool()
	pool.Token = stringInput("t")
	pool.ListenerResources = &ContainerResources{CPURequest: sp("")}
	for want, build := range map[string]func(*pulumi.Context) error{
		"resources": func(ctx *pulumi.Context) error {
			_, err := NewController(ctx, "arc", ControllerArgs{Resources: &ContainerResources{}})
			return err
		},
		"listenerResources": func(ctx *pulumi.Context) error {
			_, err := NewPool(ctx, "acme", pool)
			return err
		},
	} {
		m := &mocks{}
		err := pulumi.RunErr(build, pulumi.WithMocks("project", "stack", m))
		if err == nil || !strings.Contains(err.Error(), want+" sets no request or limit") || len(m.all) != 0 {
			t.Errorf("%s: err %v, %d resources", want, err, len(m.all))
		}
	}
}

// checkControllerNamespace: how many Namespaces were created, and that the release and every
// DaemonSet target the namespace.
func checkControllerNamespace(t *testing.T, m *mocks, namespace string, created int) {
	t.Helper()
	if n := m.count("kubernetes:core/v1:Namespace"); n != created {
		t.Fatalf("%d Namespaces, want %d", n, created)
	}
	release := m.find(t, "kubernetes:helm.sh/v3:Release", "-controller")
	if release.inputs["namespace"].StringValue() != namespace || release.inputs["createNamespace"].BoolValue() {
		t.Fatalf("release %v", release.inputs)
	}
	daemonSets := 0
	for _, r := range m.all {
		if r.typ != "kubernetes:apps/v1:DaemonSet" {
			continue
		}
		daemonSets++
		if got := r.inputs["metadata"].ObjectValue()["namespace"].StringValue(); got != namespace {
			t.Errorf("DaemonSet %s in namespace %q, want %q", r.name, got, namespace)
		}
	}
	if daemonSets < 2 {
		t.Fatalf("%d DaemonSets, want the device plugins and the pre-pull", daemonSets)
	}
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
