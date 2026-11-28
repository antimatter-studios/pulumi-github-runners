package githubrunners

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	testPodMonitorType     = "kubernetes:monitoring.coreos.com/v1:PodMonitor"
	testPrometheusRuleType = "kubernetes:monitoring.coreos.com/v1:PrometheusRule"
	testConfigMapType      = "kubernetes:core/v1:ConfigMap"
)

// The labels ARC 0.15.1 puts on the listener pods it creates and on its controller's pod.
var (
	listenerPodLabels   = map[string]string{"app.kubernetes.io/part-of": "gha-runner-scale-set", "app.kubernetes.io/component": "runner-scale-set-listener"}
	controllerPodLabels = map[string]string{"app.kubernetes.io/part-of": "gha-rs-controller", "app.kubernetes.io/component": "controller-manager"}
)

// selects reports whether a selector made of one In expression matches the labels.
func selects(t *testing.T, selector resource.PropertyValue, labels map[string]string) bool {
	t.Helper()
	expressions := obj(selector)["matchExpressions"].ArrayValue()
	if len(expressions) != 1 || obj(expressions[0])["operator"].StringValue() != "In" {
		t.Fatalf("selector %v, want one In expression", selector)
	}
	got, ok := labels[obj(expressions[0])["key"].StringValue()]
	if !ok {
		return false
	}
	for _, v := range obj(expressions[0])["values"].ArrayValue() {
		if v.StringValue() == got {
			return true
		}
	}
	return false
}

func controllerReleaseValues(t *testing.T, m *mocks) resource.PropertyMap {
	t.Helper()
	return obj(m.find(t, "kubernetes:helm.sh/v3:Release", "-controller").inputs["values"])
}

func TestMonitoringIsOffByDefault(t *testing.T) {
	m := runController(t, ControllerArgs{})
	for _, typ := range []string{testPodMonitorType, testPrometheusRuleType, testConfigMapType, testNetworkPolicyType} {
		if n := m.count(typ); n != 0 {
			t.Errorf("%d %s without monitoring", n, typ)
		}
	}
	values := controllerReleaseValues(t, m)
	if values.HasValue("podAnnotations") || obj(values["metrics"])["controllerManagerAddr"].StringValue() != "0" {
		t.Fatalf("values %v", values)
	}
}

// An empty block only turns the controller's own metrics on: every object it could create needs a
// CRD, a Grafana or a scraper the cluster may not have.
func TestEmptyMonitoringOnlyServesControllerMetrics(t *testing.T) {
	m := runController(t, ControllerArgs{Monitoring: &ControllerMonitoring{}})
	for _, typ := range []string{testPodMonitorType, testPrometheusRuleType, testConfigMapType, testNetworkPolicyType} {
		if n := m.count(typ); n != 0 {
			t.Errorf("%d %s from an empty monitoring block", n, typ)
		}
	}
	values := controllerReleaseValues(t, m)
	metrics := obj(values["metrics"])
	if metrics["controllerManagerAddr"].StringValue() != ":8080" || metrics["listenerAddr"].StringValue() != ":8080" ||
		metrics["listenerEndpoint"].StringValue() != "/metrics" || values.HasValue("podAnnotations") {
		t.Fatalf("values %v", values)
	}
}

func TestMonitoringScrapeAnnotationsOnTheController(t *testing.T) {
	m := runController(t, ControllerArgs{Monitoring: &ControllerMonitoring{ScrapeAnnotations: bp(true)}})
	annotations := obj(controllerReleaseValues(t, m)["podAnnotations"])
	for k, want := range map[resource.PropertyKey]string{"prometheus.io/scrape": "true", "prometheus.io/port": "8080", "prometheus.io/path": "/metrics"} {
		if !annotations.HasValue(k) || annotations[k].StringValue() != want {
			t.Errorf("annotation %s = %v, want %q", k, annotations[k], want)
		}
	}
}

func TestPoolListenerScrapeAnnotations(t *testing.T) {
	if _, ok := scaleSetValues(basePool(), "arc-github", "")["listenerTemplate"]; ok {
		t.Fatal("no listener template unless asked for")
	}
	a := basePool()
	a.ListenerScrapeAnnotations = bp(true)
	v := scaleSetValues(a, "arc-github", "")
	annotations := get(t, v, "listenerTemplate", "metadata", "annotations")
	want := map[string]any{"prometheus.io/scrape": "true", "prometheus.io/port": "8080", "prometheus.io/path": "/metrics"}
	if !jsonEqual(annotations, want) {
		t.Errorf("annotations %v", annotations)
	}
	// ARC replaces the listener pod's grace period with the template's, set or not, so the template
	// repeats ARC's own 60 seconds.
	if grace := get(t, v, "listenerTemplate", "spec", "terminationGracePeriodSeconds"); grace != float64(60) {
		t.Errorf("grace period %v", grace)
	}
	// The CRD requires containers; naming the listener alone changes nothing in it.
	if containers := get(t, v, "listenerTemplate", "spec", "containers"); !jsonEqual(containers, []any{map[string]any{"name": "listener"}}) {
		t.Errorf("containers %v", containers)
	}
}

func TestMonitoringPodMonitor(t *testing.T) {
	m := runController(t, ControllerArgs{Namespace: sp("arc"), Monitoring: &ControllerMonitoring{
		PodMonitor: &MonitoringPodMonitor{Labels: map[string]string{"release": "kps"}, Interval: sp("30s")},
	}})
	monitor := m.find(t, testPodMonitorType, "-pod-monitor")
	meta := obj(monitor.inputs["metadata"])
	if meta["namespace"].StringValue() != "arc" || obj(meta["labels"])["release"].StringValue() != "kps" {
		t.Fatalf("metadata %v", meta)
	}
	spec := obj(monitor.inputs["spec"])
	// No namespaceSelector: the monitor's own namespace, where the controller and listeners run.
	if spec.HasValue("namespaceSelector") {
		t.Errorf("namespaceSelector %v", spec["namespaceSelector"])
	}
	for _, labels := range []map[string]string{listenerPodLabels, controllerPodLabels} {
		if !selects(t, spec["selector"], labels) {
			t.Errorf("the monitor misses pods labelled %v", labels)
		}
	}
	if selects(t, spec["selector"], map[string]string{"app.kubernetes.io/part-of": "other"}) {
		t.Error("the monitor selects unrelated pods")
	}
	endpoints := spec["podMetricsEndpoints"].ArrayValue()
	if len(endpoints) != 1 {
		t.Fatalf("endpoints %v", endpoints)
	}
	e := obj(endpoints[0])
	// honorLabels keeps the listener's namespace label (the pool's namespace), which the alerts use;
	// without it Prometheus renames it to exported_namespace.
	if e["port"].StringValue() != "metrics" || e["path"].StringValue() != "/metrics" ||
		!e["honorLabels"].BoolValue() || e["interval"].StringValue() != "30s" {
		t.Fatalf("endpoint %v", e)
	}
	if m.count(testPrometheusRuleType) != 0 {
		t.Error("a PodMonitor does not imply alerts")
	}
}

func alertRules(t *testing.T, m *mocks) []resource.PropertyMap {
	t.Helper()
	spec := obj(m.find(t, testPrometheusRuleType, "-alerts").inputs["spec"])
	groups := spec["groups"].ArrayValue()
	if len(groups) != 1 {
		t.Fatalf("groups %v", groups)
	}
	var rules []resource.PropertyMap
	for _, r := range obj(groups[0])["rules"].ArrayValue() {
		rules = append(rules, obj(r))
	}
	return rules
}

func TestMonitoringAlerts(t *testing.T) {
	m := runController(t, ControllerArgs{Monitoring: &ControllerMonitoring{Alerts: &MonitoringAlerts{}}})
	meta := obj(m.find(t, testPrometheusRuleType, "-alerts").inputs["metadata"])
	if meta["namespace"].StringValue() != "arc-systems" {
		t.Fatalf("metadata %v", meta)
	}
	var got []string
	for _, r := range alertRules(t, m) {
		got = append(got, r["alert"].StringValue()+" | "+r["expr"].StringValue()+" | "+r["for"].StringValue()+" | "+obj(r["labels"])["severity"].StringValue())
		if obj(r["annotations"])["summary"].StringValue() == "" {
			t.Errorf("%s has no summary", r["alert"].StringValue())
		}
	}
	want := []string{
		"ARCListenerNotRunning | gha_controller_running_listeners == 0 | 5m | critical",
		"ARCJobsQueuedWithoutRunner | gha_assigned_jobs - gha_running_jobs > 0 | 10m | warning",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rules:\n%s", strings.Join(got, "\n"))
	}
}

// A listener that never started has no series at all, so only naming the expected pools finds it.
func TestMonitoringAlertsForExpectedPools(t *testing.T) {
	m := runController(t, ControllerArgs{Monitoring: &ControllerMonitoring{Alerts: &MonitoringAlerts{
		Labels: map[string]string{"release": "kps"}, PoolIDs: []string{"acme", "zed"}, ListenerFor: sp("2m"), QueuedFor: sp("1h30m"),
	}}})
	meta := obj(m.find(t, testPrometheusRuleType, "-alerts").inputs["metadata"])
	if obj(meta["labels"])["release"].StringValue() != "kps" {
		t.Fatalf("metadata %v", meta)
	}
	var got []string
	for _, r := range alertRules(t, m) {
		got = append(got, r["alert"].StringValue()+" | "+r["expr"].StringValue()+" | "+r["for"].StringValue())
	}
	want := []string{
		"ARCListenerNotRunning | gha_controller_running_listeners == 0 | 2m",
		"ARCJobsQueuedWithoutRunner | gha_assigned_jobs - gha_running_jobs > 0 | 1h30m",
		`ARCListenerMissing | absent(gha_max_runners{namespace="arc-runners-acme"}) | 2m`,
		`ARCListenerMissing | absent(gha_max_runners{namespace="arc-runners-zed"}) | 2m`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rules:\n%s", strings.Join(got, "\n"))
	}
}

func TestMonitoringDashboard(t *testing.T) {
	m := runController(t, ControllerArgs{Monitoring: &ControllerMonitoring{Dashboard: &MonitoringDashboard{}}})
	cm := m.find(t, testConfigMapType, "-dashboard")
	meta := obj(cm.inputs["metadata"])
	if meta["namespace"].StringValue() != "arc-systems" || obj(meta["labels"])["grafana_dashboard"].StringValue() != "1" {
		t.Fatalf("metadata %v", meta)
	}
	var dashboard struct {
		UID    string `json:"uid"`
		Title  string `json:"title"`
		Panels []struct {
			ID      int    `json:"id"`
			Title   string `json:"title"`
			GridPos struct{ X, Y, W, H int }
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(obj(cm.inputs["data"])["arc-runners.json"].StringValue()), &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.UID == "" || dashboard.Title == "" {
		t.Fatalf("dashboard %+v", dashboard)
	}
	var exprs strings.Builder
	ids, cells := map[int]bool{}, map[[2]int]bool{}
	for _, p := range dashboard.Panels {
		cell := [2]int{p.GridPos.X, p.GridPos.Y}
		if ids[p.ID] || cells[cell] || p.Title == "" || p.GridPos.W == 0 || p.GridPos.H == 0 || len(p.Targets) == 0 {
			t.Errorf("panel %+v", p)
		}
		ids[p.ID], cells[cell] = true, true
		for _, target := range p.Targets {
			exprs.WriteString(target.Expr + "\n")
		}
	}
	for _, metric := range []string{
		"gha_assigned_jobs", "gha_running_jobs", "gha_registered_runners", "gha_idle_runners",
		"gha_job_startup_duration_seconds_bucket", "gha_job_execution_duration_seconds_bucket",
	} {
		if !strings.Contains(exprs.String(), metric) {
			t.Errorf("no panel reads %s", metric)
		}
	}
}

func TestMonitoringDashboardNamespaceAndLabels(t *testing.T) {
	m := runController(t, ControllerArgs{Monitoring: &ControllerMonitoring{Dashboard: &MonitoringDashboard{
		Namespace: pulumi.StringPtr("monitoring"), Labels: map[string]string{"grafana_dashboard": "yes", "team": "ci"},
	}}})
	meta := obj(m.find(t, testConfigMapType, "-dashboard").inputs["metadata"])
	labels := obj(meta["labels"])
	if meta["namespace"].StringValue() != "monitoring" || labels["grafana_dashboard"].StringValue() != "yes" || labels["team"].StringValue() != "ci" {
		t.Fatalf("metadata %v", meta)
	}
}

// Selecting a pod in an ingress policy shuts out everyone the policy does not list, so the gate
// agent, which reads the listeners' metrics, must stay on the list.
func TestMonitoringNetworkPolicyAdmitsTheScraperAndTheGateAgent(t *testing.T) {
	m := &mocks{}
	agent := testGateAgentArgs()
	agent.Namespace = pulumi.StringPtr("gate")
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		if _, err := NewController(ctx, "arc", ControllerArgs{Monitoring: &ControllerMonitoring{
			NetworkPolicy: &MonitoringNetworkPolicy{
				GateAgentNamespace: pulumi.StringPtr("gate"),
				Allow: []MetricsSource{{
					NamespaceLabels: map[string]string{"kubernetes.io/metadata.name": "monitoring"},
					PodLabels:       map[string]string{"app.kubernetes.io/name": "prometheus"},
				}},
			},
		}}); err != nil {
			return err
		}
		_, err := NewGateAgent(ctx, "homelab", agent)
		return err
	}, pulumi.WithMocks("project", "stack", m))
	if err != nil {
		t.Fatal(err)
	}
	policy := m.find(t, testNetworkPolicyType, "-metrics-ingress")
	if obj(policy.inputs["metadata"])["namespace"].StringValue() != "arc-systems" {
		t.Fatalf("metadata %v", policy.inputs["metadata"])
	}
	spec := obj(policy.inputs["spec"])
	if allStrings(spec["policyTypes"]) != "Ingress\n" || spec.HasValue("egress") {
		t.Fatalf("policy must restrict ingress only: %v", spec)
	}
	for _, labels := range []map[string]string{listenerPodLabels, controllerPodLabels} {
		if !selects(t, spec["podSelector"], labels) {
			t.Errorf("the policy misses pods labelled %v", labels)
		}
	}
	rules := spec["ingress"].ArrayValue()
	if len(rules) != 1 {
		t.Fatalf("ingress %v", rules)
	}
	ports := obj(rules[0])["ports"].ArrayValue()
	if len(ports) != 1 || obj(ports[0])["protocol"].StringValue() != "TCP" || obj(ports[0])["port"].NumberValue() != 8080 {
		t.Fatalf("ports %v", ports)
	}
	from := obj(rules[0])["from"].ArrayValue()
	if len(from) != 2 {
		t.Fatalf("from %v, want the gate agent and the scraper", from)
	}
	gate := obj(from[0])
	agentPods := obj(obj(obj(obj(m.find(t, "kubernetes:apps/v1:Deployment", "-agent").inputs["spec"])["template"])["metadata"])["labels"])
	namespace := resource.PropertyMap{"kubernetes.io/metadata.name": resource.NewStringProperty("gate")}
	if !matchLabels(obj(gate["namespaceSelector"]), namespace) || !matchLabels(obj(gate["podSelector"]), agentPods) {
		t.Errorf("gate agent peer %v does not select its pods", gate)
	}
	scraper := obj(from[1])
	if allStrings(obj(scraper["namespaceSelector"])["matchLabels"]) != "monitoring\n" ||
		allStrings(obj(scraper["podSelector"])["matchLabels"]) != "prometheus\n" {
		t.Errorf("scraper peer %v", scraper)
	}
}

func TestMonitoringNetworkPolicyDefaultsToTheDefaultGateAgentNamespace(t *testing.T) {
	m := runController(t, ControllerArgs{Namespace: sp("arc"), Monitoring: &ControllerMonitoring{
		NetworkPolicy: &MonitoringNetworkPolicy{Allow: []MetricsSource{{PodLabels: map[string]string{"app": "scraper"}}}},
	}})
	spec := obj(m.find(t, testNetworkPolicyType, "-metrics-ingress").inputs["spec"])
	from := obj(spec["ingress"].ArrayValue()[0])["from"].ArrayValue()
	if allStrings(obj(obj(from[0])["namespaceSelector"])["matchLabels"]) != "arc-systems\n" {
		t.Errorf("gate agent peer %v", from[0])
	}
	if obj(from[1]).HasValue("namespaceSelector") {
		t.Errorf("peer %v must not select namespaces", from[1])
	}
}

func TestInvalidMonitoringCreatesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		args       ControllerArgs
	}{
		{"listener metrics off", "listenerMetrics", ControllerArgs{ListenerMetrics: bp(false), Monitoring: &ControllerMonitoring{}}},
		{"policy with no source", "networkPolicy.allow", ControllerArgs{Monitoring: &ControllerMonitoring{NetworkPolicy: &MonitoringNetworkPolicy{}}}},
		{"empty source", "networkPolicy.allow", ControllerArgs{Monitoring: &ControllerMonitoring{NetworkPolicy: &MonitoringNetworkPolicy{Allow: []MetricsSource{{}}}}}},
		{"pool id", "alerts.poolIds", ControllerArgs{Monitoring: &ControllerMonitoring{Alerts: &MonitoringAlerts{PoolIDs: []string{`a"}`}}}}},
		{"listenerFor", "alerts.listenerFor", ControllerArgs{Monitoring: &ControllerMonitoring{Alerts: &MonitoringAlerts{ListenerFor: sp("soon")}}}},
		{"queuedFor", "alerts.queuedFor", ControllerArgs{Monitoring: &ControllerMonitoring{Alerts: &MonitoringAlerts{QueuedFor: sp("10")}}}},
		{"interval", "podMonitor.interval", ControllerArgs{Monitoring: &ControllerMonitoring{PodMonitor: &MonitoringPodMonitor{Interval: sp("30 s")}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &mocks{}
			err := pulumi.RunErr(func(ctx *pulumi.Context) error {
				_, err := NewController(ctx, "arc", tc.args)
				return err
			}, pulumi.WithMocks("project", "stack", m))
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(m.all) != 0 {
				t.Fatalf("err %v, %d resources", err, len(m.all))
			}
		})
	}
}

// The listener's priority, resources and scrape annotations share one listener template.
func TestListenerTemplateCarriesPriorityResourcesAndScrapeAnnotations(t *testing.T) {
	a := basePool()
	a.ListenerPriorityClassName = sp("infra-critical")
	a.ListenerResources = &ContainerResources{MemoryRequest: sp("64Mi")}
	a.ListenerScrapeAnnotations = bp(true)
	v := scaleSetValues(a, "arc-github", "")
	if got := get(t, v, "listenerTemplate", "metadata", "annotations", "prometheus.io/port"); got != "8080" {
		t.Errorf("scrape port %v", got)
	}
	if got := get(t, v, "listenerTemplate", "spec", "priorityClassName"); got != "infra-critical" {
		t.Errorf("priorityClassName %v", got)
	}
	if got := get(t, v, "listenerTemplate", "spec", "terminationGracePeriodSeconds"); got != float64(60) {
		t.Errorf("grace period %v", got)
	}
	want := []any{map[string]any{"name": "listener", "resources": map[string]any{"requests": map[string]any{"memory": "64Mi"}}}}
	if got := get(t, v, "listenerTemplate", "spec", "containers"); !jsonEqual(got, want) {
		t.Errorf("containers %v", got)
	}
}

// Monitoring works in a namespace the Controller did not create.
func TestMonitoringInAnExistingNamespace(t *testing.T) {
	m := runController(t, ControllerArgs{Namespace: sp("arc-systems"), CreateNamespace: bp(false), Monitoring: &ControllerMonitoring{
		NetworkPolicy: &MonitoringNetworkPolicy{Allow: []MetricsSource{{PodLabels: map[string]string{"app": "scraper"}}}},
	}})
	if n := m.count("kubernetes:core/v1:Namespace"); n != 0 {
		t.Fatalf("%d Namespaces, want none", n)
	}
	policy := m.find(t, testNetworkPolicyType, "-metrics-ingress")
	if got := obj(policy.inputs["metadata"])["namespace"].StringValue(); got != "arc-systems" {
		t.Errorf("metrics policy in namespace %q", got)
	}
}
