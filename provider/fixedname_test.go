package githubrunners

import "testing"

// pulumi-kubernetes replaces a ConfigMap or Secret whose data changes, and creating the replacement
// first fails when the name is fixed ("configmaps \"arc-runner-podman\" already exists"). Every one
// the plugin names must be deleted before it is replaced.
func TestEveryNamedConfigMapAndSecretIsDeletedBeforeItIsReplaced(t *testing.T) {
	pool := withApp(basePool())
	pool.Hook = &PoolHook{Script: sp("true")}
	pool.RegistryMirror = &PoolRegistryMirror{}
	pool.NetworkPolicy = &PoolNetworkPolicy{}
	runs := map[string]*mocks{
		"pool": runPool(t, pool),
		"controller": runController(t, ControllerArgs{
			RegistryMirror: &RegistryMirror{},
			Monitoring:     &ControllerMonitoring{Dashboard: &MonitoringDashboard{}},
		}),
		"gate agent": runGateAgent(t, testGateAgentArgs()),
	}
	seen := 0
	for what, m := range runs {
		for _, r := range m.all {
			if r.typ != "kubernetes:core/v1:ConfigMap" && r.typ != "kubernetes:core/v1:Secret" {
				continue
			}
			meta := r.inputs["metadata"].ObjectValue()
			if !meta.HasValue("name") {
				continue
			}
			seen++
			if !r.deleteBeforeReplace {
				t.Errorf("%s: %s %s (%s) is not deleted before it is replaced", what, r.typ, r.name, meta["name"].StringValue())
			}
		}
	}
	// pool: secret, hooks, podman; controller: mirror config, dashboard; gate agent: token, image pull.
	if seen != 7 {
		t.Fatalf("checked %d named ConfigMaps and Secrets, want 7", seen)
	}
}
