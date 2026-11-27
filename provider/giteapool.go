package githubrunners

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/pulumi/pulumi-go-provider/infer"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// GiteaRunnerPool is a set of Gitea Actions runners: a StatefulSet, so each runner keeps its
// registration on its own volume.
type GiteaRunnerPool struct {
	pulumi.ResourceState
	Namespace                 pulumi.StringOutput `pulumi:"namespace"`
	StatefulSetName           pulumi.StringOutput `pulumi:"statefulSetName"`
	RegistrationTokenEndpoint pulumi.StringOutput `pulumi:"registrationTokenEndpoint"`
}

func (p *GiteaRunnerPool) Annotate(an infer.Annotator) {
	an.Describe(p, "Gitea Actions runners for one repository, organisation or instance: registration Secret, a fixed-size StatefulSet with a persistent registration and image store per runner, and a Docker sidecar (rootless podman, or privileged dind). It creates no ServiceAccount, role or binding, and mounts no API token unless asked.")
	an.SetToken("index", "GiteaRunnerPool")
}

// NewGiteaRunnerPool builds the GiteaRunnerPool component.
func NewGiteaRunnerPool(ctx *pulumi.Context, name string, a GiteaRunnerPoolArgs, opts ...pulumi.ResourceOption) (*GiteaRunnerPool, error) {
	if err := checkGiteaPool(a); err != nil {
		return nil, err
	}
	config, err := giteaRunnerConfig(a)
	if err != nil {
		return nil, err
	}
	pool := &GiteaRunnerPool{}
	if err := ctx.RegisterComponentResource("github-runners:index:GiteaRunnerPool", name, pool, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(pool)
	namespace := "gitea-runners-" + a.ID
	ns, err := corev1.NewNamespace(ctx, name+"-ns", &corev1.NamespaceArgs{
		Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(namespace)},
	}, child)
	if err != nil {
		return nil, err
	}
	inNS := pulumi.DependsOn([]pulumi.Resource{ns})
	meta := func(object string) *metav1.ObjectMetaArgs {
		return &metav1.ObjectMetaArgs{Name: pulumi.String(object), Namespace: pulumi.String(namespace)}
	}

	secret, err := corev1.NewSecret(ctx, name+"-registration", &corev1.SecretArgs{
		Metadata: meta(giteaSecretName),
		Type:     pulumi.String("Opaque"),
		StringData: pulumi.StringMap{
			giteaTokenKey: pulumi.ToSecret(a.RegistrationToken).(pulumi.StringOutput),
		},
	}, child, inNS)
	if err != nil {
		return nil, err
	}
	runnerConfig, err := corev1.NewConfigMap(ctx, name+"-config", &corev1.ConfigMapArgs{
		Metadata: meta(giteaConfigMap),
		Data:     pulumi.StringMap{"config.yaml": pulumi.String(config)},
	}, child, inNS)
	if err != nil {
		return nil, err
	}
	deps := []pulumi.Resource{ns, secret, runnerConfig}
	checksum := sha256.New()
	checksum.Write([]byte(config))
	if giteaEngine(a) == engineRootless {
		podman, err := corev1.NewConfigMap(ctx, name+"-podman", &corev1.ConfigMapArgs{
			Metadata: meta(giteaPodmanMap),
			Data:     pulumi.ToStringMap(podmanFiles()),
		}, child, inNS)
		if err != nil {
			return nil, err
		}
		deps = append(deps, podman)
		// envList is in name order, so the checksum is stable.
		for _, e := range envList(podmanFiles()) {
			checksum.Write([]byte(e.(map[string]any)["value"].(string)))
		}
	}

	labels := pulumi.StringMap{
		"app.kubernetes.io/name":     pulumi.String("gitea-runner"),
		"app.kubernetes.io/instance": pulumi.String(a.ID),
	}
	claims := corev1.PersistentVolumeClaimTypeArray{
		volumeClaim("data", orDefault(a.StorageSize, "1Gi"), orDefault(a.StorageClassName, "")),
	}
	if giteaPersistentEngine(a) {
		claims = append(claims, volumeClaim(giteaEngineVolume, orDefault(a.EngineStorageSize, "20Gi"), orDefault(a.EngineStorageClassName, "")))
	}
	set, err := appsv1.NewStatefulSet(ctx, name+"-runner", &appsv1.StatefulSetArgs{
		Metadata: meta(a.ID),
		Spec: &appsv1.StatefulSetSpecArgs{
			Replicas: pulumi.Int(intOr(a.Replicas, 1)),
			// A runner that cannot start does not hold back the others.
			PodManagementPolicy: pulumi.String("Parallel"),
			Selector:            &metav1.LabelSelectorArgs{MatchLabels: labels},
			UpdateStrategy: &appsv1.StatefulSetUpdateStrategyArgs{
				Type:          pulumi.String("RollingUpdate"),
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategyArgs{Partition: pulumi.Int(intOr(a.UpdatePartition, 0))},
			},
			// A removed runner's claim goes with it, rather than holding disk for ever. Removing the
			// whole StatefulSet keeps the claims, so a replacement does not register again.
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicyArgs{
				WhenDeleted: pulumi.String("Retain"),
				WhenScaled:  pulumi.String("Delete"),
			},
			Template: &corev1.PodTemplateSpecArgs{
				Metadata: &metav1.ObjectMetaArgs{
					Labels:      labels,
					Annotations: pulumi.StringMap{giteaConfigChecksum: pulumi.String(hex.EncodeToString(checksum.Sum(nil)))},
				},
				Spec: giteaPodSpec(a, labels),
			},
			VolumeClaimTemplates: claims,
		},
	}, child, pulumi.DependsOn(deps))
	if err != nil {
		return nil, err
	}
	pool.Namespace = set.Metadata.Namespace().Elem()
	pool.StatefulSetName = set.Metadata.Name().Elem()
	pool.RegistrationTokenEndpoint = pulumi.String(registrationTokenEndpoint(a)).ToStringOutput()
	if err := ctx.RegisterResourceOutputs(pool, pulumi.Map{
		"namespace":                 pool.Namespace,
		"statefulSetName":           pool.StatefulSetName,
		"registrationTokenEndpoint": pool.RegistrationTokenEndpoint,
	}); err != nil {
		return nil, err
	}
	return pool, nil
}

// volumeClaim is one volume each runner gets, named <name>-<poolId>-<ordinal>.
func volumeClaim(name, size, class string) *corev1.PersistentVolumeClaimTypeArgs {
	spec := &corev1.PersistentVolumeClaimSpecArgs{
		AccessModes: pulumi.StringArray{pulumi.String("ReadWriteOnce")},
		Resources: &corev1.VolumeResourceRequirementsArgs{
			Requests: pulumi.StringMap{"storage": pulumi.String(size)},
		},
	}
	if class != "" {
		spec.StorageClassName = pulumi.String(class)
	}
	return &corev1.PersistentVolumeClaimTypeArgs{
		Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(name)},
		Spec:     spec,
	}
}

func envVar(name, value string) corev1.EnvVarInput {
	return &corev1.EnvVarArgs{Name: pulumi.String(name), Value: pulumi.String(value)}
}

func mount(volume, path string, readOnly bool) corev1.VolumeMountInput {
	return &corev1.VolumeMountArgs{Name: pulumi.String(volume), MountPath: pulumi.String(path), ReadOnly: pulumi.Bool(readOnly)}
}

// giteaResources: small requests and no CPU or memory limit unless set, as Pool's runners; the disk
// limit stays.
func giteaResources(r *GiteaContainerResources, cpuRequest, memoryRequest, storageLimit string) *corev1.ResourceRequirementsArgs {
	if r == nil {
		r = &GiteaContainerResources{}
	}
	requests := pulumi.StringMap{
		"cpu":               pulumi.String(orDefault(r.CPURequest, cpuRequest)),
		"memory":            pulumi.String(orDefault(r.MemoryRequest, memoryRequest)),
		"ephemeral-storage": pulumi.String("1Gi"),
	}
	limits := pulumi.StringMap{"ephemeral-storage": pulumi.String(storageLimit)}
	for resource, v := range quantities(r.CPULimit, r.MemoryLimit) {
		limits[resource] = pulumi.String(v.(string))
	}
	return &corev1.ResourceRequirementsArgs{Requests: requests, Limits: limits}
}

// giteaRegister registers the runner when /data holds no registration. gitea-runner can fail and
// still exit 0, so the file decides.
const giteaRegister = `if [ ! -s "$RUNNER_STATE_FILE" ]; then
  gitea-runner register --config "$CONFIG_FILE" --no-interactive \
    --instance "$GITEA_INSTANCE_URL" --name "$GITEA_RUNNER_NAME" \
    --token "$(cat "$GITEA_RUNNER_REGISTRATION_TOKEN_FILE")"
fi
[ -s "$RUNNER_STATE_FILE" ]
`

// giteaInitContainers prepare /data and register the runner. Registering apart from the daemon
// keeps the registration token out of every container a job can reach.
func giteaInitContainers(a GiteaRunnerPoolArgs, image string) corev1.ContainerArray {
	tiny := &corev1.ResourceRequirementsArgs{
		Requests: pulumi.StringMap{"cpu": pulumi.String("10m"), "memory": pulumi.String("32Mi")},
	}
	return corev1.ContainerArray{
		// A new volume's root belongs to root, and a runner image need not run as root. Recursive,
		// because the daemon rewrites the registration file, which an earlier image may have written as
		// another user. Not fsGroup, which would also rewrite the ownership of every file in the
		// engine's image store.
		&corev1.ContainerArgs{
			Name:            pulumi.String("prepare"),
			Image:           pulumi.String(image),
			Command:         pulumi.ToStringArray([]string{"sh", "-c", "chmod -R a+rwX " + giteaDataDir}),
			SecurityContext: &corev1.SecurityContextArgs{RunAsUser: pulumi.Int(0)},
			Resources:       tiny,
			VolumeMounts:    corev1.VolumeMountArray{mount("data", giteaDataDir, false)},
		},
		&corev1.ContainerArgs{
			Name:       pulumi.String("register"),
			Image:      pulumi.String(image),
			Command:    pulumi.ToStringArray([]string{"sh", "-c", giteaRegister}),
			WorkingDir: pulumi.String(giteaDataDir),
			Env: corev1.EnvVarArray{
				envVar("CONFIG_FILE", giteaConfigFile),
				envVar("GITEA_INSTANCE_URL", strings.TrimSuffix(a.InstanceURL, "/")),
				&corev1.EnvVarArgs{Name: pulumi.String("GITEA_RUNNER_NAME"), ValueFrom: &corev1.EnvVarSourceArgs{
					FieldRef: &corev1.ObjectFieldSelectorArgs{FieldPath: pulumi.String("metadata.name")},
				}},
				envVar("GITEA_RUNNER_REGISTRATION_TOKEN_FILE", giteaTokenDir+"/"+giteaTokenKey),
				// The config names the same file.
				envVar("RUNNER_STATE_FILE", giteaStateFile(a)),
			},
			Resources: tiny,
			VolumeMounts: corev1.VolumeMountArray{
				mount("data", giteaDataDir, false),
				mount("config", giteaConfigDir, true),
				mount("registration-token", giteaTokenDir, true),
			},
		},
	}
}

// giteaPodSpec is the runner pod: the runner, and the engine sidecar its containers run in.
func giteaPodSpec(a GiteaRunnerPoolArgs, labels pulumi.StringMap) *corev1.PodSpecArgs {
	socket := giteaSocketDir + "/docker.sock"
	storage := orDefault(a.EngineStorageSize, "20Gi")
	image := orDefault(a.Image, DefaultGiteaRunnerImage)
	// The engine's own disk use: with a volume for its store, only its writable layer, logs and the
	// shared /tmp; without one the store counts too, as the kubelet counts an emptyDir.
	engineDisk, engineMountPath := "10Gi", "/var/lib/containers"
	if !giteaPersistentEngine(a) {
		engineDisk = storage
	}
	if giteaEngine(a) == enginePrivileged {
		engineMountPath = "/var/lib/docker"
	}
	runnerMounts := corev1.VolumeMountArray{
		mount("data", giteaDataDir, false),
		mount("config", giteaConfigDir, true),
		mount("docker-socket", giteaSocketDir, false),
	}
	// The socket directory is at one path in both containers: the runner hands job containers a bind
	// of DOCKER_HOST's path, which the engine resolves in its own filesystem. Mounting the directory,
	// not the socket file, follows the engine's new socket when it restarts.
	engineMounts := corev1.VolumeMountArray{
		mount("docker-socket", giteaSocketDir, false),
		mount(giteaEngineVolume, engineMountPath, false),
	}
	volumes := corev1.VolumeArray{
		&corev1.VolumeArgs{Name: pulumi.String("config"), ConfigMap: &corev1.ConfigMapVolumeSourceArgs{Name: pulumi.String(giteaConfigMap)}},
		// Readable by the image's user; only the register container mounts it.
		&corev1.VolumeArgs{Name: pulumi.String("registration-token"), Secret: &corev1.SecretVolumeSourceArgs{SecretName: pulumi.String(giteaSecretName), DefaultMode: pulumi.Int(0o444)}},
		&corev1.VolumeArgs{Name: pulumi.String("docker-socket"), EmptyDir: &corev1.EmptyDirVolumeSourceArgs{}},
	}
	if !giteaPersistentEngine(a) {
		volumes = append(volumes, &corev1.VolumeArgs{Name: pulumi.String(giteaEngineVolume), EmptyDir: &corev1.EmptyDirVolumeSourceArgs{SizeLimit: pulumi.String(storage)}})
	}
	// Host steps run in the runner, and what they `docker run` runs in the engine, which resolves a
	// bind mount's path in its own filesystem: the same volumes at the same paths, as in Pool.
	if giteaHostJobs(a) {
		for _, m := range [][2]string{{"work", giteaWorkDir}, {"tmp", "/tmp"}} {
			runnerMounts = append(runnerMounts, mount(m[0], m[1], false))
			engineMounts = append(engineMounts, mount(m[0], m[1], false))
		}
		volumes = append(volumes,
			&corev1.VolumeArgs{Name: pulumi.String("work"), EmptyDir: &corev1.EmptyDirVolumeSourceArgs{SizeLimit: pulumi.String(orDefault(a.WorkVolumeSize, "10Gi"))}},
			&corev1.VolumeArgs{Name: pulumi.String("tmp"), EmptyDir: &corev1.EmptyDirVolumeSourceArgs{}})
	}
	runner := &corev1.ContainerArgs{
		Name:  pulumi.String("runner"),
		Image: pulumi.String(image),
		// The daemon refuses to start without the engine, so wait for its socket first.
		Command: pulumi.ToStringArray([]string{"sh", "-c",
			"for i in $(seq 1 120); do [ -S " + socket + " ] && break; sleep 0.5; done; exec tini -- gitea-runner daemon --config " + giteaConfigFile}),
		WorkingDir:   pulumi.String(giteaDataDir),
		Env:          corev1.EnvVarArray{envVar("DOCKER_HOST", "unix://"+socket)},
		Resources:    giteaResources(a.RunnerResources, "100m", "256Mi", orDefault(a.RunnerStorageLimit, "10Gi")),
		VolumeMounts: runnerMounts,
	}
	engine := &corev1.ContainerArgs{
		Name:         pulumi.String("engine"),
		Resources:    giteaResources(a.EngineResources, "100m", "128Mi", engineDisk),
		VolumeMounts: engineMounts,
	}
	spec := &corev1.PodSpecArgs{
		InitContainers:               giteaInitContainers(a, image),
		AutomountServiceAccountToken: pulumi.Bool(a.AutomountServiceAccountToken != nil && *a.AutomountServiceAccountToken),
		EnableServiceLinks:           pulumi.Bool(false),
	}
	if giteaEngine(a) == engineRootless {
		// Root in the pod is an unprivileged UID on the node.
		spec.HostUsers = pulumi.Bool(false)
		engine.Image = pulumi.String(orDefault(a.EngineImage, DefaultGiteaPodmanImage))
		engine.Command = pulumi.ToStringArray(podmanServiceCommand(giteaSocketDir))
		env := corev1.EnvVarArray{}
		for _, e := range podmanEnv() {
			env = append(env, envVar(e[0], e[1]))
		}
		engine.Env = env
		engine.SecurityContext = &corev1.SecurityContextArgs{
			RunAsUser:                pulumi.Int(0),
			Privileged:               pulumi.Bool(false),
			AllowPrivilegeEscalation: pulumi.Bool(true),
			Capabilities:             &corev1.CapabilitiesArgs{Add: pulumi.ToStringArray(podmanCapabilities)},
			ProcMount:                pulumi.String("Unmasked"),
			SeccompProfile:           &corev1.SeccompProfileArgs{Type: pulumi.String("Unconfined")},
			AppArmorProfile:          &corev1.AppArmorProfileArgs{Type: pulumi.String("Unconfined")},
		}
		engine.VolumeMounts = append(engineMounts, mount("podman-config", podmanConfigDir, true))
		volumes = append(volumes, &corev1.VolumeArgs{Name: pulumi.String("podman-config"),
			ConfigMap: &corev1.ConfigMapVolumeSourceArgs{Name: pulumi.String(giteaPodmanMap), DefaultMode: pulumi.Int(0o555)}})
	} else {
		engine.Image = pulumi.String(orDefault(a.EngineImage, DefaultDindImage))
		// The socket only: no TCP listener, so no TLS certificates either. dockerd gives its socket to
		// root and the docker group; a runner image that is not root needs it too, as podman's does.
		engine.Command = pulumi.ToStringArray([]string{"sh", "-c",
			"(until [ -S " + socket + " ]; do sleep 0.2; done; chmod 666 " + socket + ") & " +
				"exec dockerd-entrypoint.sh dockerd --host=unix://" + socket})
		engine.Env = corev1.EnvVarArray{envVar("DOCKER_TLS_CERTDIR", "")}
		engine.SecurityContext = &corev1.SecurityContextArgs{Privileged: pulumi.Bool(true)}
	}
	spec.Containers = corev1.ContainerArray{runner, engine}
	spec.Volumes = volumes
	// HARD, as Pool's topologySpread is: soft spreading put four runners on one 4-core node and a
	// `go test -race` suite timed out. With one node to choose from it constrains nothing.
	if a.TopologySpread == nil || *a.TopologySpread {
		spec.TopologySpreadConstraints = corev1.TopologySpreadConstraintArray{&corev1.TopologySpreadConstraintArgs{
			MaxSkew:           pulumi.Int(1),
			TopologyKey:       pulumi.String("kubernetes.io/hostname"),
			WhenUnsatisfiable: pulumi.String("DoNotSchedule"),
			LabelSelector:     &metav1.LabelSelectorArgs{MatchLabels: labels},
		}}
	}
	if account := orDefault(a.ServiceAccountName, ""); account != "" {
		spec.ServiceAccountName = pulumi.String(account)
	}
	if class := orDefault(a.PriorityClassName, ""); class != "" {
		spec.PriorityClassName = pulumi.String(class)
	}
	if len(a.NodeSelector) > 0 {
		spec.NodeSelector = pulumi.ToStringMap(a.NodeSelector)
	}
	return spec
}
