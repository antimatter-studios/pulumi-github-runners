package githubrunners

import (
	_ "embed"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// gateClaimHook claims a job's runner-gate reservation through the cluster's gate agent. It fails
// open: a gate that cannot be reached lets the job run. Only an expired reservation fails the job.
//
// gate-claim.sh is vendored verbatim from antimatter-vps, runner-hooks/gate-claim.sh at commit
// 3d43b1b (blob 6c4977a). Do not edit it here: change it there, where tests keep it equal to the
// end of the VPS's own job-started hook, then copy it again and update this commit.
//
//go:embed gate-claim.sh
var gateClaimHook string

const (
	gateClaimFile = "gate-claim.sh"
	gateClaimEnv  = "RUNNER_GATE_URL"
)

var namespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// gateClaimURL is the base URL the claim script asks, from inputs that are known: poolValues writes
// the values once they are.
func gateClaimURL(c PoolGateClaim) string {
	if u := orDefault(knownStringPtr(c.URL), ""); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return gateAgentClaimURL(orDefault(knownStringPtr(c.AgentNamespace), defaultGateAgentNamespace))
}

// givenString reports whether an input is set to something: a value that is not empty, or one a
// preview does not know yet.
func givenString(in pulumi.StringPtrInput) bool {
	if in == nil {
		return false
	}
	v, ok := known[*string](in.ToStringPtrOutput())
	return !ok || orDefault(v, "") != ""
}

func checkGateClaim(c PoolGateClaim) error {
	if givenString(c.URL) == givenString(c.AgentNamespace) {
		return fmt.Errorf("gateClaim: give exactly one of url or agentNamespace")
	}
	// A value a preview does not know yet is checked when it is known, on the update.
	if givenString(c.AgentNamespace) {
		if namespace := orDefault(knownStringPtr(c.AgentNamespace), ""); namespace != "" && !namespacePattern.MatchString(namespace) {
			return fmt.Errorf("gateClaim.agentNamespace %q must be a namespace name", namespace)
		}
		return nil
	}
	raw := orDefault(knownStringPtr(c.URL), "")
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("gateClaim.url %q must be an http or https base URL, without a query or fragment", raw)
	}
	return nil
}

// poolNetworkPolicy is the pool's network policy, letting its runners reach the agent in
// gateClaim.agentNamespace unless the policy names a namespace itself, and the registry mirror when
// the pool pulls through one, whose address is private. Only for a pool with one.
func poolNetworkPolicy(a PoolArgs) PoolNetworkPolicy {
	n := *a.NetworkPolicy
	if !givenString(n.GateAgentNamespace) && a.GateClaim != nil {
		n.GateAgentNamespace = a.GateClaim.AgentNamespace
	}
	if a.RegistryMirror != nil {
		n.Allow = append(append([]NetworkAllow{}, n.Allow...), NetworkAllow{
			NamespaceLabels: map[string]string{"kubernetes.io/metadata.name": poolMirrorNamespace(*a.RegistryMirror)},
			PodLabels:       map[string]string{"app": registryMirrorName},
			Ports:           []int{registryMirrorPort},
		})
	}
	return n
}
