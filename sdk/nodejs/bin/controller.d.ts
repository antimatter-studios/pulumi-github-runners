import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
/**
 * Actions Runner Controller for one cluster, and the device plugins its runner pods use.
 */
export declare class Controller extends pulumi.ComponentResource {
    /**
     * Returns true if the given object is an instance of Controller.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Controller;
    readonly namespace: pulumi.Output<string>;
    /**
     * Create a Controller resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args?: ControllerArgs, opts?: pulumi.ComponentResourceOptions);
}
/**
 * The set of arguments for constructing a Controller resource.
 */
export interface ControllerArgs {
    devicePluginImage?: string;
    /**
     * Host devices offered to runner pods, e.g. {name: fuse, paths: [/dev/fuse]} as devic.es/fuse.
     */
    devices?: pulumi.Input<pulumi.Input<inputs.DeviceOfferArgs>[] | undefined>;
    /**
     * Serve the listeners' metrics on :8080/metrics, which the runner-gate reads. Default true.
     */
    listenerMetrics?: boolean;
    /**
     * Where the controller runs. Default arc-systems.
     */
    namespace?: string;
    /**
     * Keep runner images on every node with a DaemonSet, so pods scaled from zero do not wait for them to pull. Off unless set; {} pre-pulls the default runner and podman images.
     */
    prePull?: pulumi.Input<inputs.PrePullArgs | undefined>;
    /**
     * The controller manager pod's PriorityClass, e.g. a high one so runners cannot starve it. Default: none. The class must exist. The listeners' is each Pool's listenerPriorityClassName.
     */
    priorityClassName?: string;
    /**
     * The controller manager container's requests and limits. Set, it replaces the default block whole (requests 100m CPU and 128Mi, limits 500m and 512Mi), so a limit left out is no limit.
     */
    resources?: pulumi.Input<inputs.ContainerResourcesArgs | undefined>;
    /**
     * The ARC chart version. Default 0.15.1.
     */
    version?: string;
}
//# sourceMappingURL=controller.d.ts.map