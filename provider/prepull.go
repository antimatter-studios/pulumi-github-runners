package githubrunners

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-go-provider/infer"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Pools scale from zero, so a job's pod can be the first on its node to need the runner and podman
// images, and waits for both to pull before the job starts. The pre-pull DaemonSet keeps them on
// every node: each image runs once as an init container, and the pod then idles on pause. The images
// stay because the pod's exited init containers still reference them, so the kubelet's image
// garbage collection keeps them for as long as the pod exists.

// DefaultPauseImage is the container the pre-pull pods idle on, pinned by digest.
const DefaultPauseImage = "registry.k8s.io/pause:3.10@sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a"

const prePullName = "runner-image-prepull"

// prePullUser is nobody: the pre-pull containers only exit, so they need no user of their own.
const prePullUser = 65534

// PrePull keeps runner images present on every node, so a pod scaled from zero does not wait for them.
type PrePull struct {
	Images            []string `pulumi:"images,optional"`
	PriorityClassName *string  `pulumi:"priorityClassName,optional"`
}

func (p *PrePull) Annotate(an infer.Annotator) {
	an.Describe(&p.Images, "Images kept on every node, each of which must have `true` on its PATH. Default: the default runner and podman images. List a pool's own image here if it sets one.")
	an.Describe(&p.PriorityClassName, "The pre-pull pods' PriorityClass, e.g. a low one so they are evicted first. Default: none. The class must exist.")
}

// prePullContainer is one container of the pre-pull pod.
type prePullContainer struct {
	Name    string
	Image   string
	Command []string
}

// prePullSpec is the pre-pull pod, as plain data.
type prePullSpec struct {
	PriorityClassName string
	InitContainers    []prePullContainer
	Pause             prePullContainer
}

// prePullPlan is what the pre-pull pod runs: each image once, with a command that exits at once,
// then pause.
func prePullPlan(p PrePull) (prePullSpec, error) {
	images := p.Images
	if len(images) == 0 {
		images = []string{DefaultRunnerImage, DefaultPodmanImage}
	}
	spec := prePullSpec{
		PriorityClassName: orDefault(p.PriorityClassName, ""),
		Pause:             prePullContainer{Name: "pause", Image: DefaultPauseImage},
	}
	for i, image := range images {
		if strings.TrimSpace(image) == "" {
			return prePullSpec{}, fmt.Errorf("prePull.images[%d] is empty", i)
		}
		spec.InitContainers = append(spec.InitContainers, prePullContainer{
			Name:    fmt.Sprintf("pull-%d", i),
			Image:   image,
			Command: []string{"true"},
		})
	}
	return spec, nil
}

// prePullContainerArgs is one pre-pull container: tiny requests, only a small memory limit, and no
// privileges at all. Its only job is to exist.
func prePullContainerArgs(c prePullContainer) *corev1.ContainerArgs {
	args := &corev1.ContainerArgs{
		Name:            pulumi.String(c.Name),
		Image:           pulumi.String(c.Image),
		ImagePullPolicy: pulumi.String("IfNotPresent"),
		Resources: &corev1.ResourceRequirementsArgs{
			Requests: pulumi.StringMap{"cpu": pulumi.String("1m"), "memory": pulumi.String("8Mi")},
			Limits:   pulumi.StringMap{"memory": pulumi.String("32Mi")},
		},
		SecurityContext: &corev1.SecurityContextArgs{
			RunAsNonRoot:             pulumi.Bool(true),
			RunAsUser:                pulumi.Int(prePullUser),
			RunAsGroup:               pulumi.Int(prePullUser),
			AllowPrivilegeEscalation: pulumi.Bool(false),
			Privileged:               pulumi.Bool(false),
			ReadOnlyRootFilesystem:   pulumi.Bool(true),
			Capabilities:             &corev1.CapabilitiesArgs{Drop: pulumi.StringArray{pulumi.String("ALL")}},
			SeccompProfile:           &corev1.SeccompProfileArgs{Type: pulumi.String("RuntimeDefault")},
		},
	}
	if len(c.Command) > 0 {
		args.Command = pulumi.ToStringArray(c.Command)
	}
	return args
}

// newPrePull creates the pre-pull DaemonSet in the controller's namespace.
func newPrePull(ctx *pulumi.Context, name, namespace string, p PrePull, opts ...pulumi.ResourceOption) (*appsv1.DaemonSet, error) {
	spec, err := prePullPlan(p)
	if err != nil {
		return nil, err
	}
	inits := corev1.ContainerArray{}
	for _, c := range spec.InitContainers {
		inits = append(inits, prePullContainerArgs(c))
	}
	pod := &corev1.PodSpecArgs{
		AutomountServiceAccountToken:  pulumi.Bool(false),
		EnableServiceLinks:            pulumi.Bool(false),
		TerminationGracePeriodSeconds: pulumi.Int(0),
		InitContainers:                inits,
		Containers:                    corev1.ContainerArray{prePullContainerArgs(spec.Pause)},
	}
	if spec.PriorityClassName != "" {
		pod.PriorityClassName = pulumi.String(spec.PriorityClassName)
	}
	labels := pulumi.StringMap{"app": pulumi.String(prePullName)}
	return appsv1.NewDaemonSet(ctx, name+"-"+prePullName, &appsv1.DaemonSetArgs{
		Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(prePullName), Namespace: pulumi.String(namespace)},
		Spec: &appsv1.DaemonSetSpecArgs{
			Selector: &metav1.LabelSelectorArgs{MatchLabels: labels},
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{Labels: labels},
				Spec:     pod,
			},
		},
	}, opts...)
}
