// Command pulumi-resource-github-runners is the github-runners provider plugin. Pulumi starts it by
// name; it serves the provider over gRPC on the port it prints, and exits when the engine is done
// with it.
package main

import (
	"context"
	"fmt"
	"os"

	githubrunners "github.com/antimatter-studios/pulumi-github-runners/provider"
)

func main() {
	if err := githubrunners.Provider().Run(context.Background(), githubrunners.Name, githubrunners.Version); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}
