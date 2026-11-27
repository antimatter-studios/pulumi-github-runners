import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
/**
 * Gitea Actions runners for one repository, organisation or instance: registration Secret, a fixed-size StatefulSet with a persistent registration and image store per runner, and a Docker sidecar (rootless podman, or privileged dind). It creates no ServiceAccount, role or binding, and mounts no API token unless asked.
 */
export declare class GiteaRunnerPool extends pulumi.ComponentResource {
    /**
     * Returns true if the given object is an instance of GiteaRunnerPool.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is GiteaRunnerPool;
    readonly namespace: pulumi.Output<string>;
    readonly registrationTokenEndpoint: pulumi.Output<string>;
    readonly statefulSetName: pulumi.Output<string>;
    /**
     * Create a GiteaRunnerPool resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: GiteaRunnerPoolArgs, opts?: pulumi.ComponentResourceOptions);
}
/**
 * The set of arguments for constructing a GiteaRunnerPool resource.
 */
export interface GiteaRunnerPoolArgs {
    /**
     * Mount a Kubernetes API token in the runner pods, which every job can then read. Default false. The pool creates no role or binding, so grant the ServiceAccount only what the jobs need.
     */
    automountServiceAccountToken?: boolean;
    /**
     * Jobs each runner takes at once. Default 1. Jobs of one runner share its container, engine and disk.
     */
    capacity?: number;
    /**
     * Options added to the docker create of every job container, e.g. "--add-host=git:10.0.0.1". Jobs that run on the host are not containers.
     */
    containerOptions?: string;
    /**
     * The Docker sidecar jobs run in: podman (default), rootless in the pod's own user namespace, or dind, Docker in a privileged container.
     */
    engine?: string;
    /**
     * The engine sidecar's image, pinned by digest. Default: podman, or docker:dind when engine is dind.
     */
    engineImage?: string;
    /**
     * The engine sidecar's CPU and memory, which is where job containers run. No limits unless set.
     */
    engineResources?: pulumi.Input<inputs.GiteaContainerResourcesArgs | undefined>;
    /**
     * StorageClass of the engine's volume, e.g. a node-local one: it is a cache. Default: the cluster's default class. With engine podman it must support idmapped mounts and overlayfs.
     */
    engineStorageClassName?: string;
    /**
     * Disk for the engine's images, containers and job workspaces. Default 20Gi.
     */
    engineStorageSize?: string;
    /**
     * Every job's default environment. DOCKER_HOST is the runner's own and is refused.
     */
    env?: pulumi.Input<{
        [key: string]: string;
    } | undefined>;
    /**
     * The runner container's image, pinned by digest. Default: Gitea's runner. Required with host labels: an image with the tools jobs need, plus gitea-runner and tini on its PATH.
     */
    image?: string;
    /**
     * The Gitea instance the runners connect to, e.g. https://git.example.com, or its in-cluster Service.
     */
    instanceUrl: string;
    /**
     * The labels workflows name in runs-on. <name>:docker://<image> runs the job in a container of that image in the engine; <name>:host, or a bare <name>, runs its steps in the runner container, which needs image. Default: ubuntu-latest, ubuntu-24.04 and ubuntu-22.04 as containers of Gitea's runner-images.
     */
    labels?: pulumi.Input<string[] | undefined>;
    /**
     * Node labels the runner pods require.
     */
    nodeSelector?: pulumi.Input<{
        [key: string]: string;
    } | undefined>;
    /**
     * The organisation (scope org), the user (scope user) or the repository's owner (scope repo). Not set for scope instance.
     */
    owner?: string;
    /**
     * Give each runner its own volume for the engine's images and containers, so a restart pulls nothing again. Default true; false uses an emptyDir.
     */
    persistentEngineStorage?: boolean;
    /**
     * A DNS-safe key for the pool; its runners live in namespace gitea-runners-<poolId> and register as <poolId>-0, <poolId>-1, ...
     */
    poolId: string;
    /**
     * The runner pods' PriorityClass, which must exist.
     */
    priorityClassName?: string;
    /**
     * A runner registration token for the scope: POST <instanceUrl>/api/v1/repos/{owner}/{repo}/actions/runners/registration-token for a repository; /api/v1/orgs/{org}, /api/v1/user (asked as that user) or /api/v1/admin in place of /api/v1/repos/{owner}/{repo} for the other scopes. One token registers any number of runners. Read only when a runner registers.
     */
    registrationToken: pulumi.Input<string>;
    /**
     * Runners. Default 1. With capacity, the pool takes replicas times capacity jobs at once.
     */
    replicas?: number;
    /**
     * The repository's name, without its owner. Only for scope repo.
     */
    repo?: string;
    /**
     * The runner container's CPU and memory, which is where host jobs run. No limits unless set.
     */
    runnerResources?: pulumi.Input<inputs.GiteaContainerResourcesArgs | undefined>;
    /**
     * The runner container's ephemeral storage limit, which holds the actions cache. Default 10Gi.
     */
    runnerStorageLimit?: string;
    /**
     * What the runners serve: instance, org, user or repo. The registration token must be one minted for it.
     */
    scope: string;
    /**
     * An existing ServiceAccount in the pool's namespace for the runner pods. Default: the namespace's default one. The pool creates none.
     */
    serviceAccountName?: string;
    /**
     * StorageClass of each runner's /data claim, which holds its registration. Default: the cluster's default class. With engine podman it must support idmapped mounts.
     */
    storageClassName?: string;
    /**
     * Size of each runner's /data claim. Default 1Gi. Changing a claim's size or class replaces the StatefulSet.
     */
    storageSize?: string;
    /**
     * Spread the pool's runners evenly across the nodes they may run on, hard: a runner that would unbalance them stays Pending. Default true.
     */
    topologySpread?: boolean;
    /**
     * Runners with an ordinal below this keep their old pod spec on a change, e.g. so a deploy running on them is not restarted by itself. Default 0.
     */
    updatePartition?: number;
    /**
     * The workspace host jobs share with the engine, an emptyDir. Used only with host labels. Default 10Gi.
     */
    workVolumeSize?: string;
}
//# sourceMappingURL=giteaRunnerPool.d.ts.map