package githubrunners

import (
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"gopkg.in/yaml.v2"
)

// The drop-in as podman reads it (containers/image parses registries.conf with the same library).
type registriesConf struct {
	Registry []struct {
		Prefix   string `toml:"prefix"`
		Location string `toml:"location"`
		Mirror   []struct {
			Location string `toml:"location"`
			Insecure bool   `toml:"insecure"`
		} `toml:"mirror"`
	} `toml:"registry"`
}

func decodeDropIn(t *testing.T, m PoolRegistryMirror) registriesConf {
	t.Helper()
	var conf registriesConf
	if _, err := toml.Decode(podmanFiles(&m)[registryMirrorFile], &conf); err != nil {
		t.Fatal(err)
	}
	return conf
}

func TestThePoolsPullsGoThroughTheMirrorForEachRegistry(t *testing.T) {
	conf := decodeDropIn(t, PoolRegistryMirror{})
	want := map[string]string{
		"docker.io": "registry-mirror-docker-io.arc-systems.svc:5000",
		"ghcr.io":   "registry-mirror-ghcr-io.arc-systems.svc:5000",
		"quay.io":   "registry-mirror-quay-io.arc-systems.svc:5000",
	}
	if len(conf.Registry) != len(want) {
		t.Fatalf("registries %+v", conf.Registry)
	}
	for _, r := range conf.Registry {
		// The registry itself stays the location, so podman goes there when the mirror fails or has
		// no such image (a private one).
		if r.Location != r.Prefix || len(r.Mirror) != 1 {
			t.Fatalf("registry %+v", r)
		}
		if r.Mirror[0].Location != want[r.Prefix] || !r.Mirror[0].Insecure {
			t.Fatalf("%s mirror %+v, want %s over plain HTTP", r.Prefix, r.Mirror[0], want[r.Prefix])
		}
	}

	a := basePool()
	a.RegistryMirror = &PoolRegistryMirror{}
	docker := container(t, podTemplate(a), "docker")
	found := false
	for _, m := range docker["volumeMounts"].([]any) {
		mount := m.(map[string]any)
		if mount["mountPath"] == registryMirrorDropIn {
			found = mount["name"] == "podman-config" && mount["subPath"] == registryMirrorFile && mount["readOnly"] == true
		}
	}
	if !found {
		t.Fatalf("the sidecar does not mount the drop-in at %s: %v", registryMirrorDropIn, docker["volumeMounts"])
	}
	// The drop-in sits beside the image's own registries.conf, which must stay in charge of the rest.
	if strings.Contains(strings.Join(envNames(docker), " "), "CONTAINERS_REGISTRIES_CONF") {
		t.Fatal("CONTAINERS_REGISTRIES_CONF would replace the image's registries.conf")
	}
}

func TestThePoolsMirrorNamespaceAndUpstreams(t *testing.T) {
	conf := decodeDropIn(t, PoolRegistryMirror{Namespace: sp("arc"), Upstreams: []string{"registry.k8s.io"}})
	if len(conf.Registry) != 1 || conf.Registry[0].Prefix != "registry.k8s.io" ||
		conf.Registry[0].Mirror[0].Location != "registry-mirror-registry-k8s-io.arc.svc:5000" {
		t.Fatalf("registries %+v", conf.Registry)
	}
}

func TestAPoolWithoutAMirrorPullsFromTheRegistries(t *testing.T) {
	if _, ok := podmanFiles(nil)[registryMirrorFile]; ok {
		t.Fatal("drop-in without a mirror")
	}
	for _, m := range container(t, podTemplate(basePool()), "docker")["volumeMounts"].([]any) {
		if m.(map[string]any)["mountPath"] == registryMirrorDropIn {
			t.Fatal("drop-in mounted without a mirror")
		}
	}
	a := withApp(basePool())
	a.NetworkPolicy = &PoolNetworkPolicy{}
	if len(poolNetworkPolicy(a).Allow) != 0 {
		t.Fatal("mirror exception without a mirror")
	}
}

func TestCheckPoolRefusesABadMirrorUpstream(t *testing.T) {
	for _, ups := range [][]string{{"https://ghcr.io"}, {"ghcr.io", "ghcr.io"}, {"localhost"}, {"GHCR.IO"}} {
		a := withApp(basePool())
		a.RegistryMirror = &PoolRegistryMirror{Upstreams: ups}
		if err := checkPool(a); err == nil || !strings.Contains(err.Error(), "registryMirror.upstreams") {
			t.Errorf("upstreams %v: %v", ups, err)
		}
	}
}

// The mirror's Service addresses are private, which blockPrivate refuses unless the policy allows them.
func TestThePoolsNetworkPolicyLetsRunnersReachTheMirror(t *testing.T) {
	a := withApp(basePool())
	own := NetworkAllow{PodLabels: map[string]string{"app": "x"}, Ports: []int{1}}
	a.NetworkPolicy = &PoolNetworkPolicy{Allow: []NetworkAllow{own}}
	a.RegistryMirror = &PoolRegistryMirror{Namespace: sp("arc")}
	n := poolNetworkPolicy(a)
	if len(a.NetworkPolicy.Allow) != 1 {
		t.Fatal("poolNetworkPolicy changed the pool's own allow list")
	}
	if len(n.Allow) != 2 {
		t.Fatalf("allow %+v", n.Allow)
	}
	rule := get(t, networkPolicySpec(n), "egress", 3).(map[string]any)
	if get(t, rule, "to", 0, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "arc" ||
		get(t, rule, "to", 0, "podSelector", "matchLabels", "app") != registryMirrorName {
		t.Fatalf("mirror rule %v", rule)
	}
	// Every port of the mirror's pods: one per upstream, which the Controller's list decides.
	if has(t, rule, "ports") {
		t.Fatalf("mirror rule %v limits ports", rule)
	}

	// And through NewPool, as deployed.
	m := runPool(t, a)
	policy := m.find(t, "kubernetes:networking.k8s.io/v1:NetworkPolicy", "-egress")
	if !strings.Contains(allStrings(resource.NewObjectProperty(policy.inputs)), registryMirrorName) {
		t.Fatalf("policy %v", policy.inputs)
	}
	podman := m.find(t, "kubernetes:core/v1:ConfigMap", "-podman")
	if !strings.Contains(podman.inputs["data"].ObjectValue()[registryMirrorFile].StringValue(), "registry-mirror-ghcr-io.arc.svc:5000") {
		t.Fatalf("podman ConfigMap %v", podman.inputs["data"])
	}
}

// distributionConfig is the part of one upstream's config the tests read, decoded with the YAML
// library distribution itself uses.
type distributionConfig struct {
	Log struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
	Storage struct {
		Filesystem struct {
			RootDirectory string `yaml:"rootdirectory"`
		} `yaml:"filesystem"`
	} `yaml:"storage"`
	HTTP struct {
		Addr  string         `yaml:"addr"`
		Debug map[string]any `yaml:"debug"`
	} `yaml:"http"`
	Proxy map[string]string `yaml:"proxy"`
}

func mirrorConfigs(t *testing.T, m *mocks) map[string]distributionConfig {
	t.Helper()
	data := m.find(t, "kubernetes:core/v1:ConfigMap", "-"+registryMirrorName).inputs["data"].ObjectValue()
	out := map[string]distributionConfig{}
	for k, v := range data {
		var c distributionConfig
		if err := yaml.UnmarshalStrict([]byte(v.StringValue()), &map[string]any{}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if err := yaml.Unmarshal([]byte(v.StringValue()), &c); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		out[string(k)] = c
	}
	return out
}

func TestControllerRegistryMirror(t *testing.T) {
	m := runController(t, ControllerArgs{Namespace: sp("arc"), RegistryMirror: &RegistryMirror{}})

	configs := mirrorConfigs(t, m)
	remotes := map[string]string{
		"docker-io.json": "https://registry-1.docker.io",
		"ghcr-io.json":   "https://ghcr.io",
		"quay-io.json":   "https://quay.io",
	}
	if len(configs) != len(remotes) {
		t.Fatalf("configs %v", configs)
	}
	addrs := map[string]bool{}
	for file, c := range configs {
		slug := strings.TrimSuffix(file, ".json")
		if c.Proxy["remoteurl"] != remotes[file] || c.Proxy["ttl"] != "720h" {
			t.Errorf("%s proxy %v", file, c.Proxy)
		}
		// Anonymous only: credentials would let the mirror serve private images to every pod.
		if _, ok := c.Proxy["username"]; ok || c.Proxy["password"] != "" {
			t.Errorf("%s holds upstream credentials", file)
		}
		if c.Storage.Filesystem.RootDirectory != "/var/lib/registry/"+slug || c.Log.Level != "info" || c.HTTP.Debug != nil {
			t.Errorf("%s %+v", file, c)
		}
		addrs[c.HTTP.Addr] = true
	}
	if len(addrs) != len(remotes) {
		t.Fatalf("the containers share a network namespace, so each needs its own port: %v", addrs)
	}

	deploy := m.find(t, "kubernetes:apps/v1:Deployment", "-"+registryMirrorName)
	meta := deploy.inputs["metadata"].ObjectValue()
	if meta["namespace"].StringValue() != "arc" || meta["name"].StringValue() != registryMirrorName {
		t.Fatalf("metadata %v", meta)
	}
	spec := deploy.inputs["spec"].ObjectValue()
	// The volume is ReadWriteOnce: a rolling update's second pod could not mount it.
	if spec["strategy"].ObjectValue()["type"].StringValue() != "Recreate" {
		t.Fatalf("strategy %v", spec["strategy"])
	}
	tpl := spec["template"].ObjectValue()
	if !tpl["metadata"].ObjectValue()["annotations"].ObjectValue().HasValue("github-runners.antimatter.studios/config-sha256") {
		t.Fatal("a changed config must restart the pod")
	}
	pod := tpl["spec"].ObjectValue()
	if pod["automountServiceAccountToken"].BoolValue() {
		t.Fatal("the mirror needs no service account token")
	}
	containers := pod["containers"].ArrayValue()
	if len(containers) != 3 {
		t.Fatalf("containers %v", containers)
	}
	for _, v := range containers {
		c := v.ObjectValue()
		name := c["name"].StringValue()
		args := c["args"].ArrayValue()
		if c["image"].StringValue() != DefaultRegistryMirrorImage || len(args) != 2 || args[1].StringValue() != "/etc/distribution/"+name+".json" {
			t.Fatalf("container %v", c)
		}
		sc := c["securityContext"].ObjectValue()
		if sc["allowPrivilegeEscalation"].BoolValue() || !sc["readOnlyRootFilesystem"].BoolValue() {
			t.Fatalf("security %v", sc)
		}
	}
	if !strings.Contains(DefaultRegistryMirrorImage, "@sha256:") {
		t.Fatal("the image is pinned by digest")
	}
	cache := pod["volumes"].ArrayValue()[0].ObjectValue()
	if cache["persistentVolumeClaim"].ObjectValue()["claimName"].StringValue() != registryMirrorName {
		t.Fatalf("volumes %v", pod["volumes"])
	}

	pvc := m.find(t, "kubernetes:core/v1:PersistentVolumeClaim", "-"+registryMirrorName).inputs["spec"].ObjectValue()
	if pvc["resources"].ObjectValue()["requests"].ObjectValue()["storage"].StringValue() != "50Gi" || pvc.HasValue("storageClassName") {
		t.Fatalf("claim %v", pvc)
	}
	for i, slug := range []string{"docker-io", "ghcr-io", "quay-io"} {
		svc := m.find(t, "kubernetes:core/v1:Service", "-"+registryMirrorName+"-"+slug)
		port := svc.inputs["spec"].ObjectValue()["ports"].ArrayValue()[0].ObjectValue()
		if svc.inputs["metadata"].ObjectValue()["name"].StringValue() != registryMirrorName+"-"+slug ||
			port["port"].NumberValue() != registryMirrorPort || port["targetPort"].NumberValue() != float64(registryMirrorPort+i) {
			t.Fatalf("service %v", svc.inputs)
		}
		if configs[slug+".json"].HTTP.Addr != ":"+strconv.Itoa(int(port["targetPort"].NumberValue())) {
			t.Fatalf("%s: Service targets %v, registry listens on %s", slug, port["targetPort"], configs[slug+".json"].HTTP.Addr)
		}
	}
}

func TestControllerRegistryMirrorSettings(t *testing.T) {
	m := runController(t, ControllerArgs{RegistryMirror: &RegistryMirror{
		Upstreams: []string{"ghcr.io"}, TTL: sp("0"), Size: sp("200Gi"),
		Image: sp("x@sha256:1"), StorageClassName: sp("fast"), PriorityClassName: sp("high"),
	}})
	pvc := m.find(t, "kubernetes:core/v1:PersistentVolumeClaim", "-"+registryMirrorName).inputs["spec"].ObjectValue()
	if pvc["storageClassName"].StringValue() != "fast" || pvc["resources"].ObjectValue()["requests"].ObjectValue()["storage"].StringValue() != "200Gi" {
		t.Fatalf("claim %v", pvc)
	}
	configs := mirrorConfigs(t, m)
	if len(configs) != 1 || configs["ghcr-io.json"].Proxy["ttl"] != "0" || configs["ghcr-io.json"].HTTP.Addr != ":5000" {
		t.Fatalf("configs %+v", configs)
	}
	pod := m.find(t, "kubernetes:apps/v1:Deployment", "-"+registryMirrorName).inputs["spec"].ObjectValue()["template"].ObjectValue()["spec"].ObjectValue()
	if pod["priorityClassName"].StringValue() != "high" || pod["containers"].ArrayValue()[0].ObjectValue()["image"].StringValue() != "x@sha256:1" {
		t.Fatalf("pod %v", pod)
	}
	if m.count("kubernetes:core/v1:Service") != 1 {
		t.Fatal("one Service per upstream")
	}
}

func TestABadRegistryMirrorCreatesNothing(t *testing.T) {
	for _, mirror := range []RegistryMirror{{TTL: sp("a month")}, {TTL: sp("-1h")}, {Upstreams: []string{"ghcr.io/x"}}} {
		m := &mocks{}
		err := pulumi.RunErr(func(ctx *pulumi.Context) error {
			_, err := NewController(ctx, "arc", ControllerArgs{RegistryMirror: &mirror})
			return err
		}, pulumi.WithMocks("project", "stack", m))
		if err == nil || !strings.Contains(err.Error(), "registryMirror") || len(m.all) != 0 {
			t.Errorf("%+v: err %v, %d resources", mirror, err, len(m.all))
		}
	}
}

func TestTheRegistryMirrorIsOffByDefault(t *testing.T) {
	m := runController(t, ControllerArgs{})
	for _, typ := range []string{"kubernetes:apps/v1:Deployment", "kubernetes:core/v1:PersistentVolumeClaim", "kubernetes:core/v1:Service", "kubernetes:core/v1:ConfigMap"} {
		if m.count(typ) != 0 {
			t.Fatalf("unexpected %s", typ)
		}
	}
}

func envNames(c map[string]any) []string {
	var out []string
	for _, e := range c["env"].([]any) {
		out = append(out, e.(map[string]any)["name"].(string))
	}
	return out
}
