package githubrunners

import (
	"github.com/pulumi/pulumi-go-provider/infer"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apps/v1"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	networkingv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	rbacv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/rbac/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// GateAgent publishes a cluster's runner pools and serves its in-cluster claim endpoint.
type GateAgent struct {
	pulumi.ResourceState
	ClaimURL pulumi.StringOutput `pulumi:"claimUrl"`
}

func (g *GateAgent) Annotate(an infer.Annotator) {
	an.Describe(g, "Reports one cluster's runner pools to the public runner-gate and serves its in-cluster claim endpoint.")
	an.Describe(&g.ClaimURL, "The in-cluster base URL for the runner's claim hook (RUNNER_GATE_URL).")
	an.SetToken("index", "GateAgent")
}

// NewGateAgent deploys one GateAgent into existing cluster namespaces.
func NewGateAgent(ctx *pulumi.Context, name string, a GateAgentArgs, opts ...pulumi.ResourceOption) (*GateAgent, error) {
	if err := checkGateAgent(a); err != nil {
		return nil, err
	}
	agent := &GateAgent{}
	if err := ctx.RegisterComponentResource("github-runners:index:GateAgent", name, agent, opts...); err != nil {
		return nil, err
	}
	child := pulumi.Parent(agent)
	namespace := orDefault(a.Namespace, defaultGateAgentNamespace)

	token, err := corev1.NewSecret(ctx, name+"-token", gateAgentTokenSecret(namespace, a.Token), child)
	if err != nil {
		return nil, err
	}
	pullSecret, err := corev1.NewSecret(ctx, name+"-image-pull", gateAgentImagePullSecret(namespace, a.ImagePull), child)
	if err != nil {
		return nil, err
	}
	account, err := corev1.NewServiceAccount(ctx, name+"-account", &corev1.ServiceAccountArgs{
		Metadata: gateAgentMetadata(gateAgentName, namespace),
	}, child)
	if err != nil {
		return nil, err
	}
	deps := []pulumi.Resource{token, pullSecret, account}
	for _, pair := range gateAgentNamespacedRoles(a, namespace) {
		role, err := rbacv1.NewRole(ctx, name+"-"+pair.suffix+"-role", pair.role, child)
		if err != nil {
			return nil, err
		}
		binding, err := rbacv1.NewRoleBinding(ctx, name+"-"+pair.suffix+"-binding", pair.binding,
			child, pulumi.DependsOn([]pulumi.Resource{role, account}))
		if err != nil {
			return nil, err
		}
		deps = append(deps, binding)
	}
	roleArgs, bindingArgs := gateAgentClusterRole(a, namespace)
	role, err := rbacv1.NewClusterRole(ctx, name+"-node-role", roleArgs, child)
	if err != nil {
		return nil, err
	}
	binding, err := rbacv1.NewClusterRoleBinding(ctx, name+"-node-binding", bindingArgs,
		child, pulumi.DependsOn([]pulumi.Resource{role, account}))
	if err != nil {
		return nil, err
	}
	deps = append(deps, binding)

	if a.NetworkPolicy == nil || a.NetworkPolicy.Enabled == nil || *a.NetworkPolicy.Enabled {
		policy, err := networkingv1.NewNetworkPolicy(ctx, name+"-ingress", gateAgentNetworkPolicy(a, namespace), child)
		if err != nil {
			return nil, err
		}
		// Before the Deployment, so the agent never serves unguarded.
		deps = append(deps, policy)
	}

	if _, err := appsv1.NewDeployment(ctx, name+"-agent", gateAgentDeployment(a, namespace),
		child, pulumi.DependsOn(deps)); err != nil {
		return nil, err
	}
	if _, err := corev1.NewService(ctx, name+"-service", gateAgentService(namespace), child); err != nil {
		return nil, err
	}
	agent.ClaimURL = pulumi.String(gateAgentClaimURL(namespace)).ToStringOutput()
	if err := ctx.RegisterResourceOutputs(agent, pulumi.Map{"claimUrl": agent.ClaimURL}); err != nil {
		return nil, err
	}
	return agent, nil
}
