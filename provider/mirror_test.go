package githubrunners

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
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

func TestThePoolsDockerHubPullsGoThroughTheMirror(t *testing.T) {
	a := basePool()
	a.RegistryMirror = &PoolRegistryMirror{}
	var conf registriesConf
	if _, err := toml.Decode(podmanFiles(a.RegistryMirror)[registryMirrorFile], &conf); err != nil {
		t.Fatal(err)
	}
	if len(conf.Registry) != 1 {
		t.Fatalf("registries %+v", conf.Registry)
	}
	r := conf.Registry[0]
	// docker.io itself stays the location, so podman falls back to Docker Hub when the mirror fails.
	if r.Prefix != "docker.io" || r.Location != "docker.io" || len(r.Mirror) != 1 {
		t.Fatalf("registry %+v", r)
	}
	if r.Mirror[0].Location != "registry-mirror.arc-systems.svc:5000" || !r.Mirror[0].Insecure {
		t.Fatalf("mirror %+v: the in-cluster mirror speaks plain HTTP", r.Mirror[0])
	}

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

func TestTheMirrorNamespaceIsTheControllers(t *testing.T) {
	conf := podmanFiles(&PoolRegistryMirror{Namespace: sp("arc")})[registryMirrorFile]
	if !strings.Contains(conf, `location = "registry-mirror.arc.svc:5000"`) {
		t.Fatalf("drop-in:\n%s", conf)
	}
}

func TestAPoolWithoutAMirrorPullsFromDockerHub(t *testing.T) {
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

// The mirror's Service address is private, which blockPrivate refuses unless the policy allows it.
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
	spec := networkPolicySpec(n)
	rule := get(t, spec, "egress", 3).(map[string]any)
	if get(t, rule, "to", 0, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "arc" ||
		get(t, rule, "to", 0, "podSelector", "matchLabels", "app") != registryMirrorName ||
		get(t, rule, "ports", 0, "port") != float64(registryMirrorPort) {
		t.Fatalf("mirror rule %v", rule)
	}

	// And through NewPool, as deployed.
	m := runPool(t, a)
	policy := m.find(t, "kubernetes:networking.k8s.io/v1:NetworkPolicy", "-egress")
	if !strings.Contains(allStrings(resource.NewObjectProperty(policy.inputs)), registryMirrorName) {
		t.Fatalf("policy %v", policy.inputs)
	}
	podman := m.find(t, "kubernetes:core/v1:ConfigMap", "-podman")
	if !strings.Contains(podman.inputs["data"].ObjectValue()[registryMirrorFile].StringValue(), "registry-mirror.arc.svc:5000") {
		t.Fatalf("podman ConfigMap %v", podman.inputs["data"])
	}
}

func TestControllerRegistryMirror(t *testing.T) {
	m := runController(t, ControllerArgs{Namespace: sp("arc"), RegistryMirror: &RegistryMirror{}})
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
	pod := spec["template"].ObjectValue()["spec"].ObjectValue()
	if pod["automountServiceAccountToken"].BoolValue() {
		t.Fatal("the mirror needs no service account token")
	}
	c := pod["containers"].ArrayValue()[0].ObjectValue()
	if c["image"].StringValue() != DefaultRegistryMirrorImage || !strings.Contains(DefaultRegistryMirrorImage, "@sha256:") {
		t.Fatalf("image %v", c["image"])
	}
	env := map[string]string{}
	for _, e := range c["env"].ArrayValue() {
		env[e.ObjectValue()["name"].StringValue()] = e.ObjectValue()["value"].StringValue()
	}
	if env["REGISTRY_PROXY_REMOTEURL"] != "https://registry-1.docker.io" || env["REGISTRY_HTTP_ADDR"] != ":5000" {
		t.Fatalf("env %v", env)
	}
	sc := c["securityContext"].ObjectValue()
	if sc["allowPrivilegeEscalation"].BoolValue() || !sc["readOnlyRootFilesystem"].BoolValue() {
		t.Fatalf("security %v", sc)
	}
	cache := pod["volumes"].ArrayValue()[0].ObjectValue()
	if cache["persistentVolumeClaim"].ObjectValue()["claimName"].StringValue() != registryMirrorName {
		t.Fatalf("volumes %v", pod["volumes"])
	}

	pvc := m.find(t, "kubernetes:core/v1:PersistentVolumeClaim", "-"+registryMirrorName)
	pvcSpec := pvc.inputs["spec"].ObjectValue()
	if pvcSpec["resources"].ObjectValue()["requests"].ObjectValue()["storage"].StringValue() != "20Gi" || pvcSpec.HasValue("storageClassName") {
		t.Fatalf("claim %v", pvcSpec)
	}
	svc := m.find(t, "kubernetes:core/v1:Service", "-"+registryMirrorName)
	port := svc.inputs["spec"].ObjectValue()["ports"].ArrayValue()[0].ObjectValue()
	if svc.inputs["metadata"].ObjectValue()["name"].StringValue() != registryMirrorName || port["port"].NumberValue() != registryMirrorPort {
		t.Fatalf("service %v", svc.inputs)
	}
}

func TestControllerRegistryMirrorSettings(t *testing.T) {
	m := runController(t, ControllerArgs{RegistryMirror: &RegistryMirror{
		Image: sp("x@sha256:1"), StorageSize: sp("50Gi"), StorageClassName: sp("fast"), PriorityClassName: sp("high"),
	}})
	pvc := m.find(t, "kubernetes:core/v1:PersistentVolumeClaim", "-"+registryMirrorName).inputs["spec"].ObjectValue()
	if pvc["storageClassName"].StringValue() != "fast" || pvc["resources"].ObjectValue()["requests"].ObjectValue()["storage"].StringValue() != "50Gi" {
		t.Fatalf("claim %v", pvc)
	}
	pod := m.find(t, "kubernetes:apps/v1:Deployment", "-"+registryMirrorName).inputs["spec"].ObjectValue()["template"].ObjectValue()["spec"].ObjectValue()
	if pod["priorityClassName"].StringValue() != "high" || pod["containers"].ArrayValue()[0].ObjectValue()["image"].StringValue() != "x@sha256:1" {
		t.Fatalf("pod %v", pod)
	}
}

func TestTheRegistryMirrorIsOffByDefault(t *testing.T) {
	m := runController(t, ControllerArgs{})
	for _, typ := range []string{"kubernetes:apps/v1:Deployment", "kubernetes:core/v1:PersistentVolumeClaim", "kubernetes:core/v1:Service"} {
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
