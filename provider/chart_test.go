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
	if testing.Short() {
		t.Skip("pulls the chart")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	a := basePool()
	a.MaxRunners = ip(4)
	a.Devices = map[string]string{"devic.es/kvm": "1"}
	a.PodResources = &PodResources{CPULimit: sp("4"), MemoryLimit: sp("2Gi")}
	a.CPULimit = sp("8")
	a.SupplementalGroups = []int{993}
	a.TopologySpread = bp(true)
	dir := t.TempDir()
	values := filepath.Join(dir, "values.json")
	raw, err := json.Marshal(scaleSetValues(a, "arc-github", "ryzen"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(values, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("helm", "template", a.ScaleSetName, arcCharts+"/gha-runner-scale-set",
		"--version", DefaultARCVersion, "--namespace", "arc-runners-acme", "-f", values,
		// What Helm's lookup finds in a real install; template mode cannot look it up.
		"--set", "controllerServiceAccount.namespace=arc-systems", "--set", "controllerServiceAccount.name=arc-gha-rs-controller")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var set map[string]any
	for _, doc := range strings.Split(string(out), "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			continue
		}
		if obj["kind"] == "AutoscalingRunnerSet" {
			set = obj
		}
	}
	if set == nil {
		t.Fatalf("no AutoscalingRunnerSet in:\n%s", out)
	}
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
