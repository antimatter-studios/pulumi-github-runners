package githubrunners

import (
	_ "embed"
	"fmt"
	"net/url"
	"regexp"
	"strings"
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

// gateClaimURL is the base URL the claim script asks.
func gateClaimURL(c PoolGateClaim) string {
	if u := orDefault(c.URL, ""); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	return gateAgentClaimURL(orDefault(c.AgentNamespace, defaultGateAgentNamespace))
}

func checkGateClaim(c PoolGateClaim) error {
	raw, namespace := orDefault(c.URL, ""), orDefault(c.AgentNamespace, "")
	if (raw == "") == (namespace == "") {
		return fmt.Errorf("gateClaim: give exactly one of url or agentNamespace")
	}
	if namespace != "" {
		if !namespacePattern.MatchString(namespace) {
			return fmt.Errorf("gateClaim.agentNamespace %q must be a namespace name", namespace)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("gateClaim.url %q must be an http or https base URL, without a query or fragment", raw)
	}
	return nil
}

// poolNetworkPolicy is the pool's network policy, letting its runners reach the agent in
// gateClaim.agentNamespace unless the policy names a namespace itself. Only for a pool with one.
func poolNetworkPolicy(a PoolArgs) PoolNetworkPolicy {
	n := *a.NetworkPolicy
	if orDefault(n.GateAgentNamespace, "") == "" && a.GateClaim != nil {
		n.GateAgentNamespace = a.GateClaim.AgentNamespace
	}
	return n
}
