import * as pulumi from "@pulumi/pulumi";
/**
 * An organisation's fork pull request approval policy. Deleting the resource leaves the setting as it is.
 */
export declare class ForkPrApproval extends pulumi.CustomResource {
    /**
     * Get an existing ForkPrApproval resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): ForkPrApproval;
    /**
     * Check whether an existing ForkPrApproval resource with the given ID exists.
     *
     * @param id The _unique_ provider ID of the resource to check.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static exists(id: pulumi.Input<string>, opts?: pulumi.CustomResourceOptions): pulumi.Output<boolean>;
    /**
     * Returns true if the given object is an instance of ForkPrApproval.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is ForkPrApproval;
    /**
     * The organisation's login.
     */
    readonly org: pulumi.Output<string>;
    /**
     * Which contributors need approval before a fork's pull request runs workflows: first_time_contributors_new_to_github, first_time_contributors or all_external_contributors.
     */
    readonly policy: pulumi.Output<string>;
    /**
     * A token with organisation Administration: read and write.
     */
    readonly token: pulumi.Output<string>;
    /**
     * Create a ForkPrApproval resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: ForkPrApprovalArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a ForkPrApproval resource.
 */
export interface ForkPrApprovalArgs {
    /**
     * The organisation's login.
     */
    org: pulumi.Input<string>;
    /**
     * Which contributors need approval before a fork's pull request runs workflows: first_time_contributors_new_to_github, first_time_contributors or all_external_contributors.
     */
    policy: pulumi.Input<string>;
    /**
     * A token with organisation Administration: read and write.
     */
    token: pulumi.Input<string>;
}
//# sourceMappingURL=forkPrApproval.d.ts.map