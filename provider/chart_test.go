package githubrunners

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The values only matter as the chart renders them, so they are rendered by the pinned chart
// itself, and the runner pod checked in its output. The chart could drop or rewrite a field the
// values set (it does, in its dind mode), and no test of the values alone would notice.
func TestTheChartKeepsThePodAsWritten(t *testing.T) {
	needHelm(t)
	a := basePool()
	a.MaxRunners = ip(4)
	a.Devices = map[string]string{"devic.es/kvm": "1"}
	a.PodResources = &PodResources{CPULimit: sp("4"), MemoryLimit: sp("2Gi")}
	a.CPULimit = sp("8")
	a.SupplementalGroups = []int{993}
	a.TopologySpread = bp(true)
	a.Slots = &PoolSlots{Enabled: bp(true)}
	set := renderScaleSet(t, a, "ryzen")
	if get(t, set, "spec", "runnerGroup") != "ryzen" || get(t, set, "spec", "maxRunners") != float64(4) {
		t.Fatalf("spec %v", set["spec"])
	}
	pod := get(t, set, "spec", "template").(map[string]any)
	want := podTemplate(a)
	for _, field := range []string{"hostUsers", "automountServiceAccountToken", "dnsConfig", "resources", "securityContext", "topologySpreadConstraints"} {
		if g, w := get(t, pod, "spec", field), get(t, want, "spec", field); !jsonEqual(g, w) {
			t.Errorf("%s: chart rendered %v, values had %v", field, g, w)
		}
	}
	for _, name := range []string{"runner", "docker"} {
		if g, w := container(t, pod, name), container(t, want, name); !jsonEqual(g, w) {
			t.Errorf("container %s changed by the chart:\n got %v\nwant %v", name, g, w)
		}
		limits := get(t, container(t, pod, name), "resources", "limits").(map[string]any)
		if limits["cpu"] != nil || limits["memory"] != nil {
			t.Errorf("%s has its own CPU or memory limit: %v", name, limits)
		}
	}
	runner := container(t, pod, "runner")
	if !jsonEqual(runner["command"], wantRunnerCommand) {
		t.Errorf("rendered runner command %v, want %v", runner["command"], wantRunnerCommand)
	}
	if got := len(get(t, pod, "spec", "containers").([]any)); got != 2 {
		t.Errorf("%d containers: the chart added one", got)
	}
}

func needHelm(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("pulls the chart")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
}

// render runs the pinned chart over the values and returns its one object of this kind.
func render(t *testing.T, release, chart, namespace, kind string, chartValues map[string]any, extra ...string) map[string]any {
	t.Helper()
	values := filepath.Join(t.TempDir(), "values.json")
	raw, err := json.Marshal(chartValues)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(values, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"template", release, arcCharts + "/" + chart, "--version", DefaultARCVersion, "--namespace", namespace, "-f", values}
	out, err := exec.Command("helm", append(args, extra...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var found map[string]any
	for _, doc := range strings.Split(string(out), "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			continue
		}
		if obj["kind"] == kind {
			if found != nil {
				t.Fatalf("more than one %s in:\n%s", kind, out)
			}
			found = obj
		}
	}
	if found == nil {
		t.Fatalf("no %s in:\n%s", kind, out)
	}
	return found
}

func renderScaleSet(t *testing.T, a PoolArgs, runnerGroup string) map[string]any {
	t.Helper()
	return render(t, a.ScaleSetName, "gha-runner-scale-set", "arc-runners-acme", "AutoscalingRunnerSet",
		scaleSetValues(a, "arc-github", runnerGroup),
		// What Helm's lookup finds in a real install; template mode cannot look it up.
		"--set", "controllerServiceAccount.namespace=arc-systems", "--set", "controllerServiceAccount.name=arc-gha-rs-controller")
}

func renderController(t *testing.T, a ControllerArgs) map[string]any {
	t.Helper()
	deployment := render(t, "arc", "gha-runner-scale-set-controller", "arc-systems", "Deployment", controllerValues(a))
	return get(t, deployment, "spec", "template").(map[string]any)
}

func TestTheChartSetsTheControllersPriorityAndResources(t *testing.T) {
	needHelm(t)
	pod := renderController(t, ControllerArgs{
		PriorityClassName: sp("infra-critical"),
		Resources:         &ContainerResources{CPURequest: sp("20m"), MemoryRequest: sp("96Mi")},
	})
	if got := get(t, pod, "spec", "priorityClassName"); got != "infra-critical" {
		t.Errorf("priorityClassName %v", got)
	}
	// The block replaces the defaults whole: no limit was asked for, so none is left.
	want := map[string]any{"requests": map[string]any{"cpu": "20m", "memory": "96Mi"}}
	if got := get(t, container(t, pod, "manager"), "resources"); !jsonEqual(got, want) {
		t.Errorf("manager resources %v, want %v", got, want)
	}

	pod = renderController(t, ControllerArgs{Resources: &ContainerResources{
		CPURequest: sp("20m"), MemoryRequest: sp("96Mi"), CPULimit: sp("1"), MemoryLimit: sp("1Gi"),
	}})
	want["limits"] = map[string]any{"cpu": "1", "memory": "1Gi"}
	if got := get(t, container(t, pod, "manager"), "resources"); !jsonEqual(got, want) {
		t.Errorf("manager resources %v, want %v", got, want)
	}
}

func TestTheChartKeepsTheControllersDefaults(t *testing.T) {
	needHelm(t)
	pod := renderController(t, ControllerArgs{})
	if got, ok := pod["spec"].(map[string]any)["priorityClassName"]; ok {
		t.Errorf("priorityClassName %v, want none", got)
	}
	want := map[string]any{
		"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
		"limits":   map[string]any{"cpu": "500m", "memory": "512Mi"},
	}
	if got := get(t, container(t, pod, "manager"), "resources"); !jsonEqual(got, want) {
		t.Errorf("manager resources %v, want %v", got, want)
	}
}

func TestTheChartSetsTheListenersPriorityAndResources(t *testing.T) {
	needHelm(t)
	a := basePool()
	a.ListenerPriorityClassName = sp("infra-critical")
	a.ListenerResources = &ContainerResources{CPURequest: sp("10m"), MemoryRequest: sp("64Mi"), MemoryLimit: sp("128Mi")}
	set := renderScaleSet(t, a, "")
	pod := get(t, set, "spec", "listenerTemplate").(map[string]any)
	if got := get(t, pod, "spec", "priorityClassName"); got != "infra-critical" {
		t.Errorf("listener priorityClassName %v", got)
	}
	// ARC takes this field from the template even when unset, so its own 60 s has to be restated.
	if got := get(t, pod, "spec", "terminationGracePeriodSeconds"); got != float64(60) {
		t.Errorf("listener terminationGracePeriodSeconds %v", got)
	}
	want := map[string]any{
		"requests": map[string]any{"cpu": "10m", "memory": "64Mi"},
		"limits":   map[string]any{"memory": "128Mi"},
	}
	if got := get(t, container(t, pod, "listener"), "resources"); !jsonEqual(got, want) {
		t.Errorf("listener resources %v, want %v", got, want)
	}
	if got := len(get(t, pod, "spec", "containers").([]any)); got != 1 {
		t.Errorf("%d listener containers, want 1", got)
	}
	// The listener's priority is not the runners'.
	if got, ok := get(t, set, "spec", "template", "spec").(map[string]any)["priorityClassName"]; ok {
		t.Errorf("runner priorityClassName %v, want none", got)
	}
}

// Either input alone makes a template the CRD accepts: containers is required, with the listener's name.
func TestTheChartTakesEitherListenerInputAlone(t *testing.T) {
	needHelm(t)
	a := basePool()
	a.ListenerPriorityClassName = sp("infra-critical")
	pod := get(t, renderScaleSet(t, a, ""), "spec", "listenerTemplate").(map[string]any)
	if _, ok := container(t, pod, "listener")["resources"]; ok {
		t.Errorf("listener resources set without being asked for: %v", pod)
	}
	a = basePool()
	a.ListenerResources = &ContainerResources{MemoryRequest: sp("64Mi")}
	pod = get(t, renderScaleSet(t, a, ""), "spec", "listenerTemplate").(map[string]any)
	if _, ok := pod["spec"].(map[string]any)["priorityClassName"]; ok {
		t.Errorf("listener priorityClassName set without being asked for: %v", pod)
	}
	if got := get(t, container(t, pod, "listener"), "resources", "requests", "memory"); got != "64Mi" {
		t.Errorf("listener memory request %v", got)
	}
}

func TestTheChartHasNoListenerTemplateByDefault(t *testing.T) {
	needHelm(t)
	set := renderScaleSet(t, basePool(), "")
	if got, ok := set["spec"].(map[string]any)["listenerTemplate"]; ok {
		t.Errorf("listenerTemplate %v, want none", got)
	}
}

// The monitoring values only matter as the pinned charts render them: the controller's metrics port,
// which the PodMonitor scrapes by name, its pod labels, and the listener template the scale set
// hands to the controller.
func TestTheChartsRenderMonitoring(t *testing.T) {
	needHelm(t)
	pod := renderController(t, ControllerArgs{Monitoring: &ControllerMonitoring{ScrapeAnnotations: bp(true)}})
	if got := get(t, pod, "metadata", "labels", "app.kubernetes.io/part-of"); got != controllerPodLabels["app.kubernetes.io/part-of"] {
		t.Errorf("controller pod part-of %v", got)
	}
	if got := get(t, pod, "metadata", "annotations", "prometheus.io/port"); got != "8080" {
		t.Errorf("controller scrape port %v", got)
	}
	manager := get(t, pod, "spec", "containers", 0)
	if got := get(t, manager, "ports"); !jsonEqual(got, []any{map[string]any{"containerPort": 8080, "name": "metrics", "protocol": "TCP"}}) {
		t.Errorf("controller ports %v", got)
	}

	a := basePool()
	a.ListenerScrapeAnnotations = bp(true)
	set := renderScaleSet(t, a, "")
	if got := get(t, set, "spec", "listenerTemplate"); !jsonEqual(got, scaleSetValues(a, "arc-github", "")["listenerTemplate"]) {
		t.Errorf("rendered listener template %v", got)
	}
}

// The agent's marker has to reach the AutoscalingRunnerSet itself, where the agent lists it, and the
// live field has to start at the declared maxRunners.
func TestTheChartMarksTheScaleSetAsTheAgents(t *testing.T) {
	needHelm(t)
	a := basePool()
	a.MaxRunners = ip(6)
	a.MaxRunnersAgent = &PoolMaxRunnersAgent{Enabled: bp(true)}
	set := renderScaleSet(t, a, "ryzen")
	if get(t, set, "spec", "maxRunners") != float64(6) {
		t.Fatalf("maxRunners %v, want the start value 6", get(t, set, "spec", "maxRunners"))
	}
	if got := get(t, set, "metadata", "annotations", maxRunnersAgentAnnotation); got != "true" {
		t.Fatalf("marker annotation %v, want \"true\"", got)
	}
	if has(t, set, "metadata", "annotations", "runners.antimatter.studios/max-runners-ceiling") {
		t.Fatal("a ceiling annotation: the agent has no cap")
	}
}

// The listener's priority, resources and scrape annotations all reach the rendered listener template.
func TestTheChartRendersTheWholeListenerTemplate(t *testing.T) {
	needHelm(t)
	a := basePool()
	a.ListenerPriorityClassName = sp("infra-critical")
	a.ListenerResources = &ContainerResources{MemoryRequest: sp("64Mi")}
	a.ListenerScrapeAnnotations = bp(true)
	pod := get(t, renderScaleSet(t, a, ""), "spec", "listenerTemplate").(map[string]any)
	if got := get(t, pod, "spec", "priorityClassName"); got != "infra-critical" {
		t.Errorf("listener priorityClassName %v", got)
	}
	if got := get(t, container(t, pod, "listener"), "resources", "requests", "memory"); got != "64Mi" {
		t.Errorf("listener memory request %v", got)
	}
	if got := get(t, pod, "metadata", "annotations", "prometheus.io/scrape"); got != "true" {
		t.Errorf("listener scrape annotation %v", got)
	}
}

func jsonEqual(a, b any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	var x, y any
	_ = json.Unmarshal(ra, &x)
	_ = json.Unmarshal(rb, &y)
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}
