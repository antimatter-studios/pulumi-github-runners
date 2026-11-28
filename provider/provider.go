// Package githubrunners is the github-runners Pulumi provider: self-hosted GitHub Actions runners on
// Kubernetes, the same way on every cluster that runs them.
//
// Every token is github-runners:index:<Type>. The design, its rules and the order of work are in
// the tracking issue, antimatter-studios/pulumi-github-runners#1.
package githubrunners

import (
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Name is the package name: the plugin binary is pulumi-resource-github-runners and every token
// starts with github-runners:.
const Name = "github-runners"

// Version is the provider's version. Release builds set it with
// -ldflags "-X github.com/antimatter-studios/pulumi-github-runners/provider.Version=<version>".
var Version = "0.0.0-dev"

// PluginDownloadURL is where Pulumi fetches the plugin: the assets of this repository's GitHub
// releases, which the release workflow attaches.
const PluginDownloadURL = "github://api.github.com/antimatter-studios"

// Provider builds the provider the plugin serves.
func Provider() p.Provider {
	return withPlainInputs(infer.Provider(options(defaultGitHubAPI)))
}

// providerWith builds the provider against another GitHub API base URL, for tests.
func providerWith(api string) p.Provider {
	return withPlainInputs(infer.Provider(options(api)))
}

func options(api string) infer.Options {
	return infer.NewProviderBuilder().
		WithResources(infer.Resource(&ForkPrApproval{api: api})).
		WithComponents(infer.ComponentF(NewController), infer.ComponentF(NewPool), infer.ComponentF(NewGateAgent)).
		// Every type is github-runners:index:<Type>. Without this the module would be the Go
		// package's directory, provider.
		WithModuleMap(map[tokens.ModuleName]tokens.ModuleName{"provider": "index"}).
		WithDisplayName("github-runners").
		WithDescription("Self-hosted GitHub Actions runners on Kubernetes: ARC, runner groups, rootless Docker, per-pool job hooks.").
		WithNamespace("antimatter-studios").
		WithRepository("https://github.com/antimatter-studios/pulumi-github-runners").
		WithLicense("MIT").
		WithPluginDownloadURL(PluginDownloadURL).
		// Left unset, Pulumi names the SDK @pulumi/github-runners, claiming Pulumi's own npm scope.
		WithLanguageMap(map[string]any{
			"nodejs": map[string]any{"packageName": "@antimatter-studios/github-runners"},
			// The Go SDK (sdk/go), for Go programs such as antimatter-vps; tagged sdk/go/vX.Y.Z on release.
			"go": map[string]any{
				"importBasePath":       "github.com/antimatter-studios/pulumi-github-runners/sdk/go",
				"modulePath":           "github.com/antimatter-studios/pulumi-github-runners/sdk/go",
				"rootPackageName":      "githubrunners",
				"respectSchemaVersion": true,
				// Components take object inputs as plain values; Go needs their Args types generated too.
				"generateExtraInputTypes": true,
			},
		}).
		WithKeywords("github", "actions", "runners", "kubernetes", "category/infrastructure").
		BuildOptions()
}

// fixedName goes on every ConfigMap and Secret the plugin names itself. pulumi-kubernetes replaces a
// ConfigMap or Secret whose data changes, creating the new one before deleting the old, and with a
// fixed name the create fails: github-runners v0.6.0's new registries-mirror.conf previewed as
// 'configmaps "arc-runner-podman" already exists' on the Ryzen. Deleting first leaves a moment without
// the object, which a pod starting in it waits out.
var fixedName = pulumi.DeleteBeforeReplace(true)
