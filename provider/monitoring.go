package githubrunners

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strconv"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	networkingv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ARC serves Prometheus metrics from each listener pod and, when asked, from the controller. Nothing
// scrapes or alerts on them by itself, so a dead listener, or jobs waiting with no runner, go
// unnoticed. Monitoring is the Controller's opt-in wiring for that. Both kinds of pod run in the
// controller's namespace.

// Both listeners and the controller serve metrics here, each in its own pod.
const (
	metricsPort     = 8080
	metricsPath     = "/metrics"
	metricsPortName = "metrics"
)

// listenerGracePeriodSeconds is what ARC gives a listener pod that has no template.
const listenerGracePeriodSeconds = 60

const (
	defaultListenerFor = "5m"
	defaultQueuedFor   = "10m"
	dashboardLabel     = "grafana_dashboard"
)

// The app.kubernetes.io/part-of values ARC puts on the listener pods it creates and on its
// controller's pod.
const partOfLabel = "app.kubernetes.io/part-of"

var metricsPartOf = []string{"gha-runner-scale-set", "gha-rs-controller"}

var prometheusDuration = regexp.MustCompile(`^(?:[1-9][0-9]*(?:ms|s|m|h|d|w|y))+$`)

// ControllerMonitoring is what a Controller sets up for a scraper. Everything is off unless set.
type ControllerMonitoring struct {
	ScrapeAnnotations *bool                    `pulumi:"scrapeAnnotations,optional"`
	PodMonitor        *MonitoringPodMonitor    `pulumi:"podMonitor,optional"`
	Alerts            *MonitoringAlerts        `pulumi:"alerts,optional"`
	Dashboard         *MonitoringDashboard     `pulumi:"dashboard,optional"`
	NetworkPolicy     *MonitoringNetworkPolicy `pulumi:"networkPolicy,optional"`
}

// MonitoringPodMonitor is a Prometheus Operator PodMonitor for the controller and listener pods.
type MonitoringPodMonitor struct {
	Labels   map[string]string `pulumi:"labels,optional"`
	Interval *string           `pulumi:"interval,optional"`
}

// MonitoringAlerts is a PrometheusRule for dead listeners and stuck jobs.
type MonitoringAlerts struct {
	Labels      map[string]string `pulumi:"labels,optional"`
	PoolIDs     []string          `pulumi:"poolIds,optional"`
	ListenerFor *string           `pulumi:"listenerFor,optional"`
	QueuedFor   *string           `pulumi:"queuedFor,optional"`
}

// MonitoringDashboard is a ConfigMap Grafana's dashboard sidecar loads.
type MonitoringDashboard struct {
	Namespace pulumi.StringPtrInput `pulumi:"namespace,optional"`
	Labels    map[string]string     `pulumi:"labels,optional"`
}

// MonitoringNetworkPolicy restricts who reaches the metrics ports.
type MonitoringNetworkPolicy struct {
	Allow              []MetricsSource       `pulumi:"allow,optional"`
	GateAgentNamespace pulumi.StringPtrInput `pulumi:"gateAgentNamespace,optional"`
}

// MetricsSource is one scraper: pods matching the selectors.
type MetricsSource struct {
	NamespaceLabels map[string]string `pulumi:"namespaceLabels,optional"`
	PodLabels       map[string]string `pulumi:"podLabels,optional"`
}

func (m *ControllerMonitoring) Annotate(an infer.Annotator) {
	an.Describe(&m.ScrapeAnnotations, "Put prometheus.io/scrape, port and path annotations on the controller pod, for a scraper that discovers pods by them. The listeners' annotations are each Pool's listenerScrapeAnnotations.")
	an.Describe(&m.PodMonitor, "Create a PodMonitor for the controller and listener pods. Off unless set; {} creates it. The cluster must already have the Prometheus Operator's CRDs.")
	an.Describe(&m.Alerts, "Create a PrometheusRule alerting on listeners that are not running and jobs queued without a runner. Off unless set; {} creates it. The cluster must already have the Prometheus Operator's CRDs.")
	an.Describe(&m.Dashboard, "Create a Grafana dashboard ConfigMap of the listener metrics. Off unless set; {} creates it.")
	an.Describe(&m.NetworkPolicy, "Admit only these scrapers, and the GateAgent, to the controller and listener pods' port 8080. Off unless set; without a policy in the namespace the pods are reachable by everything.")
}

func (p *MonitoringPodMonitor) Annotate(an infer.Annotator) {
	an.Describe(&p.Labels, "Labels on the PodMonitor, for a Prometheus that selects monitors by label, e.g. {release: \"kps\"}.")
	an.Describe(&p.Interval, "The scrape interval, e.g. 30s. Defaults to Prometheus's own.")
}

func (a *MonitoringAlerts) Annotate(an infer.Annotator) {
	an.Describe(&a.Labels, "Labels on the PrometheusRule, for a Prometheus that selects rules by label.")
	an.Describe(&a.PoolIDs, "The poolIds expected to have a listener. Each gets an alert for having no listener metrics at all, which covers a listener that never started and a controller that is down.")
	an.Describe(&a.ListenerFor, "How long a listener must be down or missing before its alert fires. Default "+defaultListenerFor+".")
	an.Describe(&a.QueuedFor, "How long jobs must be assigned to a pool without running before the alert fires. Default "+defaultQueuedFor+".")
}

func (d *MonitoringDashboard) Annotate(an infer.Annotator) {
	an.Describe(&d.Namespace, "Existing namespace for the ConfigMap, where Grafana's sidecar looks. Defaults to the controller's namespace.")
	an.Describe(&d.Labels, "Labels on the ConfigMap, added to "+dashboardLabel+": \"1\", which a label of the same name replaces.")
}

func (p *MonitoringNetworkPolicy) Annotate(an infer.Annotator) {
	an.Describe(&p.Allow, "The scrapers allowed to reach port 8080, at least one, e.g. {namespaceLabels: {\"kubernetes.io/metadata.name\": \"monitoring\"}, podLabels: {\"app.kubernetes.io/name\": \"prometheus\"}}.")
	an.Describe(&p.GateAgentNamespace, "The GateAgent's namespace: its agent pods read the listeners' metrics, so they are always admitted. Defaults to "+defaultGateAgentNamespace+".")
}

func (s *MetricsSource) Annotate(an infer.Annotator) {
	an.Describe(&s.NamespaceLabels, "Labels of the scraper's namespace. Unset, podLabels select only pods in the controller's namespace.")
	an.Describe(&s.PodLabels, "Labels of the scraper's pods. Unset, every pod in the matching namespaces.")
}

func checkMonitoring(a ControllerArgs) error {
	m := a.Monitoring
	if m == nil {
		return nil
	}
	if a.ListenerMetrics != nil && !*a.ListenerMetrics {
		return fmt.Errorf("monitoring needs the listener metrics: do not set listenerMetrics to false")
	}
	duration := func(field string, v *string) error {
		if s := orDefault(v, ""); s != "" && !prometheusDuration.MatchString(s) {
			return fmt.Errorf("monitoring.%s %q must be a Prometheus duration such as 30s or 5m", field, s)
		}
		return nil
	}
	if p := m.PodMonitor; p != nil {
		if err := duration("podMonitor.interval", p.Interval); err != nil {
			return err
		}
	}
	if r := m.Alerts; r != nil {
		if err := duration("alerts.listenerFor", r.ListenerFor); err != nil {
			return err
		}
		if err := duration("alerts.queuedFor", r.QueuedFor); err != nil {
			return err
		}
		for _, id := range r.PoolIDs {
			if !poolIDPattern.MatchString(id) {
				return fmt.Errorf("monitoring.alerts.poolIds: %q is not a poolId", id)
			}
		}
	}
	if p := m.NetworkPolicy; p != nil {
		if len(p.Allow) == 0 {
			return fmt.Errorf("monitoring.networkPolicy.allow needs at least one scraper, or the policy would shut every scraper out")
		}
		for _, source := range p.Allow {
			// Kubernetes refuses a peer that selects nothing.
			if len(source.NamespaceLabels) == 0 && len(source.PodLabels) == 0 {
				return fmt.Errorf("every monitoring.networkPolicy.allow entry needs namespaceLabels or podLabels")
			}
		}
	}
	return nil
}

// scrapeAnnotations are the conventional annotations a scraper discovers pods by.
func scrapeAnnotations() map[string]any {
	return map[string]any{
		"prometheus.io/scrape": "true",
		"prometheus.io/port":   strconv.Itoa(metricsPort),
		"prometheus.io/path":   metricsPath,
	}
}

// metricsPodSelector selects the controller and listener pods.
func metricsPodSelector() *metav1.LabelSelectorArgs {
	return &metav1.LabelSelectorArgs{MatchExpressions: metav1.LabelSelectorRequirementArray{
		&metav1.LabelSelectorRequirementArgs{
			Key:      pulumi.String(partOfLabel),
			Operator: pulumi.String("In"),
			Values:   pulumi.ToStringArray(metricsPartOf),
		},
	}}
}

func podMonitorSpec(p MonitoringPodMonitor) map[string]any {
	endpoint := map[string]any{
		"port": metricsPortName,
		"path": metricsPath,
		// The listeners' own namespace label is the pool's namespace. Without this, Prometheus keeps
		// the listener pod's namespace and renames the metric's to exported_namespace.
		"honorLabels": true,
	}
	if interval := orDefault(p.Interval, ""); interval != "" {
		endpoint["interval"] = interval
	}
	return map[string]any{
		"selector": map[string]any{"matchExpressions": []any{
			map[string]any{"key": partOfLabel, "operator": "In", "values": metricsPartOf},
		}},
		"podMetricsEndpoints": []any{endpoint},
	}
}

func alertRule(name, expr, duration, severity, summary, description string) map[string]any {
	return map[string]any{
		"alert":       name,
		"expr":        expr,
		"for":         duration,
		"labels":      map[string]any{"severity": severity},
		"annotations": map[string]any{"summary": summary, "description": description},
	}
}

// prometheusRuleSpec alerts on the two failures that otherwise look like nothing: a listener that is gone,
// and jobs that wait while no runner takes them.
func prometheusRuleSpec(a MonitoringAlerts) map[string]any {
	listenerFor := orDefault(a.ListenerFor, defaultListenerFor)
	rules := []any{
		alertRule("ARCListenerNotRunning", "gha_controller_running_listeners == 0", listenerFor, "critical",
			"ARC listener {{ $labels.name }} is not running",
			"The controller reports no running listener pod, so its pool takes no jobs."),
		alertRule("ARCJobsQueuedWithoutRunner", "gha_assigned_jobs - gha_running_jobs > 0", orDefault(a.QueuedFor, defaultQueuedFor), "warning",
			"Jobs are queued without a runner in {{ $labels.namespace }}",
			"{{ $value }} jobs assigned to {{ $labels.name }} have no runner. Look for pending or failing runner pods."),
	}
	for _, id := range a.PoolIDs {
		namespace := poolNamespacePrefix + id
		// A listener sets gha_max_runners as soon as it has its session with GitHub.
		rules = append(rules, alertRule("ARCListenerMissing", fmt.Sprintf("absent(gha_max_runners{namespace=%q})", namespace), listenerFor, "critical",
			"No ARC listener metrics for "+namespace,
			"The pool's listener never started, is gone, or is not scraped."))
	}
	return map[string]any{"groups": []any{map[string]any{"name": "arc-runners", "rules": rules}}}
}

// dashboardPanel is one graph, of one or more queries, each with its legend.
type dashboardPanel struct {
	title, unit string
	queries     [][2]string
}

func dashboardPanels() []dashboardPanel {
	perPool := func(title, metric string) dashboardPanel {
		return dashboardPanel{title: title, unit: "short", queries: [][2]string{{metric, "{{namespace}} {{name}}"}}}
	}
	// The job histograms carry no scale set label, so they are grouped by organisation. Jobs are
	// sparse, hence the wide window.
	durations := func(title, histogram string) dashboardPanel {
		p := dashboardPanel{title: title, unit: "s"}
		for _, q := range [][2]string{{"0.5", "p50"}, {"0.95", "p95"}} {
			p.queries = append(p.queries, [2]string{
				fmt.Sprintf("histogram_quantile(%s, sum by (le, organization) (rate(%s_bucket[1h])))", q[0], histogram),
				"{{organization}} " + q[1],
			})
		}
		return p
	}
	return []dashboardPanel{
		perPool("Assigned jobs", "gha_assigned_jobs"),
		perPool("Running jobs", "gha_running_jobs"),
		perPool("Registered runners", "gha_registered_runners"),
		perPool("Idle runners", "gha_idle_runners"),
		durations("Job startup duration", "gha_job_startup_duration_seconds"),
		durations("Job execution duration", "gha_job_execution_duration_seconds"),
	}
}

// dashboardJSON lays the panels out two to a row, on the data source chosen in the dashboard.
func dashboardJSON() (string, error) {
	datasource := map[string]any{"type": "prometheus", "uid": "${datasource}"}
	var panels []any
	for i, p := range dashboardPanels() {
		var targets []any
		for j, q := range p.queries {
			targets = append(targets, map[string]any{"refId": string(rune('A' + j)), "datasource": datasource, "expr": q[0], "legendFormat": q[1]})
		}
		panels = append(panels, map[string]any{
			"id": i + 1, "type": "timeseries", "title": p.title, "datasource": datasource,
			"gridPos":     map[string]any{"x": 12 * (i % 2), "y": 8 * (i / 2), "w": 12, "h": 8},
			"fieldConfig": map[string]any{"defaults": map[string]any{"unit": p.unit}},
			"targets":     targets,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"uid": "arc-runners", "title": "GitHub Actions runners", "schemaVersion": 39,
		"time": map[string]any{"from": "now-24h", "to": "now"}, "refresh": "1m",
		"templating": map[string]any{"list": []any{map[string]any{"name": "datasource", "label": "Data source", "type": "datasource", "query": "prometheus"}}},
		"panels":     panels,
	})
	return string(raw), err
}

// metricsNetworkPolicy admits the scrapers and the GateAgent to the controller and listener pods'
// metrics port. It is ingress only.
func metricsNetworkPolicy(p MonitoringNetworkPolicy, namespace string) *networkingv1.NetworkPolicyArgs {
	// Selecting a pod shuts out every source not listed, so the GateAgent, which reads the
	// listeners' metrics, is always listed.
	// Its namespace goes in as an Output, so one from a Controller or a GateAgent carries its
	// dependency and its preview unknown.
	from := networkingv1.NetworkPolicyPeerArray{&networkingv1.NetworkPolicyPeerArgs{
		NamespaceSelector: &metav1.LabelSelectorArgs{MatchLabels: pulumi.StringMap{
			"kubernetes.io/metadata.name": stringOr(p.GateAgentNamespace, defaultGateAgentNamespace),
		}},
		PodSelector: &metav1.LabelSelectorArgs{MatchLabels: gateAgentPodLabels()},
	}}
	for _, source := range p.Allow {
		peer := &networkingv1.NetworkPolicyPeerArgs{}
		if len(source.NamespaceLabels) > 0 {
			peer.NamespaceSelector = &metav1.LabelSelectorArgs{MatchLabels: pulumi.ToStringMap(source.NamespaceLabels)}
		}
		if len(source.PodLabels) > 0 {
			peer.PodSelector = &metav1.LabelSelectorArgs{MatchLabels: pulumi.ToStringMap(source.PodLabels)}
		}
		from = append(from, peer)
	}
	return &networkingv1.NetworkPolicyArgs{
		Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String("arc-metrics"), Namespace: pulumi.String(namespace)},
		Spec: &networkingv1.NetworkPolicySpecArgs{
			PodSelector: metricsPodSelector(),
			PolicyTypes: pulumi.ToStringArray([]string{"Ingress"}),
			Ingress: networkingv1.NetworkPolicyIngressRuleArray{&networkingv1.NetworkPolicyIngressRuleArgs{
				From: from,
				Ports: networkingv1.NetworkPolicyPortArray{
					&networkingv1.NetworkPolicyPortArgs{Protocol: pulumi.String("TCP"), Port: pulumi.Int(metricsPort)},
				},
			}},
		},
	}
}

// newPrometheusObject creates one of the Prometheus Operator's custom resources.
func newPrometheusObject(ctx *pulumi.Context, resourceName, kind, name, namespace string, labels map[string]string, spec map[string]any, opts ...pulumi.ResourceOption) error {
	meta := &metav1.ObjectMetaArgs{Name: pulumi.String(name), Namespace: pulumi.String(namespace)}
	if len(labels) > 0 {
		meta.Labels = pulumi.ToStringMap(labels)
	}
	_, err := apiextensions.NewCustomResource(ctx, resourceName, &apiextensions.CustomResourceArgs{
		ApiVersion:  pulumi.String("monitoring.coreos.com/v1"),
		Kind:        pulumi.String(kind),
		Metadata:    meta,
		OtherFields: kubernetes.UntypedArgs{"spec": spec},
	}, opts...)
	return err
}

// newMonitoring creates whatever the Controller's monitoring block asks for.
func newMonitoring(ctx *pulumi.Context, name, namespace string, m ControllerMonitoring, opts ...pulumi.ResourceOption) error {
	if p := m.NetworkPolicy; p != nil {
		if _, err := networkingv1.NewNetworkPolicy(ctx, name+"-metrics-ingress", metricsNetworkPolicy(*p, namespace), opts...); err != nil {
			return err
		}
	}
	if p := m.PodMonitor; p != nil {
		if err := newPrometheusObject(ctx, name+"-pod-monitor", "PodMonitor", "arc-metrics", namespace, p.Labels, podMonitorSpec(*p), opts...); err != nil {
			return err
		}
	}
	if a := m.Alerts; a != nil {
		if err := newPrometheusObject(ctx, name+"-alerts", "PrometheusRule", "arc-runners", namespace, a.Labels, prometheusRuleSpec(*a), opts...); err != nil {
			return err
		}
	}
	if d := m.Dashboard; d != nil {
		dashboard, err := dashboardJSON()
		if err != nil {
			return err
		}
		labels := map[string]string{dashboardLabel: "1"}
		maps.Copy(labels, d.Labels)
		args := &corev1.ConfigMapArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name:      pulumi.String("arc-runners-dashboard"),
				Namespace: stringOr(d.Namespace, namespace),
				Labels:    pulumi.ToStringMap(labels),
			},
			Data: pulumi.StringMap{"arc-runners.json": pulumi.String(dashboard)},
		}
		if _, err := corev1.NewConfigMap(ctx, name+"-dashboard", args, append(opts, fixedName)...); err != nil {
			return err
		}
	}
	return nil
}
