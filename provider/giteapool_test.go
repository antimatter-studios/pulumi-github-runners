package githubrunners

import (
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"sigs.k8s.io/yaml"
)

func baseGiteaPool() GiteaRunnerPoolArgs {
	return GiteaRunnerPoolArgs{
		ID:                "ryzen",
		InstanceURL:       "https://git.example.com",
		Scope:             "repo",
		Owner:             sp("acme"),
		Repo:              sp("server"),
		RegistrationToken: stringInput("registration-secret"),
	}
}

// scoped sets the pool's scope; an empty owner or repo is left unset.
func scoped(a *GiteaRunnerPoolArgs, scope, owner, repo string) {
	a.Scope, a.Owner, a.Repo = scope, nil, nil
	if owner != "" {
		a.Owner = sp(owner)
	}
	if repo != "" {
		a.Repo = sp(repo)
	}
}

func TestCheckGiteaPool(t *testing.T) {
	valid := map[string]func(a *GiteaRunnerPoolArgs){
		"repo scope":             func(a *GiteaRunnerPoolArgs) {},
		"org scope":              func(a *GiteaRunnerPoolArgs) { scoped(a, "org", "acme", "") },
		"instance scope":         func(a *GiteaRunnerPoolArgs) { scoped(a, "instance", "", "") },
		"capacity":               func(a *GiteaRunnerPoolArgs) { a.Capacity = ip(3) },
		"in-cluster http":        func(a *GiteaRunnerPoolArgs) { a.InstanceURL = "http://gitea-http.gitea.svc:3000/" },
		"instance under a path":  func(a *GiteaRunnerPoolArgs) { a.InstanceURL = "https://example.com/gitea" },
		"scaled to zero":         func(a *GiteaRunnerPoolArgs) { a.Replicas = ip(0) },
		"dind":                   func(a *GiteaRunnerPoolArgs) { a.Engine = sp("dind") },
		"a label with a version": func(a *GiteaRunnerPoolArgs) { a.Labels = []string{"go:docker://golang:1.26"} },
		"user scope":             func(a *GiteaRunnerPoolArgs) { scoped(a, "user", "someone", "") },
		"host labels with an image": func(a *GiteaRunnerPoolArgs) {
			a.Labels, a.Image = []string{"homelab-server-deploy", "linux:host"}, sp("registry.example.com/gitea-host:1")
		},
	}
	for name, change := range valid {
		a := baseGiteaPool()
		change(&a)
		if err := checkGiteaPool(a); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	invalid := map[string]struct {
		change func(a *GiteaRunnerPoolArgs)
		want   string
	}{
		"bad poolId":         {func(a *GiteaRunnerPoolArgs) { a.ID = "Ryzen_1" }, "poolId"},
		"no instance":        {func(a *GiteaRunnerPoolArgs) { a.InstanceURL = "" }, "instanceUrl"},
		"not a URL":          {func(a *GiteaRunnerPoolArgs) { a.InstanceURL = "git.example.com" }, "instanceUrl"},
		"URL with a token":   {func(a *GiteaRunnerPoolArgs) { a.InstanceURL = "https://u:p@git.example.com" }, "instanceUrl"},
		"URL with a query":   {func(a *GiteaRunnerPoolArgs) { a.InstanceURL = "https://git.example.com?x=1" }, "instanceUrl"},
		"no scope":           {func(a *GiteaRunnerPoolArgs) { scoped(a, "", "", "") }, "scope"},
		"unknown scope":      {func(a *GiteaRunnerPoolArgs) { scoped(a, "team", "acme", "") }, "scope"},
		"repo without owner": {func(a *GiteaRunnerPoolArgs) { scoped(a, "repo", "", "server") }, "owner"},
		"repo without repo":  {func(a *GiteaRunnerPoolArgs) { scoped(a, "repo", "acme", "") }, "repo"},
		"repo as a pair":     {func(a *GiteaRunnerPoolArgs) { scoped(a, "repo", "acme", "acme/server") }, "repo"},
		"org without owner":  {func(a *GiteaRunnerPoolArgs) { scoped(a, "org", "", "") }, "owner"},
		"org with a repo":    {func(a *GiteaRunnerPoolArgs) { scoped(a, "org", "acme", "server") }, "repo"},
		"org with a slash":   {func(a *GiteaRunnerPoolArgs) { scoped(a, "org", "acme/server", "") }, "owner"},
		"user without owner": {func(a *GiteaRunnerPoolArgs) { scoped(a, "user", "", "") }, "owner"},
		"instance owner":     {func(a *GiteaRunnerPoolArgs) { scoped(a, "instance", "acme", "") }, "owner"},
		"zero capacity":      {func(a *GiteaRunnerPoolArgs) { a.Capacity = ip(0) }, "capacity"},
		"no token":           {func(a *GiteaRunnerPoolArgs) { a.RegistrationToken = nil }, "registrationToken"},
		"negative replicas":  {func(a *GiteaRunnerPoolArgs) { a.Replicas = ip(-1) }, "replicas"},
		"negative partition": {func(a *GiteaRunnerPoolArgs) { a.UpdatePartition = ip(-1) }, "updatePartition"},
		"unknown engine":     {func(a *GiteaRunnerPoolArgs) { a.Engine = sp("docker") }, "engine"},
		// Host steps run in the runner image, and Gitea's own has no tools for them.
		"host label, default image": {func(a *GiteaRunnerPoolArgs) { a.Labels = []string{"linux:host"} }, "image"},
		"bare label, default image": {func(a *GiteaRunnerPoolArgs) { a.Labels = []string{"deploy"} }, "image"},
		"unknown schema":            {func(a *GiteaRunnerPoolArgs) { a.Labels = []string{"linux:vm://x"} }, "label"},
		"empty docker image":        {func(a *GiteaRunnerPoolArgs) { a.Labels = []string{"linux:docker://"} }, "label"},
		"empty label":               {func(a *GiteaRunnerPoolArgs) { a.Labels = []string{""} }, "label"},
		"space in label":            {func(a *GiteaRunnerPoolArgs) { a.Labels = []string{"a b:docker://x"} }, "label"},
		"comma in label":            {func(a *GiteaRunnerPoolArgs) { a.Labels = []string{"a:docker://x,b:host"} }, "label"},
		"reserved env":              {func(a *GiteaRunnerPoolArgs) { a.Env = map[string]string{"DOCKER_HOST": "tcp://x"} }, "DOCKER_HOST"},
	}
	for name, c := range invalid {
		a := baseGiteaPool()
		c.change(&a)
		if err := checkGiteaPool(a); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one naming %q", name, err, c.want)
		}
	}
}

func TestRegistrationTokenEndpoint(t *testing.T) {
	for want, scope := range map[string][3]string{
		"https://git.example.com/api/v1/repos/acme/server/actions/runners/registration-token": {"repo", "acme", "server"},
		"https://git.example.com/api/v1/orgs/acme/actions/runners/registration-token":         {"org", "acme", ""},
		"https://git.example.com/api/v1/admin/actions/runners/registration-token":             {"instance", "", ""},
		"https://git.example.com/api/v1/user/actions/runners/registration-token":              {"user", "someone", ""},
	} {
		a := baseGiteaPool()
		a.InstanceURL = "https://git.example.com/"
		scoped(&a, scope[0], scope[1], scope[2])
		if got := registrationTokenEndpoint(a); got != want {
			t.Errorf("endpoint %s, want %s", got, want)
		}
	}
}

func TestGiteaRunnerConfigCapacityAndContainerOptions(t *testing.T) {
	config := giteaConfig(t, baseGiteaPool())
	if get(t, config, "runner", "capacity") != float64(1) {
		t.Errorf("default capacity %v", get(t, config, "runner", "capacity"))
	}
	if has(t, config, "container", "options") {
		t.Error("no container options unless the pool sets them")
	}
	a := baseGiteaPool()
	a.Capacity = ip(3)
	a.ContainerOptions = sp("--memory=4g --add-host=git:10.0.0.1")
	config = giteaConfig(t, a)
	if get(t, config, "runner", "capacity") != float64(3) || get(t, config, "container", "options") != *a.ContainerOptions {
		t.Errorf("config %v", config)
	}
}

func giteaConfig(t *testing.T, a GiteaRunnerPoolArgs) map[string]any {
	t.Helper()
	raw, err := giteaRunnerConfig(a)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGiteaRunnerConfigHasTheLabels(t *testing.T) {
	// In the config file, not only at registration: the daemon declares these on every start, so a
	// label change needs a restart and not a new registration.
	config := giteaConfig(t, baseGiteaPool())
	got := get(t, config, "runner", "labels").([]any)
	want := []any{
		"ubuntu-latest:docker://docker.gitea.com/runner-images:ubuntu-latest",
		"ubuntu-24.04:docker://docker.gitea.com/runner-images:ubuntu-24.04",
		"ubuntu-22.04:docker://docker.gitea.com/runner-images:ubuntu-22.04",
	}
	if !slices.Equal(got, want) {
		t.Errorf("default labels %v", got)
	}
	a := baseGiteaPool()
	a.Labels = []string{"go:docker://golang:1.26"}
	if got := get(t, giteaConfig(t, a), "runner", "labels").([]any); len(got) != 1 || got[0] != a.Labels[0] {
		t.Errorf("labels %v", got)
	}
}

func TestGiteaRunnerConfigJobEnvironment(t *testing.T) {
	a := baseGiteaPool()
	a.Env = map[string]string{"GOMAXPROCS": "2"}
	envs := get(t, giteaConfig(t, a), "runner", "envs")
	// podman serves the classic build API; BuildKit needs cgroups.
	if get(t, envs, "DOCKER_BUILDKIT") != "0" || get(t, envs, "GOMAXPROCS") != "2" {
		t.Errorf("podman job env %v", envs)
	}
	a.Engine = sp("dind")
	if has(t, giteaConfig(t, a), "runner", "envs", "DOCKER_BUILDKIT") {
		t.Error("dind runs BuildKit; do not turn it off")
	}
}

func TestGiteaRegistrationIsKeptUntilTheInstanceOrScopeChanges(t *testing.T) {
	a := baseGiteaPool()
	file := giteaStateFile(a)
	if !strings.HasPrefix(file, ".runner-") || get(t, giteaConfig(t, a), "runner", "file") != file {
		t.Fatalf("state file %q must be the one the config names", file)
	}
	same := baseGiteaPool()
	same.InstanceURL += "/"
	same.RegistrationToken = stringInput("a-new-token")
	same.Labels = []string{"go:docker://golang:1.26"}
	same.Replicas = ip(4)
	if giteaStateFile(same) != file {
		t.Error("a new token, new labels or more replicas must not register again")
	}
	user := baseGiteaPool()
	scoped(&user, "user", "someone", "")
	other := baseGiteaPool()
	scoped(&other, "user", "someone-else", "")
	if giteaStateFile(user) == giteaStateFile(other) {
		t.Error("another user must register again, though the API path is the same")
	}
	for name, change := range map[string]func(a *GiteaRunnerPoolArgs){
		"instance": func(a *GiteaRunnerPoolArgs) { a.InstanceURL = "https://other.example.com" },
		"scope":    func(a *GiteaRunnerPoolArgs) { scoped(a, "org", "acme", "") },
	} {
		b := baseGiteaPool()
		change(&b)
		if giteaStateFile(b) == file {
			t.Errorf("a new %s must register again", name)
		}
	}
}

func envValue(t *testing.T, c map[string]any, name string) (string, bool) {
	t.Helper()
	list, _ := c["env"].([]any)
	for _, e := range list {
		m := e.(map[string]any)
		if m["name"] == name {
			v, _ := m["value"].(string)
			return v, true
		}
	}
	return "", false
}

func mountPath(c map[string]any, volume string) string {
	list, _ := c["volumeMounts"].([]any)
	for _, m := range list {
		if m.(map[string]any)["name"] == volume {
			return m.(map[string]any)["mountPath"].(string)
		}
	}
	return ""
}

// giteaPod is the pod template of the StatefulSet the component registers, as Kubernetes reads it.
func giteaPod(t *testing.T, a GiteaRunnerPoolArgs) map[string]any {
	t.Helper()
	sts := runGiteaPool(t, a).find(t, "kubernetes:apps/v1:StatefulSet", "-runner")
	return get(t, obj(sts.inputs["spec"])["template"].ObjectValue().Mappable()).(map[string]any)
}

func initContainer(t *testing.T, tpl map[string]any, name string) map[string]any {
	t.Helper()
	for _, c := range get(t, tpl, "spec", "initContainers").([]any) {
		if c.(map[string]any)["name"] == name {
			return c.(map[string]any)
		}
	}
	t.Fatalf("no init container %s", name)
	return nil
}

func script(t *testing.T, c map[string]any) string {
	t.Helper()
	return get(t, c, "command", 2).(string)
}

func TestGiteaPodRegistersOnceInAnInitContainer(t *testing.T) {
	tpl := giteaPod(t, baseGiteaPool())
	register := initContainer(t, tpl, "register")
	if register["image"] != DefaultGiteaRunnerImage || !strings.Contains(DefaultGiteaRunnerImage, "@sha256:") {
		t.Errorf("register image %v", register["image"])
	}
	if mountPath(register, "data") != "/data" || register["workingDir"] != "/data" {
		t.Error("the registration must be written to the persistent volume")
	}
	if v, _ := envValue(t, register, "RUNNER_STATE_FILE"); v != giteaStateFile(baseGiteaPool()) {
		t.Errorf("RUNNER_STATE_FILE %q", v)
	}
	if v, _ := envValue(t, register, "GITEA_INSTANCE_URL"); v != "https://git.example.com" {
		t.Errorf("instance %q", v)
	}
	got := script(t, register)
	// Only without a registration, and a failure to register fails the container, which retries.
	if !strings.Contains(got, `if [ ! -s "$RUNNER_STATE_FILE" ]`) || !strings.Contains(got, "gitea-runner register") ||
		!strings.HasSuffix(strings.TrimSpace(got), `[ -s "$RUNNER_STATE_FILE" ]`) {
		t.Errorf("register script %q", got)
	}
}

func TestGiteaPodShowsItsTokenOnlyWhileRegistering(t *testing.T) {
	for _, labels := range [][]string{nil, {"deploy:host"}} {
		a := baseGiteaPool()
		a.Labels, a.Image = labels, sp("registry.example.com/gitea-host:1")
		tpl := giteaPod(t, a)
		register := initContainer(t, tpl, "register")
		file, _ := envValue(t, register, "GITEA_RUNNER_REGISTRATION_TOKEN_FILE")
		if file != mountPath(register, "registration-token")+"/token" || !strings.Contains(script(t, register), `cat "$GITEA_RUNNER_REGISTRATION_TOKEN_FILE"`) {
			t.Errorf("the token must be read from the mounted Secret, not %q", file)
		}
		// Jobs run in the runner container (host labels) or control the engine (docker labels).
		for _, name := range []string{"runner", "engine"} {
			if mountPath(container(t, tpl, name), "registration-token") != "" {
				t.Errorf("%v: %s must not see the registration token", labels, name)
			}
		}
		if mountPath(container(t, tpl, "engine"), "data") != "" {
			t.Error("the engine must not see the runner's credential")
		}
		if get(t, tpl, "spec", "automountServiceAccountToken") != false {
			t.Error("no Kubernetes credential in a runner pod")
		}
		if has(t, tpl, "spec", "serviceAccountName") {
			t.Error("the pool binds no ServiceAccount")
		}
	}
}

func TestGiteaRunnerWaitsForTheEngineThenRunsTheDaemon(t *testing.T) {
	runner := container(t, giteaPod(t, baseGiteaPool()), "runner")
	command := script(t, runner)
	if !strings.Contains(command, "[ -S /run/engine/docker.sock ]") || !strings.HasSuffix(command, `exec tini -- gitea-runner daemon --config /etc/gitea-runner/config.yaml`) {
		t.Errorf("runner command %q", command)
	}
	if mountPath(runner, "data") != "/data" || runner["workingDir"] != "/data" {
		t.Error("the daemon reads the registration from the persistent volume")
	}
}

// A runner image that is not root cannot write a new volume's root directory.
func TestGiteaDataIsWritableByTheImagesUser(t *testing.T) {
	tpl := giteaPod(t, baseGiteaPool())
	first := get(t, tpl, "spec", "initContainers", 0).(map[string]any)
	if first["name"] != "prepare" || get(t, first, "securityContext", "runAsUser") != float64(0) || mountPath(first, "data") != "/data" {
		t.Errorf("first init container %v", first)
	}
	// Recursive: the daemon rewrites its registration file, which an image run as another user wrote.
	if got := script(t, first); got != "chmod -R a+rwX /data" {
		t.Errorf("prepare runs %q", got)
	}
	// Not fsGroup, which would also rewrite the ownership of every file in the engine's image store.
	if has(t, tpl, "spec", "securityContext", "fsGroup") {
		t.Error("fsGroup would reach the engine's storage volume")
	}
}

// act_runner binds the socket named by DOCKER_HOST into every job container, and the engine resolves
// that path in its own filesystem. It must be the same directory in both containers.
func TestGiteaSocketPathIsTheSameInBothContainers(t *testing.T) {
	for _, engineName := range []string{"podman", "dind"} {
		a := baseGiteaPool()
		a.Engine = &engineName
		tpl := giteaPod(t, a)
		runner, engine := container(t, tpl, "runner"), container(t, tpl, "engine")
		dir := mountPath(runner, "docker-socket")
		if dir == "" || mountPath(engine, "docker-socket") != dir {
			t.Errorf("%s: socket directory %q in the runner, %q in the engine", engineName, dir, mountPath(engine, "docker-socket"))
		}
		if v, _ := envValue(t, runner, "DOCKER_HOST"); v != "unix://"+dir+"/docker.sock" {
			t.Errorf("%s: DOCKER_HOST %q", engineName, v)
		}
		if !strings.Contains(allText(t, engine["command"]), "unix://"+dir+"/docker.sock") {
			t.Errorf("%s: the engine does not listen on the socket: %v", engineName, engine["command"])
		}
	}
}

func allText(t *testing.T, v any) string {
	t.Helper()
	var b strings.Builder
	list, _ := v.([]any)
	for _, e := range list {
		b.WriteString(e.(string) + " ")
	}
	return b.String()
}

func TestGiteaPodmanEngineIsNotPrivileged(t *testing.T) {
	tpl := giteaPod(t, baseGiteaPool())
	engine := container(t, tpl, "engine")
	if get(t, tpl, "spec", "hostUsers") != false {
		t.Error("podman runs in the pod's own user namespace")
	}
	if get(t, engine, "securityContext", "privileged") != false || engine["image"] != DefaultGiteaPodmanImage {
		t.Errorf("engine %v", engine)
	}
	if mountPath(engine, "podman-config") != podmanConfigDir || mountPath(engine, "engine-storage") != "/var/lib/containers" {
		t.Errorf("engine mounts %v", engine["volumeMounts"])
	}
}

// Podman 6.1.3's Docker API cannot copy an archive through an absolute symlink to a job container
// (statat var/run/act: path escapes from parent), which is how the runner copies every action in.
func TestGiteaPodmanIsAReleaseThatCopiesActionsIn(t *testing.T) {
	if !strings.HasPrefix(DefaultGiteaPodmanImage, "quay.io/podman/stable:v5.8@sha256:") {
		t.Errorf("podman image %s: prove actions/checkout in a docker:// job before moving off 5.8", DefaultGiteaPodmanImage)
	}
}

func TestGiteaDindEngine(t *testing.T) {
	a := baseGiteaPool()
	a.Engine = sp("dind")
	tpl := giteaPod(t, a)
	engine := container(t, tpl, "engine")
	if get(t, engine, "securityContext", "privileged") != true || engine["image"] != DefaultDindImage || !strings.Contains(DefaultDindImage, "@sha256:") {
		t.Errorf("engine %v", engine)
	}
	if has(t, tpl, "spec", "hostUsers") {
		t.Error("privileged containers cannot run with hostUsers: false")
	}
	if mountPath(engine, "engine-storage") != "/var/lib/docker" || mountPath(engine, "podman-config") != "" {
		t.Errorf("engine mounts %v", engine["volumeMounts"])
	}
	for _, v := range get(t, tpl, "spec", "volumes").([]any) {
		if v.(map[string]any)["name"] == "podman-config" {
			t.Error("dind needs no podman configuration")
		}
	}
	// A runner that is not root must reach dockerd's socket.
	if !strings.Contains(allText(t, engine["command"]), "chmod 666 /run/engine/docker.sock") {
		t.Errorf("engine command %v", engine["command"])
	}
	a.EngineImage = sp("docker:custom")
	if container(t, giteaPod(t, a), "engine")["image"] != "docker:custom" {
		t.Error("engineImage overrides the engine's image")
	}
}

func TestGiteaPodResourcesAndScheduling(t *testing.T) {
	tpl := giteaPod(t, baseGiteaPool())
	for _, name := range []string{"runner", "engine"} {
		c := container(t, tpl, name)
		for _, resource := range []string{"cpu", "memory"} {
			if has(t, c, "resources", "limits", resource) {
				t.Errorf("%s: no %s limit unless the pool sets one", name, resource)
			}
			get(t, c, "resources", "requests", resource)
		}
		get(t, c, "resources", "limits", "ephemeral-storage")
	}
	for _, absent := range []string{"priorityClassName", "nodeSelector", "tolerations"} {
		if has(t, tpl, "spec", absent) {
			t.Errorf("%s is set by default", absent)
		}
	}

	a := baseGiteaPool()
	a.RunnerResources = &GiteaContainerResources{CPURequest: sp("200m"), MemoryLimit: sp("512Mi")}
	a.EngineResources = &GiteaContainerResources{CPULimit: sp("2"), MemoryRequest: sp("1Gi"), MemoryLimit: sp("4Gi")}
	a.EngineStorageSize = sp("50Gi")
	a.PriorityClassName = sp("ci-low")
	a.NodeSelector = map[string]string{"kubernetes.io/hostname": "ryzen"}
	tpl = giteaPod(t, a)
	runner, engine := container(t, tpl, "runner"), container(t, tpl, "engine")
	for _, c := range []struct {
		c    map[string]any
		path [2]string
		want string
	}{
		{runner, [2]string{"requests", "cpu"}, "200m"},
		{runner, [2]string{"limits", "memory"}, "512Mi"},
		{engine, [2]string{"limits", "cpu"}, "2"},
		{engine, [2]string{"requests", "memory"}, "1Gi"},
		{engine, [2]string{"limits", "memory"}, "4Gi"},
	} {
		if got := get(t, c.c, "resources", c.path[0], c.path[1]); got != c.want {
			t.Errorf("%v %v = %v, want %s", c.c["name"], c.path, got, c.want)
		}
	}
	if get(t, tpl, "spec", "priorityClassName") != "ci-low" || get(t, tpl, "spec", "nodeSelector", "kubernetes.io/hostname") != "ryzen" {
		t.Errorf("scheduling %v", tpl["spec"])
	}
}

// HARD: soft spreading lets one node take every runner, and their builds starve each other.
func TestGiteaPodsSpreadHardAcrossHosts(t *testing.T) {
	tpl := giteaPod(t, baseGiteaPool())
	c := get(t, tpl, "spec", "topologySpreadConstraints", 0).(map[string]any)
	if c["whenUnsatisfiable"] != "DoNotSchedule" || c["maxSkew"] != float64(1) || c["topologyKey"] != "kubernetes.io/hostname" {
		t.Errorf("spread %v", c)
	}
	// Only this pool's runners count, or one pool would push another off a node.
	if get(t, c, "labelSelector", "matchLabels", "app.kubernetes.io/instance") != "ryzen" ||
		get(t, tpl, "metadata", "labels", "app.kubernetes.io/instance") != "ryzen" {
		t.Errorf("selector %v", c["labelSelector"])
	}
	if has(t, tpl, "spec", "affinity") {
		t.Error("the spread replaces anti-affinity")
	}
	a := baseGiteaPool()
	a.TopologySpread = bp(false)
	if has(t, giteaPod(t, a), "spec", "topologySpreadConstraints") {
		t.Error("topologySpread: false removes the constraint")
	}
}

func TestGiteaPodServiceAccountTokenIsOptIn(t *testing.T) {
	a := baseGiteaPool()
	a.AutomountServiceAccountToken = bp(true)
	a.ServiceAccountName = sp("deployer")
	m := runGiteaPool(t, a)
	for _, r := range m.all {
		if strings.Contains(r.typ, "rbac.authorization.k8s.io") || strings.HasSuffix(r.typ, ":ServiceAccount") {
			t.Errorf("the pool creates no identity or permission: %s", r.typ)
		}
	}
	tpl := giteaPod(t, a)
	if get(t, tpl, "spec", "automountServiceAccountToken") != true || get(t, tpl, "spec", "serviceAccountName") != "deployer" {
		t.Errorf("service account %v", tpl["spec"])
	}
}

func volume(t *testing.T, tpl map[string]any, name string) map[string]any {
	t.Helper()
	for _, v := range get(t, tpl, "spec", "volumes").([]any) {
		if m := v.(map[string]any); m["name"] == name {
			return m
		}
	}
	return nil
}

func claims(t *testing.T, a GiteaRunnerPoolArgs) map[string]map[string]any {
	t.Helper()
	sts := runGiteaPool(t, a).find(t, "kubernetes:apps/v1:StatefulSet", "-runner")
	out := map[string]map[string]any{}
	for _, c := range obj(sts.inputs["spec"])["volumeClaimTemplates"].ArrayValue() {
		m := c.ObjectValue().Mappable()
		out[get(t, m, "metadata", "name").(string)] = get(t, m, "spec").(map[string]any)
	}
	return out
}

func TestGiteaEngineStorageIsAVolumePerRunner(t *testing.T) {
	a := baseGiteaPool()
	a.StorageClassName = sp("longhorn")
	got := claims(t, a)
	engine := got["engine-storage"]
	if len(got) != 2 || engine == nil || get(t, engine, "resources", "requests", "storage") != "20Gi" {
		t.Fatalf("claims %v", got)
	}
	// The image cache is not the registration: replicated storage for one must not mean it for both.
	if has(t, engine, "storageClassName") {
		t.Errorf("engine storage class %v, want the cluster's default", engine)
	}
	if volume(t, giteaPod(t, a), "engine-storage") != nil {
		t.Error("the claim template provides the volume")
	}
	a.EngineStorageClassName, a.EngineStorageSize = sp("local-path"), sp("50Gi")
	engine = claims(t, a)["engine-storage"]
	if engine["storageClassName"] != "local-path" || get(t, engine, "resources", "requests", "storage") != "50Gi" {
		t.Errorf("engine claim %v", engine)
	}
}

func TestGiteaEngineStorageCanBeEphemeral(t *testing.T) {
	a := baseGiteaPool()
	a.PersistentEngineStorage = bp(false)
	a.EngineStorageSize = sp("50Gi")
	if got := claims(t, a); len(got) != 1 {
		t.Errorf("claims %v", got)
	}
	tpl := giteaPod(t, a)
	storage := volume(t, tpl, "engine-storage")
	if storage == nil || get(t, storage, "emptyDir", "sizeLimit") != "50Gi" {
		t.Errorf("engine storage must be a bounded emptyDir: %v", storage)
	}
	// The kubelet counts an emptyDir against the pod's ephemeral storage limits.
	if got := get(t, container(t, tpl, "engine"), "resources", "limits", "ephemeral-storage"); got != "50Gi" {
		t.Errorf("engine ephemeral limit %v", got)
	}
}

// Host steps run in the runner container; docker run and services: run in the engine, which resolves
// a bind mount's path in its own filesystem.
func TestGiteaHostLabelsShareTheWorkspaceWithTheEngine(t *testing.T) {
	a := baseGiteaPool()
	a.Labels, a.Image = []string{"deploy:host"}, sp("registry.example.com/gitea-host:1")
	a.WorkVolumeSize = sp("30Gi")
	tpl := giteaPod(t, a)
	runner, engine := container(t, tpl, "runner"), container(t, tpl, "engine")
	if runner["image"] != *a.Image || initContainer(t, tpl, "register")["image"] != *a.Image {
		t.Error("the pool's image runs the registration and the daemon")
	}
	for _, name := range []string{"work", "tmp"} {
		if mountPath(runner, name) == "" || mountPath(runner, name) != mountPath(engine, name) {
			t.Errorf("%s: %q in the runner, %q in the engine", name, mountPath(runner, name), mountPath(engine, name))
		}
	}
	if get(t, volume(t, tpl, "work"), "emptyDir", "sizeLimit") != "30Gi" {
		t.Error("the workspace must be bounded")
	}
	if got := get(t, giteaConfig(t, a), "host", "workdir_parent"); got != mountPath(runner, "work") {
		t.Errorf("host jobs work in %v, not on the shared volume", got)
	}
	// A pool of docker labels keeps its workspaces in the engine's own volumes.
	docker := giteaPod(t, baseGiteaPool())
	if volume(t, docker, "work") != nil || has(t, giteaConfig(t, baseGiteaPool()), "host") {
		t.Error("no shared workspace without host labels")
	}
}

func runGiteaPool(t *testing.T, a GiteaRunnerPoolArgs) *mocks {
	t.Helper()
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewGiteaRunnerPool(ctx, "ci", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGiteaRunnerPool(t *testing.T) {
	a := baseGiteaPool()
	m := runGiteaPool(t, a)

	ns := m.find(t, "kubernetes:core/v1:Namespace", "-ns")
	if obj(ns.inputs["metadata"])["name"].StringValue() != "gitea-runners-ryzen" {
		t.Fatalf("namespace %v", ns.inputs)
	}
	secret := m.find(t, "kubernetes:core/v1:Secret", "-registration")
	checkSecretString(t, secret.inputs["stringData"], "token", "registration-secret")
	for _, r := range m.all {
		if r.typ != secret.typ && strings.Contains(allStrings(resource.NewObjectProperty(r.inputs)), "registration-secret") {
			t.Fatalf("the registration token reached %s %s", r.typ, r.name)
		}
	}
	if m.count("kubernetes:core/v1:ConfigMap") != 2 {
		t.Fatal("want the runner's config and podman's")
	}

	sts := m.find(t, "kubernetes:apps/v1:StatefulSet", "-runner")
	meta := obj(sts.inputs["metadata"])
	if meta["name"].StringValue() != "ryzen" || meta["namespace"].StringValue() != "gitea-runners-ryzen" {
		t.Fatalf("metadata %v", meta)
	}
	spec := obj(sts.inputs["spec"])
	if spec["replicas"].NumberValue() != 1 || spec["podManagementPolicy"].StringValue() != "Parallel" {
		t.Fatalf("spec %v", spec)
	}
	retention := obj(spec["persistentVolumeClaimRetentionPolicy"])
	if retention["whenDeleted"].StringValue() != "Retain" || retention["whenScaled"].StringValue() != "Delete" {
		t.Errorf("claim retention %v", retention)
	}
	strategy := obj(spec["updateStrategy"])
	if strategy["type"].StringValue() != "RollingUpdate" || obj(strategy["rollingUpdate"])["partition"].NumberValue() != 0 {
		t.Errorf("update strategy %v", strategy)
	}
	claim := obj(spec["volumeClaimTemplates"].ArrayValue()[0])
	claimSpec := obj(claim["spec"])
	// "data" first: the registration, small, and separate from the image cache.
	if obj(claim["metadata"])["name"].StringValue() != "data" || claimSpec.HasValue("storageClassName") ||
		obj(obj(claimSpec["resources"])["requests"])["storage"].StringValue() != "1Gi" {
		t.Errorf("claim %v", claim)
	}
	template := obj(spec["template"])
	annotations := obj(obj(template["metadata"])["annotations"])
	if annotations[giteaConfigChecksum].StringValue() == "" {
		t.Error("a config change must roll the pods")
	}
	// The typed pod spec Pulumi receives, not only the data it is built from.
	text := allStrings(resource.NewObjectProperty(sts.inputs))
	for _, want := range []string{"register", DefaultGiteaRunnerImage, DefaultGiteaPodmanImage, "SYS_ADMIN", "GITEA_RUNNER_NAME", "metadata.name", "Unmasked"} {
		if !strings.Contains(text, want) {
			t.Errorf("the StatefulSet lacks %q", want)
		}
	}
	pod := obj(template["spec"])
	if pod["hostUsers"].BoolValue() || !pod.HasValue("hostUsers") || len(pod["containers"].ArrayValue()) != 2 {
		t.Errorf("pod %v", pod)
	}
}

func TestGiteaRunnerPoolOptions(t *testing.T) {
	a := baseGiteaPool()
	a.Engine = sp("dind")
	a.Replicas = ip(4)
	a.UpdatePartition = ip(2)
	a.StorageClassName = sp("longhorn")
	a.StorageSize = sp("2Gi")
	m := runGiteaPool(t, a)
	if m.count("kubernetes:core/v1:ConfigMap") != 1 {
		t.Error("dind needs only the runner's config")
	}
	spec := obj(m.find(t, "kubernetes:apps/v1:StatefulSet", "-runner").inputs["spec"])
	claim := obj(obj(spec["volumeClaimTemplates"].ArrayValue()[0])["spec"])
	if spec["replicas"].NumberValue() != 4 || obj(obj(spec["updateStrategy"])["rollingUpdate"])["partition"].NumberValue() != 2 ||
		claim["storageClassName"].StringValue() != "longhorn" || obj(obj(claim["resources"])["requests"])["storage"].StringValue() != "2Gi" {
		t.Errorf("spec %v", spec)
	}
}

func TestAnInvalidGiteaPoolCreatesNothing(t *testing.T) {
	a := baseGiteaPool()
	a.Scope = ""
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewGiteaRunnerPool(ctx, "ci", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err == nil || !strings.Contains(err.Error(), "scope") || len(m.all) != 0 {
		t.Fatalf("err %v, %d resources", err, len(m.all))
	}
}
