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
	return infer.Provider(options(defaultGitHubAPI))
}

// providerWith builds the provider against another GitHub API base URL, for tests.
func providerWith(api string) p.Provider {
	return infer.Provider(options(api))
}

func options(api string) infer.Options {
	return infer.NewProviderBuilder().
		WithResources(infer.Resource(&ForkPrApproval{api: api})).
		WithComponents(infer.ComponentF(NewController), infer.ComponentF(NewPool)).
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
		WithLanguageMap(map[string]any{"nodejs": map[string]any{"packageName": "@antimatter-studios/github-runners"}}).
		WithKeywords("github", "actions", "runners", "kubernetes", "category/infrastructure").
		BuildOptions()
}
