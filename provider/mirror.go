package githubrunners

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pulumi/pulumi-go-provider/infer"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Every runner pod's podman sidecar starts with empty image storage, so every job pulls each image it
// uses from its registry again. Docker Hub allows anonymous clients 100 pulls an hour per IPv4
// address, and a whole machine's runners share one address. On 2026-11-28 the Ryzen went from 4
// runners to 12, the quota ran out (ratelimit-remaining: 0;w=3600), and every rest-mail E2E job
// failed: podman's pull got "toomanyrequests" and the job's next step "image not known". The
// registry mirror is a pull-through cache in the cluster: each upstream serves an image once, and the
// runners pull it from the cache, which is also faster than the internet.
//
// distribution's proxy mode takes one upstream, so the mirror pod runs one registry container per
// upstream, each with its own config file, port, Service and directory on the shared volume.
//
// Anonymous only: the mirror holds no upstream credentials, so it cannot hand a private image to
// every pod in the cluster. A private image's pull gets an error from the mirror, and podman then
// goes to the upstream itself with the job's own credentials.

// DefaultRegistryMirrorImage is the CNCF distribution registry, pinned by digest, from GHCR so the
// mirror does not need Docker Hub to start.
const DefaultRegistryMirrorImage = "ghcr.io/distribution/distribution:3.1.2@sha256:d106962e6fe3fa69c178cec77eeeee5edb626b8df6535b7bd6b95e658a0a5ca9"

// DefaultRegistryMirrorUpstreams are the registries mirrored unless a mirror lists its own.
var DefaultRegistryMirrorUpstreams = []string{"docker.io", "ghcr.io", "quay.io"}

const (
	registryMirrorName = "registry-mirror"
	// registryMirrorPort is every upstream's Service port; container ports count up from it.
	registryMirrorPort = 5000
	// distribution's default is 168h. 30 days, counted from when an item was cached: distribution
	// 3.1.2 schedules expiry only when it fetches from the upstream, and a hit does not move it.
	defaultRegistryMirrorTTL  = "720h"
	defaultRegistryMirrorSize = "50Gi"
	registryMirrorConfigDir   = "/etc/distribution"
	registryMirrorDataDir     = "/var/lib/registry"
	// registryMirrorFile is the drop-in in the podman ConfigMap, and where the sidecar mounts it.
	registryMirrorFile   = "registries-mirror.conf"
	registryMirrorDropIn = "/etc/containers/registries.conf.d/90-arc-mirror.conf"
)

// RegistryMirror is the cluster's pull-through cache for public registries.
type RegistryMirror struct {
	Upstreams         []string `pulumi:"upstreams,optional"`
	TTL               *string  `pulumi:"ttl,optional"`
	Size              *string  `pulumi:"size,optional"`
	Image             *string  `pulumi:"image,optional"`
	StorageClassName  *string  `pulumi:"storageClassName,optional"`
	PriorityClassName *string  `pulumi:"priorityClassName,optional"`
}

func (m *RegistryMirror) Annotate(an infer.Annotator) {
	an.Describe(&m.Upstreams, "Registries to cache, by host. Default "+strings.Join(DefaultRegistryMirrorUpstreams, ", ")+". Anonymous only: private images are not cached, and their pulls go to the upstream with the job's own credentials.")
	an.Describe(&m.TTL, "How long a cached manifest or blob lives, counted from when it was cached (a cache hit does not extend it), e.g. 720h. Default "+defaultRegistryMirrorTTL+". 0 keeps everything.")
	an.Describe(&m.Size, "The cache volume's size, shared by all upstreams. Default "+defaultRegistryMirrorSize+". Whether it is enforced is the StorageClass's business: local-path does not enforce it.")
	an.Describe(&m.Image, "The registry image, pinned by digest. Default: distribution 3.1.2 from GHCR.")
	an.Describe(&m.StorageClassName, "The cache volume's StorageClass. Default: the cluster's default class.")
	an.Describe(&m.PriorityClassName, "The mirror pod's PriorityClass. Default: none. The class must exist.")
}

// PoolRegistryMirror sends the pool's pulls from the mirrored registries to a Controller's registryMirror.
type PoolRegistryMirror struct {
	Namespace *string  `pulumi:"namespace,optional"`
	Upstreams []string `pulumi:"upstreams,optional"`
}

func (m *PoolRegistryMirror) Annotate(an infer.Annotator) {
	an.Describe(&m.Namespace, "The namespace of the Controller that runs the mirror. Default arc-systems.")
	an.Describe(&m.Upstreams, "Registries to pull through the mirror, by host. Default "+strings.Join(DefaultRegistryMirrorUpstreams, ", ")+". One the mirror does not cache costs nothing but a failed lookup: podman goes to the registry itself.")
}

// mirrorUpstream is one cached registry.
type mirrorUpstream struct {
	Host      string
	Slug      string
	RemoteURL string
	Port      int
}

var registryHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// mirrorUpstreams are the given hosts, or the defaults, each with its slug, remote URL and port.
func mirrorUpstreams(hosts []string) ([]mirrorUpstream, error) {
	if len(hosts) == 0 {
		hosts = DefaultRegistryMirrorUpstreams
	}
	seen := map[string]bool{}
	out := make([]mirrorUpstream, 0, len(hosts))
	for i, host := range hosts {
		slug := strings.ReplaceAll(host, ".", "-")
		// The slug names a container and a Service, registry-mirror-<slug>: a DNS label.
		if !registryHostPattern.MatchString(host) || len(registryMirrorName)+1+len(slug) > 63 {
			return nil, fmt.Errorf("registryMirror.upstreams[%d] %q must be a registry host name such as ghcr.io", i, host)
		}
		if seen[slug] {
			return nil, fmt.Errorf("registryMirror.upstreams[%d] %q is listed twice", i, host)
		}
		seen[slug] = true
		remote := "https://" + host
		if host == "docker.io" {
			// docker.io is Docker Hub's name; its registry API is served here.
			remote = "https://registry-1.docker.io"
		}
		out = append(out, mirrorUpstream{Host: host, Slug: slug, RemoteURL: remote, Port: registryMirrorPort + i})
	}
	return out, nil
}

func mirrorServiceName(u mirrorUpstream) string { return registryMirrorName + "-" + u.Slug }

func poolMirrorNamespace(m PoolRegistryMirror) string {
	return orDefault(m.Namespace, "arc-systems")
}

// checkRegistryMirror refuses a mirror that cannot work, before anything is created.
func checkRegistryMirror(m RegistryMirror) error {
	if _, err := mirrorUpstreams(m.Upstreams); err != nil {
		return err
	}
	ttl := orDefault(m.TTL, defaultRegistryMirrorTTL)
	if d, err := time.ParseDuration(ttl); err != nil || d < 0 {
		return fmt.Errorf("registryMirror.ttl %q must be a duration such as 720h, or 0 to keep everything", ttl)
	}
	return nil
}

// registryMirrorConf is podman's drop-in: each mirrored registry through its cache first, plain HTTP
// inside the cluster, hence insecure. The registry itself stays the location, so podman goes there
// when the cache cannot answer: when it is down, or for a private image it does not cache.
func registryMirrorConf(m PoolRegistryMirror) (string, error) {
	ups, err := mirrorUpstreams(m.Upstreams)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, u := range ups {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[[registry]]\nprefix = %q\nlocation = %q\n\n[[registry.mirror]]\nlocation = %q\ninsecure = true\n",
			u.Host, u.Host, mirrorServiceName(u)+"."+poolMirrorNamespace(m)+".svc:"+strconv.Itoa(registryMirrorPort))
	}
	return b.String(), nil
}

// registryMirrorConfig is one upstream's distribution config, as JSON, which its YAML parser reads.
// Written whole rather than layered on the image's development config, whose debug server listens on
// :5001 in every container of the pod and whose logging is at debug.
func registryMirrorConfig(u mirrorUpstream, ttl string) string {
	config := map[string]any{
		"version": "0.1",
		"log":     map[string]any{"level": "info", "fields": map[string]any{"service": "registry", "upstream": u.Host}},
		"storage": map[string]any{
			"filesystem": map[string]any{"rootdirectory": registryMirrorDataDir + "/" + u.Slug},
			"delete":     map[string]any{"enabled": true},
			"cache":      map[string]any{"blobdescriptor": "inmemory"},
		},
		"http": map[string]any{
			"addr":    ":" + strconv.Itoa(u.Port),
			"headers": map[string]any{"X-Content-Type-Options": []any{"nosniff"}},
		},
		// No username or password: anonymous only, see the top of this file.
		"proxy":  map[string]any{"remoteurl": u.RemoteURL, "ttl": ttl},
		"health": map[string]any{"storagedriver": map[string]any{"enabled": true, "interval": "10s", "threshold": 3}},
	}
	raw, _ := json.MarshalIndent(config, "", "  ")
	return string(raw) + "\n"
}

// registryMirrorFiles is the mirror's ConfigMap: one config per upstream.
func registryMirrorFiles(m RegistryMirror) (map[string]string, []mirrorUpstream, error) {
	if err := checkRegistryMirror(m); err != nil {
		return nil, nil, err
	}
	ups, _ := mirrorUpstreams(m.Upstreams)
	files := map[string]string{}
	for _, u := range ups {
		files[u.Slug+".json"] = registryMirrorConfig(u, orDefault(m.TTL, defaultRegistryMirrorTTL))
	}
	return files, ups, nil
}

// newRegistryMirror creates the mirror's config, volume, Deployment and one Service per upstream in
// the controller's namespace.
func newRegistryMirror(ctx *pulumi.Context, name, namespace string, m RegistryMirror, opts ...pulumi.ResourceOption) error {
	files, ups, err := registryMirrorFiles(m)
	if err != nil {
		return err
	}
	labels := pulumi.StringMap{"app": pulumi.String(registryMirrorName)}
	meta := &metav1.ObjectMetaArgs{Name: pulumi.String(registryMirrorName), Namespace: pulumi.String(namespace)}
	config, err := corev1.NewConfigMap(ctx, name+"-"+registryMirrorName, &corev1.ConfigMapArgs{
		Metadata: meta,
		Data:     pulumi.ToStringMap(files),
	}, opts...)
	if err != nil {
		return err
	}
	claim := &corev1.PersistentVolumeClaimSpecArgs{
		AccessModes: pulumi.StringArray{pulumi.String("ReadWriteOnce")},
		Resources: &corev1.VolumeResourceRequirementsArgs{
			Requests: pulumi.StringMap{"storage": pulumi.String(orDefault(m.Size, defaultRegistryMirrorSize))},
		},
	}
	if class := orDefault(m.StorageClassName, ""); class != "" {
		claim.StorageClassName = pulumi.String(class)
	}
	pvc, err := corev1.NewPersistentVolumeClaim(ctx, name+"-"+registryMirrorName, &corev1.PersistentVolumeClaimArgs{
		Metadata: meta,
		Spec:     claim,
	}, opts...)
	if err != nil {
		return err
	}

	containers := corev1.ContainerArray{}
	for _, u := range ups {
		probe := &corev1.ProbeArgs{HttpGet: &corev1.HTTPGetActionArgs{Path: pulumi.String("/v2/"), Port: pulumi.Int(u.Port)}}
		containers = append(containers, &corev1.ContainerArgs{
			Name:            pulumi.String(u.Slug),
			Image:           pulumi.String(orDefault(m.Image, DefaultRegistryMirrorImage)),
			ImagePullPolicy: pulumi.String("IfNotPresent"),
			Args:            pulumi.StringArray{pulumi.String("serve"), pulumi.String(registryMirrorConfigDir + "/" + u.Slug + ".json")},
			// The image's tracing exports to a collector that is not there.
			Env:            corev1.EnvVarArray{&corev1.EnvVarArgs{Name: pulumi.String("OTEL_TRACES_EXPORTER"), Value: pulumi.String("none")}},
			Ports:          corev1.ContainerPortArray{&corev1.ContainerPortArgs{ContainerPort: pulumi.Int(u.Port)}},
			ReadinessProbe: probe,
			Resources: &corev1.ResourceRequirementsArgs{
				Requests: pulumi.StringMap{"cpu": pulumi.String("20m"), "memory": pulumi.String("32Mi")},
				Limits:   pulumi.StringMap{"memory": pulumi.String("512Mi")},
			},
			SecurityContext: &corev1.SecurityContextArgs{
				AllowPrivilegeEscalation: pulumi.Bool(false),
				Privileged:               pulumi.Bool(false),
				ReadOnlyRootFilesystem:   pulumi.Bool(true),
				Capabilities:             &corev1.CapabilitiesArgs{Drop: pulumi.StringArray{pulumi.String("ALL")}},
				SeccompProfile:           &corev1.SeccompProfileArgs{Type: pulumi.String("RuntimeDefault")},
			},
			VolumeMounts: corev1.VolumeMountArray{
				&corev1.VolumeMountArgs{Name: pulumi.String("cache"), MountPath: pulumi.String(registryMirrorDataDir)},
				&corev1.VolumeMountArgs{Name: pulumi.String("config"), MountPath: pulumi.String(registryMirrorConfigDir), ReadOnly: pulumi.Bool(true)},
				&corev1.VolumeMountArgs{Name: pulumi.String("tmp"), MountPath: pulumi.String("/tmp")},
			},
		})
	}
	pod := &corev1.PodSpecArgs{
		AutomountServiceAccountToken: pulumi.Bool(false),
		EnableServiceLinks:           pulumi.Bool(false),
		Containers:                   containers,
		Volumes: corev1.VolumeArray{
			&corev1.VolumeArgs{
				Name:                  pulumi.String("cache"),
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSourceArgs{ClaimName: pvc.Metadata.Name().Elem()},
			},
			&corev1.VolumeArgs{Name: pulumi.String("config"), ConfigMap: &corev1.ConfigMapVolumeSourceArgs{Name: config.Metadata.Name()}},
			&corev1.VolumeArgs{Name: pulumi.String("tmp"), EmptyDir: &corev1.EmptyDirVolumeSourceArgs{}},
		},
	}
	if class := orDefault(m.PriorityClassName, ""); class != "" {
		pod.PriorityClassName = pulumi.String(class)
	}
	// distribution reads its config once, at start: a changed config restarts the pod.
	podMeta := &metav1.ObjectMetaArgs{
		Labels:      labels,
		Annotations: pulumi.StringMap{"github-runners.antimatter.studios/config-sha256": pulumi.String(filesHash(files))},
	}
	// Recreate: the volume is ReadWriteOnce, so a rolling update's new pod could not mount it.
	if _, err := appsv1.NewDeployment(ctx, name+"-"+registryMirrorName, &appsv1.DeploymentArgs{
		Metadata: meta,
		Spec: &appsv1.DeploymentSpecArgs{
			Replicas: pulumi.Int(1),
			Strategy: &appsv1.DeploymentStrategyArgs{Type: pulumi.String("Recreate")},
			Selector: &metav1.LabelSelectorArgs{MatchLabels: labels},
			Template: &corev1.PodTemplateSpecArgs{Metadata: podMeta, Spec: pod},
		},
	}, opts...); err != nil {
		return err
	}
	for _, u := range ups {
		if _, err := corev1.NewService(ctx, name+"-"+mirrorServiceName(u), &corev1.ServiceArgs{
			Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(mirrorServiceName(u)), Namespace: pulumi.String(namespace)},
			Spec: &corev1.ServiceSpecArgs{
				Selector: labels,
				Ports: corev1.ServicePortArray{&corev1.ServicePortArgs{
					Name: pulumi.String("registry"), Port: pulumi.Int(registryMirrorPort), TargetPort: pulumi.Int(u.Port),
				}},
			},
		}, opts...); err != nil {
			return err
		}
	}
	return nil
}

// filesHash is a stable digest of a ConfigMap's data.
func filesHash(files map[string]string) string {
	raw, _ := json.Marshal(files) // map keys in sorted order
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
