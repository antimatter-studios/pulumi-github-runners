package githubrunners

import (
	"fmt"
	"strconv"

	"github.com/pulumi/pulumi-go-provider/infer"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Every runner pod's podman sidecar starts with empty image storage, so every job pulls each image it
// uses from Docker Hub again. Docker Hub allows anonymous clients 100 pulls an hour per IPv4 address,
// and a whole machine's runners share one address. On 2026-11-28 the Ryzen went from 4 runners to 12,
// the quota ran out (ratelimit-remaining: 0;w=3600), and every rest-mail E2E job failed: podman's pull
// got "toomanyrequests" and the job's next step "image not known". The registry mirror is a pull-through
// cache in the cluster: Docker Hub serves each image once, and the runners pull it from the cache.

// DefaultRegistryMirrorImage is the CNCF distribution registry, pinned by digest, from GHCR so the
// mirror does not need Docker Hub to start.
const DefaultRegistryMirrorImage = "ghcr.io/distribution/distribution:3.1.2@sha256:d106962e6fe3fa69c178cec77eeeee5edb626b8df6535b7bd6b95e658a0a5ca9"

const (
	registryMirrorName     = "registry-mirror"
	registryMirrorPort     = 5000
	registryMirrorUpstream = "https://registry-1.docker.io"
	// registryMirrorFile is the drop-in in the podman ConfigMap, and where the sidecar mounts it.
	registryMirrorFile   = "registries-mirror.conf"
	registryMirrorDropIn = "/etc/containers/registries.conf.d/90-arc-mirror.conf"
)

// RegistryMirror is the cluster's Docker Hub pull-through cache.
type RegistryMirror struct {
	Image             *string `pulumi:"image,optional"`
	StorageSize       *string `pulumi:"storageSize,optional"`
	StorageClassName  *string `pulumi:"storageClassName,optional"`
	PriorityClassName *string `pulumi:"priorityClassName,optional"`
}

func (m *RegistryMirror) Annotate(an infer.Annotator) {
	an.Describe(&m.Image, "The registry image, pinned by digest. Default: distribution 3.1.2 from GHCR.")
	an.Describe(&m.StorageSize, "The cache's PersistentVolumeClaim size. Default 20Gi. Entries expire 7 days after their last pull.")
	an.Describe(&m.StorageClassName, "The cache volume's StorageClass. Default: the cluster's default class.")
	an.Describe(&m.PriorityClassName, "The mirror pod's PriorityClass. Default: none. The class must exist.")
}

// PoolRegistryMirror sends the pool's Docker Hub pulls to a Controller's registryMirror.
type PoolRegistryMirror struct {
	Namespace *string `pulumi:"namespace,optional"`
}

func (m *PoolRegistryMirror) Annotate(an infer.Annotator) {
	an.Describe(&m.Namespace, "The namespace of the Controller that runs the mirror. Default arc-systems.")
}

// registryMirrorHost is the mirror's in-cluster address.
func registryMirrorHost(namespace string) string {
	return registryMirrorName + "." + namespace + ".svc:" + strconv.Itoa(registryMirrorPort)
}

func poolMirrorNamespace(m PoolRegistryMirror) string {
	return orDefault(m.Namespace, "arc-systems")
}

// registryMirrorConf is podman's drop-in: docker.io through the mirror first. Plain HTTP inside the
// cluster, hence insecure. When the mirror cannot answer, podman goes to Docker Hub itself, so a mirror
// that is down costs the quota, not the job.
func registryMirrorConf(namespace string) string {
	return fmt.Sprintf(`[[registry]]
prefix = "docker.io"
location = "docker.io"

[[registry.mirror]]
location = %q
insecure = true
`, registryMirrorHost(namespace))
}

// registryMirrorEnv configures distribution as a Docker Hub proxy. Debug logging is distribution's
// default and writes several lines per request.
func registryMirrorEnv() map[string]string {
	return map[string]string{
		"REGISTRY_PROXY_REMOTEURL":        registryMirrorUpstream,
		"REGISTRY_HTTP_ADDR":              ":" + strconv.Itoa(registryMirrorPort),
		"REGISTRY_STORAGE_DELETE_ENABLED": "true",
		"REGISTRY_LOG_LEVEL":              "info",
		"OTEL_TRACES_EXPORTER":            "none",
	}
}

// newRegistryMirror creates the mirror's volume, Deployment and Service in the controller's namespace.
func newRegistryMirror(ctx *pulumi.Context, name, namespace string, m RegistryMirror, opts ...pulumi.ResourceOption) error {
	labels := pulumi.StringMap{"app": pulumi.String(registryMirrorName)}
	meta := &metav1.ObjectMetaArgs{Name: pulumi.String(registryMirrorName), Namespace: pulumi.String(namespace)}
	claim := &corev1.PersistentVolumeClaimSpecArgs{
		AccessModes: pulumi.StringArray{pulumi.String("ReadWriteOnce")},
		Resources: &corev1.VolumeResourceRequirementsArgs{
			Requests: pulumi.StringMap{"storage": pulumi.String(orDefault(m.StorageSize, "20Gi"))},
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
	env := corev1.EnvVarArray{}
	for _, e := range envList(registryMirrorEnv()) {
		kv := e.(map[string]any)
		env = append(env, &corev1.EnvVarArgs{Name: pulumi.String(kv["name"].(string)), Value: pulumi.String(kv["value"].(string))})
	}
	probe := &corev1.ProbeArgs{HttpGet: &corev1.HTTPGetActionArgs{Path: pulumi.String("/v2/"), Port: pulumi.Int(registryMirrorPort)}}
	pod := &corev1.PodSpecArgs{
		AutomountServiceAccountToken: pulumi.Bool(false),
		EnableServiceLinks:           pulumi.Bool(false),
		Containers: corev1.ContainerArray{&corev1.ContainerArgs{
			Name:            pulumi.String("registry"),
			Image:           pulumi.String(orDefault(m.Image, DefaultRegistryMirrorImage)),
			ImagePullPolicy: pulumi.String("IfNotPresent"),
			Env:             env,
			Ports:           corev1.ContainerPortArray{&corev1.ContainerPortArgs{Name: pulumi.String("registry"), ContainerPort: pulumi.Int(registryMirrorPort)}},
			ReadinessProbe:  probe,
			Resources: &corev1.ResourceRequirementsArgs{
				Requests: pulumi.StringMap{"cpu": pulumi.String("50m"), "memory": pulumi.String("64Mi")},
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
				&corev1.VolumeMountArgs{Name: pulumi.String("cache"), MountPath: pulumi.String("/var/lib/registry")},
				&corev1.VolumeMountArgs{Name: pulumi.String("tmp"), MountPath: pulumi.String("/tmp")},
			},
		}},
		Volumes: corev1.VolumeArray{
			&corev1.VolumeArgs{
				Name:                  pulumi.String("cache"),
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSourceArgs{ClaimName: pvc.Metadata.Name().Elem()},
			},
			&corev1.VolumeArgs{Name: pulumi.String("tmp"), EmptyDir: &corev1.EmptyDirVolumeSourceArgs{}},
		},
	}
	if class := orDefault(m.PriorityClassName, ""); class != "" {
		pod.PriorityClassName = pulumi.String(class)
	}
	// Recreate: the volume is ReadWriteOnce, so a rolling update's new pod could not mount it.
	if _, err := appsv1.NewDeployment(ctx, name+"-"+registryMirrorName, &appsv1.DeploymentArgs{
		Metadata: meta,
		Spec: &appsv1.DeploymentSpecArgs{
			Replicas: pulumi.Int(1),
			Strategy: &appsv1.DeploymentStrategyArgs{Type: pulumi.String("Recreate")},
			Selector: &metav1.LabelSelectorArgs{MatchLabels: labels},
			Template: &corev1.PodTemplateSpecArgs{Metadata: &metav1.ObjectMetaArgs{Labels: labels}, Spec: pod},
		},
	}, opts...); err != nil {
		return err
	}
	_, err = corev1.NewService(ctx, name+"-"+registryMirrorName, &corev1.ServiceArgs{
		Metadata: meta,
		Spec: &corev1.ServiceSpecArgs{
			Selector: labels,
			Ports: corev1.ServicePortArray{&corev1.ServicePortArgs{
				Name: pulumi.String("registry"), Port: pulumi.Int(registryMirrorPort), TargetPort: pulumi.Int(registryMirrorPort),
			}},
		},
	}, opts...)
	return err
}
