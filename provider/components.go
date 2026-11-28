package githubrunners

import (
	"encoding/json"
	"errors"
	"strconv"

	github "github.com/pulumi/pulumi-github/sdk/v6/go/github"
	"github.com/pulumi/pulumi-go-provider/infer"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	networkingv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// DefaultARCVersion is the Actions Runner Controller release both charts are pinned to together:
// the scale set's CRDs are the controller's.
const DefaultARCVersion = "0.15.1"

const arcCharts = "oci://ghcr.io/actions/actions-runner-controller-charts"

// poolNamespaceLabel marks a Pool's namespace with its poolId. The GateAgent's ingress policy admits
// namespaces carrying it.
const poolNamespaceLabel = "github-runners.antimatter.studios/pool"

// poolNamespacePrefix and a poolId name a Pool's namespace.
const poolNamespacePrefix = "arc-runners-"

// DefaultDevicePluginImage is squat/generic-device-plugin, pinned by digest, which offers host
// devices as schedulable resources named devic.es/<name>.
const DefaultDevicePluginImage = "ghcr.io/squat/generic-device-plugin@sha256:8dc84958301f054bf72d9753a7a2c9af3d6642a9844b34d1171b3f2cdb4a15f6"

// ---- Controller ----

// ControllerArgs are a Controller's inputs: one per cluster.
type ControllerArgs struct {
	Namespace         *string         `pulumi:"namespace,optional"`
	CreateNamespace   *bool           `pulumi:"createNamespace,optional"`
	Version           *string         `pulumi:"version,optional"`
	ListenerMetrics   *bool           `pulumi:"listenerMetrics,optional"`
	Devices           []DeviceOffer   `pulumi:"devices,optional"`
	DevicePluginImage *string         `pulumi:"devicePluginImage,optional"`
	PrePull           *PrePull        `pulumi:"prePull,optional"`
	RegistryMirror    *RegistryMirror `pulumi:"registryMirror,optional"`

	PriorityClassName *string             `pulumi:"priorityClassName,optional"`
	Resources         *ContainerResources `pulumi:"resources,optional"`

	Monitoring *ControllerMonitoring `pulumi:"monitoring,optional"`
}

// ContainerResources are one container's requests and limits.
type ContainerResources struct {
	CPURequest    *string `pulumi:"cpuRequest,optional"`
	MemoryRequest *string `pulumi:"memoryRequest,optional"`
	CPULimit      *string `pulumi:"cpuLimit,optional"`
	MemoryLimit   *string `pulumi:"memoryLimit,optional"`
}

// values is the Kubernetes resources block holding what is set, and nothing else.
func (r ContainerResources) values() map[string]any {
	out := map[string]any{}
	if req := quantities(r.CPURequest, r.MemoryRequest); len(req) > 0 {
		out["requests"] = req
	}
	if lim := quantities(r.CPULimit, r.MemoryLimit); len(lim) > 0 {
		out["limits"] = lim
	}
	return out
}

// DeviceOffer is a host device offered to pods as the resource devic.es/<name>.
type DeviceOffer struct {
	Name  string   `pulumi:"name"`
	Paths []string `pulumi:"paths"`
	Count *int     `pulumi:"count,optional"`
}

func (a *ControllerArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Namespace, "Where the controller runs. Default arc-systems.")
	an.Describe(&a.CreateNamespace, "Create the namespace. Default true. False uses an existing namespace as it is: it must exist. Setting false on a Controller that created its namespace deletes that namespace and everything in it.")
	an.Describe(&a.Version, "The ARC chart version. Default "+DefaultARCVersion+".")
	an.Describe(&a.ListenerMetrics, "Serve the listeners' metrics on :8080/metrics, which the runner-gate reads. Default true.")
	an.Describe(&a.Devices, "Host devices offered to runner pods, e.g. {name: fuse, paths: [/dev/fuse]} as devic.es/fuse.")
	an.Describe(&a.Monitoring, "Scraping, alerts, a dashboard and a metrics NetworkPolicy. Off unless set; setting it also serves the controller's own metrics on :8080/metrics.")
	an.Describe(&a.PrePull, "Keep runner images on every node with a DaemonSet, so pods scaled from zero do not wait for them to pull. Off unless set; {} pre-pulls the default runner and podman images.")
	an.Describe(&a.RegistryMirror, "A pull-through cache for public registries (default docker.io, ghcr.io, quay.io) in the controller's namespace, as Services "+registryMirrorName+"-<host>:"+strconv.Itoa(registryMirrorPort)+", for pools with registryMirror set. Off unless set. Every runner pod starts with empty image storage, and Docker Hub allows 100 anonymous pulls an hour per IPv4 address, which a machine's runners share.")
	an.Describe(&a.PriorityClassName, "The controller manager pod's PriorityClass, e.g. a high one so runners cannot starve it. Default: none. The class must exist. The listeners' is each Pool's listenerPriorityClassName.")
	an.Describe(&a.Resources, "The controller manager container's requests and limits. Set, it replaces the default block whole (requests 100m CPU and 128Mi, limits 500m and 512Mi), so a limit left out is no limit.")
}

func (r *ContainerResources) Annotate(an infer.Annotator) {
	an.Describe(&r.CPURequest, "CPU request, e.g. 50m.")
	an.Describe(&r.MemoryRequest, "Memory request, e.g. 128Mi.")
	an.Describe(&r.CPULimit, "CPU limit. Default: none.")
	an.Describe(&r.MemoryLimit, "Memory limit. Default: none.")
}

// Controller is ARC's controller, and the device plugins runner pods take devices from.
type Controller struct {
	pulumi.ResourceState
	Namespace pulumi.StringOutput `pulumi:"namespace"`
}

func (c *Controller) Annotate(an infer.Annotator) {
	an.Describe(c, "Actions Runner Controller for one cluster, and the device plugins its runner pods use.")
	an.SetToken("index", "Controller")
}

func controllerValues(a ControllerArgs) map[string]any {
	v := map[string]any{
		"replicaCount": 1,
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]any{"cpu": "500m", "memory": "512Mi"},
		},
	}
	if a.Resources != nil {
		v["resources"] = a.Resources.values()
	}
	if class := orDefault(a.PriorityClassName, ""); class != "" {
		v["priorityClassName"] = class
	}
	if a.ListenerMetrics == nil || *a.ListenerMetrics {
		// The listener metrics feed the runner-gate. The controller's own are for monitoring, which
		// reads whether each listener is running from them, and stay off without it.
		controllerAddr := "0"
		addr := ":" + strconv.Itoa(metricsPort)
		if a.Monitoring != nil {
			controllerAddr = addr
		}
		v["metrics"] = map[string]any{"controllerManagerAddr": controllerAddr, "listenerAddr": addr, "listenerEndpoint": metricsPath}
	}
	if m := a.Monitoring; m != nil && m.ScrapeAnnotations != nil && *m.ScrapeAnnotations {
		v["podAnnotations"] = scrapeAnnotations()
	}
	return v
}

func devicePluginArgs(d DeviceOffer) (string, error) {
	paths := make([]map[string]string, len(d.Paths))
	for i, p := range d.Paths {
		paths[i] = map[string]string{"path": p}
	}
	raw, err := json.Marshal(map[string]any{"name": d.Name, "groups": []any{map[string]any{"count": intOr(d.Count, 16), "paths": paths}}})
	return string(raw), err
}

// NewController builds the Controller component.
func NewController(ctx *pulumi.Context, name string, a ControllerArgs, opts ...pulumi.ResourceOption) (*Controller, error) {
	if a.PrePull != nil {
		if _, err := prePullPlan(*a.PrePull); err != nil {
			return nil, err
		}
	}
	if a.RegistryMirror != nil {
		if err := checkRegistryMirror(*a.RegistryMirror); err != nil {
			return nil, err
		}
	}
	if a.Resources != nil && len(a.Resources.values()) == 0 {
		return nil, errors.New("controller: resources sets no request or limit; leave it out to keep the defaults")
	}
	if err := checkMonitoring(a); err != nil {
		return nil, err
	}
	c := &Controller{}
	if err := ctx.RegisterComponentResource("github-runners:index:Controller", name, c, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(c)
	namespace := orDefault(a.Namespace, "arc-systems")
	// What the namespace's contents wait for: nothing when something else owns it.
	var namespaceDeps []pulumi.Resource
	if a.CreateNamespace == nil || *a.CreateNamespace {
		ns, err := corev1.NewNamespace(ctx, name+"-ns", &corev1.NamespaceArgs{
			Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(namespace)},
		}, child)
		if err != nil {
			return nil, err
		}
		namespaceDeps = []pulumi.Resource{ns}
	}
	inNS := pulumi.DependsOn(namespaceDeps)
	// A Helm release, not a rendered chart: Helm installs the CRDs before the controller, so a fresh
	// install does not crash-loop on "no matches for kind EphemeralRunner".
	release, err := helmv3.NewRelease(ctx, name+"-controller", &helmv3.ReleaseArgs{
		Name:            pulumi.String("arc"),
		Chart:           pulumi.String(arcCharts + "/gha-runner-scale-set-controller"),
		Version:         pulumi.String(orDefault(a.Version, DefaultARCVersion)),
		Namespace:       pulumi.String(namespace),
		CreateNamespace: pulumi.Bool(false),
		Values:          pulumi.ToMap(controllerValues(a)),
	}, child, inNS)
	if err != nil {
		return nil, err
	}
	for _, d := range a.Devices {
		arg, err := devicePluginArgs(d)
		if err != nil {
			return nil, err
		}
		app := d.Name + "-device-plugin"
		labels := pulumi.StringMap{"app": pulumi.String(app)}
		_, err = appsv1.NewDaemonSet(ctx, name+"-"+app, &appsv1.DaemonSetArgs{
			Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(app), Namespace: pulumi.String(namespace)},
			Spec: &appsv1.DaemonSetSpecArgs{
				Selector: &metav1.LabelSelectorArgs{MatchLabels: labels},
				Template: &corev1.PodTemplateSpecArgs{
					Metadata: &metav1.ObjectMetaArgs{Labels: labels},
					Spec: &corev1.PodSpecArgs{
						PriorityClassName:            pulumi.String("system-node-critical"),
						AutomountServiceAccountToken: pulumi.Bool(false),
						Containers: corev1.ContainerArray{&corev1.ContainerArgs{
							Name:  pulumi.String("plugin"),
							Image: pulumi.String(orDefault(a.DevicePluginImage, DefaultDevicePluginImage)),
							Args:  pulumi.StringArray{pulumi.String("--device"), pulumi.String(arg)},
							Resources: &corev1.ResourceRequirementsArgs{
								Requests: pulumi.StringMap{"cpu": pulumi.String("10m"), "memory": pulumi.String("16Mi")},
								Limits:   pulumi.StringMap{"memory": pulumi.String("64Mi")},
							},
							// It registers with the kubelet and watches /dev: the node's own, by design.
							SecurityContext: &corev1.SecurityContextArgs{Privileged: pulumi.Bool(true)},
							VolumeMounts: corev1.VolumeMountArray{
								&corev1.VolumeMountArgs{Name: pulumi.String("device-plugins"), MountPath: pulumi.String("/var/lib/kubelet/device-plugins")},
								&corev1.VolumeMountArgs{Name: pulumi.String("dev"), MountPath: pulumi.String("/dev")},
							},
						}},
						Volumes: corev1.VolumeArray{
							&corev1.VolumeArgs{Name: pulumi.String("device-plugins"), HostPath: &corev1.HostPathVolumeSourceArgs{Path: pulumi.String("/var/lib/kubelet/device-plugins")}},
							&corev1.VolumeArgs{Name: pulumi.String("dev"), HostPath: &corev1.HostPathVolumeSourceArgs{Path: pulumi.String("/dev")}},
						},
					},
				},
			},
		}, child, inNS)
		if err != nil {
			return nil, err
		}
	}
	if a.PrePull != nil {
		if _, err := newPrePull(ctx, name, namespace, *a.PrePull, child, inNS); err != nil {
			return nil, err
		}
	}
	if a.RegistryMirror != nil {
		if err := newRegistryMirror(ctx, name, namespace, *a.RegistryMirror, child, inNS); err != nil {
			return nil, err
		}
	}
	if a.Monitoring != nil {
		if err := newMonitoring(ctx, name, namespace, *a.Monitoring, child, inNS); err != nil {
			return nil, err
		}
	}
	c.Namespace = release.Namespace.Elem()
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{"namespace": c.Namespace}); err != nil {
		return nil, err
	}
	return c, nil
}

// ---- Pool ----

// Pool is one organisation's runners on one machine.
type Pool struct {
	pulumi.ResourceState
	Namespace    pulumi.StringOutput `pulumi:"namespace"`
	ScaleSetName pulumi.StringOutput `pulumi:"scaleSetName"`
}

func (p *Pool) Annotate(an infer.Annotator) {
	an.Describe(p, "One organisation's self-hosted runners on one machine: credentials, runner group, scale set, Docker sidecar and job-started hook.")
	an.SetToken("index", "Pool")
}

// NewPool builds the Pool component.
func NewPool(ctx *pulumi.Context, name string, a PoolArgs, opts ...pulumi.ResourceOption) (*Pool, error) {
	if err := checkPool(a); err != nil {
		return nil, err
	}
	pool := &Pool{}
	if err := ctx.RegisterComponentResource("github-runners:index:Pool", name, pool, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(pool)
	namespace := poolNamespacePrefix + a.ID
	ns, err := corev1.NewNamespace(ctx, name+"-ns", &corev1.NamespaceArgs{
		Metadata: &metav1.ObjectMetaArgs{
			Name:   pulumi.String(namespace),
			Labels: pulumi.StringMap{poolNamespaceLabel: pulumi.String(a.ID)},
		},
	}, child)
	if err != nil {
		return nil, err
	}
	inNS := pulumi.DependsOn([]pulumi.Resource{ns})

	// ARC reads the pool's credential from this secret.
	secretName := "arc-github"
	data := pulumi.StringMap{}
	var app GitHubAppOutput
	if in := a.app(); in != nil {
		app = in.ToGitHubAppOutput()
		data["github_app_id"] = app.AppID()
		data["github_app_installation_id"] = app.InstallationID()
		data["github_app_private_key"] = app.PrivateKey()
	} else {
		data["github_token"] = pulumi.ToSecret(a.Token).(pulumi.StringOutput)
	}
	secret, err := corev1.NewSecret(ctx, name+"-github", &corev1.SecretArgs{
		Metadata:   &metav1.ObjectMetaArgs{Name: pulumi.String(secretName), Namespace: pulumi.String(namespace)},
		Type:       pulumi.String("Opaque"),
		StringData: data,
	}, child, inNS, fixedName)
	if err != nil {
		return nil, err
	}

	hook := PoolHook{}
	if a.Hook != nil {
		hook = *a.Hook
	}
	files, err := hookFiles(hook, a.GateClaim != nil)
	if err != nil {
		return nil, err
	}
	hooks, err := corev1.NewConfigMap(ctx, name+"-hooks", &corev1.ConfigMapArgs{
		Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(hooksConfigMap), Namespace: pulumi.String(namespace)},
		Data:     pulumi.ToStringMap(files),
	}, child, inNS, fixedName)
	if err != nil {
		return nil, err
	}
	podman, err := corev1.NewConfigMap(ctx, name+"-podman", &corev1.ConfigMapArgs{
		Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(podmanConfigMap), Namespace: pulumi.String(namespace)},
		Data:     pulumi.ToStringMap(podmanFiles(a.RegistryMirror)),
	}, child, inNS, fixedName)
	if err != nil {
		return nil, err
	}
	deps := []pulumi.Resource{ns, secret, hooks, podman}

	if a.NetworkPolicy != nil {
		// The agent's namespace goes into the spec as an Output, so one from a Controller or a
		// GateAgent carries its dependency and its preview unknown.
		written := poolNetworkPolicy(a)
		gateNamespace := stringOr(written.GateAgentNamespace, defaultGateAgentNamespace)
		written.GateAgentNamespace = pulumi.StringPtr(gateAgentNamespacePlaceholder)
		spec, err := toNetworkPolicySpec(networkPolicySpec(written), gateNamespace)
		if err != nil {
			return nil, err
		}
		policy, err := networkingv1.NewNetworkPolicy(ctx, name+"-egress", &networkingv1.NetworkPolicyArgs{
			Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String("runners-egress"), Namespace: pulumi.String(namespace)},
			Spec:     spec,
		}, child, inNS)
		if err != nil {
			return nil, err
		}
		deps = append(deps, policy)
	}

	runnerGroup := ""
	if g := a.RunnerGroup; g != nil {
		providerArgs := &github.ProviderArgs{Owner: pulumi.String(orgOf(a.ConfigURL))}
		if a.app() != nil {
			providerArgs.AppAuth = github.ProviderAppAuthArgs{
				Id:             app.AppID(),
				InstallationId: app.InstallationID(),
				PemFile:        data["github_app_private_key"],
			}
		} else {
			providerArgs.Token = data["github_token"].ToStringOutput().ToStringPtrOutput()
		}
		// Keep the provider's logical name so existing runner groups retain their provider reference.
		gh, err := github.NewProvider(ctx, name+"-github-admin", providerArgs, child)
		if err != nil {
			return nil, err
		}
		group, err := github.NewActionsRunnerGroup(ctx, name+"-group", &github.ActionsRunnerGroupArgs{
			Name:                     pulumi.String(g.Name),
			Visibility:               pulumi.String(orDefault(g.Visibility, "all")),
			AllowsPublicRepositories: pulumi.BoolPtrFromPtr(g.AllowsPublicRepositories),
		}, child, pulumi.Provider(gh))
		if err != nil {
			return nil, err
		}
		runnerGroup = g.Name
		deps = append(deps, group)
	}

	releaseOpts := []pulumi.ResourceOption{child, pulumi.DependsOn(deps)}
	if agentOwnsMaxRunners(a) {
		// The agent owns the live maxRunners. Pulumi keeps the value it created the release with, so
		// Helm renders the same maxRunners on every upgrade; Helm patches a custom resource with a
		// merge patch of the old render against the new one, so an unchanged field is left out and
		// the agent's live value stands.
		releaseOpts = append(releaseOpts, pulumi.IgnoreChanges([]string{maxRunnersValuePath}))
	}
	release, err := helmv3.NewRelease(ctx, name+"-scale-set", &helmv3.ReleaseArgs{
		Name:            pulumi.String(a.ScaleSetName),
		Chart:           pulumi.String(arcCharts + "/gha-runner-scale-set"),
		Version:         pulumi.String(DefaultARCVersion),
		Namespace:       pulumi.String(namespace),
		CreateNamespace: pulumi.Bool(false),
		Values:          poolValues(a, secretName, runnerGroup),
	}, releaseOpts...)
	if err != nil {
		return nil, err
	}
	pool.Namespace = release.Namespace.Elem()
	pool.ScaleSetName = pulumi.String(a.ScaleSetName).ToStringOutput()
	if err := ctx.RegisterResourceOutputs(pool, pulumi.Map{"namespace": pool.Namespace, "scaleSetName": pool.ScaleSetName}); err != nil {
		return nil, err
	}
	return pool, nil
}

// poolValues are the scale set's chart values. The job and hook environments and the gate claim may
// be Outputs (the claim URL is GateAgent.claimUrl), so the values are written once they are known,
// and a secret or a dependency among them carries through to the release.
func poolValues(a PoolArgs, secretName, runnerGroup string) pulumi.MapOutput {
	var hookEnv pulumi.StringMapInput
	if a.Hook != nil {
		hookEnv = a.Hook.Env
	}
	var claim PoolGateClaim
	if a.GateClaim != nil {
		claim = *a.GateClaim
	}
	return pulumi.All(stringMapOutput(a.Env), stringMapOutput(hookEnv), stringOr(claim.URL, ""), stringOr(claim.AgentNamespace, "")).ApplyT(func(in []any) map[string]any {
		plain := a
		plain.Env = pulumi.ToStringMap(in[0].(map[string]string))
		if a.Hook != nil {
			hook := *a.Hook
			hook.Env = pulumi.ToStringMap(in[1].(map[string]string))
			plain.Hook = &hook
		}
		if a.GateClaim != nil {
			plain.GateClaim = &PoolGateClaim{URL: pulumi.StringPtr(in[2].(string)), AgentNamespace: pulumi.StringPtr(in[3].(string))}
		}
		return scaleSetValues(plain, secretName, runnerGroup)
	}).(pulumi.MapOutput)
}

// gateAgentNamespacePlaceholder stands in the written spec for the GateAgent's namespace, which
// toNetworkPolicySpec replaces with the input itself.
const gateAgentNamespacePlaceholder = "<gate-agent-namespace>"

// toNetworkPolicySpec converts the plain spec into the SDK's input type through JSON, so the spec
// is written once, as data, and tested as data.
func toNetworkPolicySpec(spec map[string]any, gateNamespace pulumi.StringInput) (networkingv1.NetworkPolicySpecPtrInput, error) {
	labels := func(m map[string]string) pulumi.StringMap {
		out := pulumi.StringMap{}
		for k, v := range m {
			if v == gateAgentNamespacePlaceholder {
				out[k] = gateNamespace
			} else {
				out[k] = pulumi.String(v)
			}
		}
		return out
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var plain struct {
		PodSelector struct{} `json:"podSelector"`
		PolicyTypes []string `json:"policyTypes"`
		Egress      []struct {
			To []struct {
				NamespaceSelector *struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"namespaceSelector"`
				PodSelector *struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"podSelector"`
				IPBlock *struct {
					CIDR   string   `json:"cidr"`
					Except []string `json:"except"`
				} `json:"ipBlock"`
			} `json:"to"`
			Ports []struct {
				Protocol string `json:"protocol"`
				Port     int    `json:"port"`
			} `json:"ports"`
		} `json:"egress"`
	}
	if err := json.Unmarshal(raw, &plain); err != nil {
		return nil, err
	}
	egress := networkingv1.NetworkPolicyEgressRuleArray{}
	for _, rule := range plain.Egress {
		peers := networkingv1.NetworkPolicyPeerArray{}
		for _, to := range rule.To {
			peer := &networkingv1.NetworkPolicyPeerArgs{}
			if to.NamespaceSelector != nil {
				peer.NamespaceSelector = &metav1.LabelSelectorArgs{MatchLabels: labels(to.NamespaceSelector.MatchLabels)}
			}
			if to.PodSelector != nil {
				peer.PodSelector = &metav1.LabelSelectorArgs{MatchLabels: labels(to.PodSelector.MatchLabels)}
			}
			if to.IPBlock != nil {
				peer.IpBlock = &networkingv1.IPBlockArgs{Cidr: pulumi.String(to.IPBlock.CIDR), Except: pulumi.ToStringArray(to.IPBlock.Except)}
			}
			peers = append(peers, peer)
		}
		ports := networkingv1.NetworkPolicyPortArray{}
		for _, p := range rule.Ports {
			ports = append(ports, &networkingv1.NetworkPolicyPortArgs{Protocol: pulumi.String(p.Protocol), Port: pulumi.Int(p.Port)})
		}
		r := &networkingv1.NetworkPolicyEgressRuleArgs{To: peers}
		if len(ports) > 0 {
			r.Ports = ports
		}
		egress = append(egress, r)
	}
	return &networkingv1.NetworkPolicySpecArgs{
		PodSelector: &metav1.LabelSelectorArgs{},
		PolicyTypes: pulumi.ToStringArray(plain.PolicyTypes),
		Ingress:     networkingv1.NetworkPolicyIngressRuleArray{},
		Egress:      egress,
	}, nil
}
