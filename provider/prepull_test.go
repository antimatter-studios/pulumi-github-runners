package githubrunners

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestPrePullDefaultsToTheRunnerAndPodmanImages(t *testing.T) {
	spec, err := prePullPlan(PrePull{})
	if err != nil {
		t.Fatal(err)
	}
	var images []string
	for _, c := range spec.InitContainers {
		images = append(images, c.Image)
		if !reflect.DeepEqual(c.Command, []string{"true"}) {
			t.Errorf("%s runs %v, want a command that exits at once", c.Name, c.Command)
		}
	}
	if !reflect.DeepEqual(images, []string{DefaultRunnerImage, DefaultPodmanImage}) {
		t.Fatalf("pre-pulled %v", images)
	}
	if spec.Pause.Image != DefaultPauseImage || len(spec.Pause.Command) != 0 {
		t.Fatalf("the pod idles on %+v, want pause", spec.Pause)
	}
	if spec.PriorityClassName != "" {
		t.Fatalf("priority class %q, want none unless given", spec.PriorityClassName)
	}
}

func TestPrePullTakesTheGivenImagesAndPriorityClass(t *testing.T) {
	spec, err := prePullPlan(PrePull{Images: []string{"a@sha256:1", "b@sha256:2", "c@sha256:3"}, PriorityClassName: sp("runners-low")})
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.InitContainers) != 3 || spec.PriorityClassName != "runners-low" {
		t.Fatalf("spec %+v", spec)
	}
	seen := map[string]bool{}
	for i, c := range spec.InitContainers {
		if c.Image != []string{"a@sha256:1", "b@sha256:2", "c@sha256:3"}[i] {
			t.Errorf("init container %d pulls %s", i, c.Image)
		}
		if seen[c.Name] {
			t.Errorf("duplicate container name %s", c.Name)
		}
		seen[c.Name] = true
	}
}

func TestPrePullRefusesAnEmptyImage(t *testing.T) {
	if _, err := prePullPlan(PrePull{Images: []string{"a", " "}}); err == nil || !strings.Contains(err.Error(), "prePull.images[1]") {
		t.Fatalf("err %v", err)
	}
}

func TestPrePullIsOffByDefault(t *testing.T) {
	m := runController(t, ControllerArgs{})
	for _, r := range m.all {
		if r.typ == "kubernetes:apps/v1:DaemonSet" {
			t.Fatalf("unexpected DaemonSet %s", r.name)
		}
	}
}

func TestControllerPrePullDaemonSet(t *testing.T) {
	m := runController(t, ControllerArgs{Namespace: sp("arc"), PrePull: &PrePull{}})
	ds := m.find(t, "kubernetes:apps/v1:DaemonSet", "-"+prePullName)
	meta := ds.inputs["metadata"].ObjectValue()
	if meta["namespace"].StringValue() != "arc" || meta["name"].StringValue() != prePullName {
		t.Fatalf("metadata %v", meta)
	}
	pod := ds.inputs["spec"].ObjectValue()["template"].ObjectValue()["spec"].ObjectValue()
	if pod["automountServiceAccountToken"].BoolValue() {
		t.Fatal("pre-pull pods need no service account token")
	}
	if pod.HasValue("priorityClassName") {
		t.Fatal("no priority class unless one is given")
	}
	inits := pod["initContainers"].ArrayValue()
	if len(inits) != 2 || inits[0].ObjectValue()["image"].StringValue() != DefaultRunnerImage || inits[1].ObjectValue()["image"].StringValue() != DefaultPodmanImage {
		t.Fatalf("init containers %v", inits)
	}
	containers := pod["containers"].ArrayValue()
	if len(containers) != 1 || containers[0].ObjectValue()["image"].StringValue() != DefaultPauseImage {
		t.Fatalf("containers %v", containers)
	}
	for _, c := range append(inits, containers...) {
		checkPrePullContainer(t, c.ObjectValue())
	}
}

func TestControllerPrePullPriorityClass(t *testing.T) {
	m := runController(t, ControllerArgs{PrePull: &PrePull{Images: []string{"x@sha256:1"}, PriorityClassName: sp("runners-low")}})
	pod := m.find(t, "kubernetes:apps/v1:DaemonSet", "-"+prePullName).inputs["spec"].ObjectValue()["template"].ObjectValue()["spec"].ObjectValue()
	if pod["priorityClassName"].StringValue() != "runners-low" {
		t.Fatalf("priority class %v", pod["priorityClassName"])
	}
	if inits := pod["initContainers"].ArrayValue(); len(inits) != 1 || inits[0].ObjectValue()["image"].StringValue() != "x@sha256:1" {
		t.Fatalf("init containers %v", inits)
	}
}

func TestAControllerWithABadPrePullCreatesNothing(t *testing.T) {
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewController(ctx, "arc", ControllerArgs{PrePull: &PrePull{Images: []string{""}}})
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err == nil || len(m.all) != 0 {
		t.Fatalf("err %v, %d resources", err, len(m.all))
	}
}

// checkPrePullContainer: tiny requests, only a small memory limit, and no privileges.
func checkPrePullContainer(t *testing.T, c resource.PropertyMap) {
	t.Helper()
	name := c["name"].StringValue()
	res := c["resources"].ObjectValue()
	requests, limits := res["requests"].ObjectValue(), res["limits"].ObjectValue()
	if requests["cpu"].StringValue() != "1m" || requests["memory"].StringValue() != "8Mi" {
		t.Errorf("%s requests %v", name, requests)
	}
	if len(limits) != 1 || limits["memory"].StringValue() != "32Mi" {
		t.Errorf("%s limits %v, want only a small memory limit", name, limits)
	}
	sc := c["securityContext"].ObjectValue()
	if !sc["runAsNonRoot"].BoolValue() || sc["runAsUser"].NumberValue() == 0 || sc["allowPrivilegeEscalation"].BoolValue() ||
		sc["privileged"].BoolValue() || !sc["readOnlyRootFilesystem"].BoolValue() {
		t.Errorf("%s security context %v", name, sc)
	}
	if drop := sc["capabilities"].ObjectValue()["drop"].ArrayValue(); len(drop) != 1 || drop[0].StringValue() != "ALL" {
		t.Errorf("%s keeps capabilities %v", name, drop)
	}
	if sc["seccompProfile"].ObjectValue()["type"].StringValue() != "RuntimeDefault" {
		t.Errorf("%s seccomp %v", name, sc["seccompProfile"])
	}
}

func runController(t *testing.T, a ControllerArgs) *mocks {
	t.Helper()
	m := &mocks{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := NewController(ctx, "arc", a)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
