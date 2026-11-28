package githubrunners

import (
	"context"
	"reflect"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
)

// A component's inputs come from a program in another language through Construct. Anything that
// program passes as an Output (a secret, a value with a dependency, a value a preview does not know
// yet) arrives as an Output, and a plain Go field refuses it: "copying input ...: expected
// destination type to implement pulumi.Input or pulumi.Output". So every input a program could
// reasonably hand over as an Output is an Input here, objects included: a credentials object read
// with getSecretObject is one Output for the whole object, which only an Input-typed object takes.

// resolved is an input's value, for the decisions that need the value itself, such as how many
// resources to create and what to call them. Children built from it must carry Secret and Deps on.
type resolved[T any] struct {
	Value  T
	Known  bool
	Secret bool
	Deps   []pulumi.Resource
}

// resolve waits for an input. Construct resolves every input before the component runs, so this
// returns at once; a preview can leave a value unknown, and Known reports that.
func resolve[T any](ctx context.Context, in pulumi.Input) (resolved[T], error) {
	var out resolved[T]
	if in == nil || (reflect.ValueOf(in).Kind() == reflect.Pointer && reflect.ValueOf(in).IsNil()) {
		out.Known = true
		return out, nil
	}
	result, err := internals.UnsafeAwaitOutput(ctx, pulumi.ToOutput(in))
	if err != nil {
		return out, err
	}
	out.Known, out.Secret, out.Deps = result.Known, result.Secret, result.Dependencies
	if v, ok := result.Value.(T); ok {
		out.Value = v
	}
	return out, nil
}

// known is an input's value when it is known now, for validation and for the plain values the
// specs are written from. The zero value and false when it is not.
func known[T any](in pulumi.Input) (T, bool) {
	r, err := resolve[T](context.Background(), in)
	if err != nil || !r.Known {
		var zero T
		return zero, false
	}
	return r.Value, true
}

// knownStringPtr is a string pointer input's value, nil when unset or not known.
func knownStringPtr(in pulumi.StringPtrInput) *string {
	if in == nil {
		return nil
	}
	// Through ToStringPtrOutput: a value may arrive as an Output of a string.
	v, ok := known[*string](in.ToStringPtrOutput())
	if !ok {
		return nil
	}
	return v
}

// knownStringMap is a string map input's value, nil when unset or not known.
func knownStringMap(in pulumi.StringMapInput) map[string]string {
	v, _ := known[map[string]string](in)
	return v
}

// stringOr is the input with def where it is unset or empty, as an Output, so a secret, a dependency
// or an unknown carries through to whatever uses it.
func stringOr(in pulumi.StringPtrInput, def string) pulumi.StringOutput {
	if in == nil {
		return pulumi.String(def).ToStringOutput()
	}
	return in.ToStringPtrOutput().ApplyT(func(v *string) string {
		if v == nil || *v == "" {
			return def
		}
		return *v
	}).(pulumi.StringOutput)
}

func stringMapOutput(in pulumi.StringMapInput) pulumi.StringMapOutput {
	if in == nil {
		return pulumi.StringMap{}.ToStringMapOutput()
	}
	return in.ToStringMapOutput()
}

// ---- GitHubApp ----

// app is the pool's GitHub App, or nil.
func (a PoolArgs) app() GitHubAppInput {
	if a.App == nil {
		return nil
	}
	return *a.App
}

// AppInput puts a GitHub App where PoolArgs.App takes it.
func AppInput(app GitHubAppInput) *GitHubAppInput { return &app }

// GitHubApp is the recommended credential for a pool.
type GitHubApp struct {
	AppID          string `pulumi:"appId"`
	InstallationID string `pulumi:"installationId"`
	PrivateKey     string `pulumi:"privateKey" provider:"secret"`
}

// GitHubAppInput is a GitHubApp, or an Output of one.
type GitHubAppInput interface {
	pulumi.Input
	ToGitHubAppOutput() GitHubAppOutput
	ToGitHubAppOutputWithContext(context.Context) GitHubAppOutput
}

// GitHubAppArgs is a GitHubApp whose fields may each be Outputs.
type GitHubAppArgs struct {
	AppID          pulumi.StringInput `pulumi:"appId"`
	InstallationID pulumi.StringInput `pulumi:"installationId"`
	PrivateKey     pulumi.StringInput `pulumi:"privateKey"`
}

func (GitHubAppArgs) ElementType() reflect.Type { return reflect.TypeFor[GitHubApp]() }

func (a GitHubAppArgs) ToGitHubAppOutput() GitHubAppOutput {
	return a.ToGitHubAppOutputWithContext(context.Background())
}

func (a GitHubAppArgs) ToGitHubAppOutputWithContext(ctx context.Context) GitHubAppOutput {
	return pulumi.ToOutputWithContext(ctx, a).(GitHubAppOutput)
}

// GitHubAppOutput is an Output of a GitHubApp.
type GitHubAppOutput struct{ *pulumi.OutputState }

func (GitHubAppOutput) ElementType() reflect.Type { return reflect.TypeFor[GitHubApp]() }

func (o GitHubAppOutput) ToGitHubAppOutput() GitHubAppOutput { return o }

func (o GitHubAppOutput) ToGitHubAppOutputWithContext(context.Context) GitHubAppOutput { return o }

func (o GitHubAppOutput) AppID() pulumi.StringOutput {
	return o.ApplyT(func(a GitHubApp) string { return a.AppID }).(pulumi.StringOutput)
}

func (o GitHubAppOutput) InstallationID() pulumi.StringOutput {
	return o.ApplyT(func(a GitHubApp) string { return a.InstallationID }).(pulumi.StringOutput)
}

// PrivateKey is always secret, however the key arrived.
func (o GitHubAppOutput) PrivateKey() pulumi.StringOutput {
	return pulumi.ToSecret(o.ApplyT(func(a GitHubApp) string { return a.PrivateKey })).(pulumi.StringOutput)
}

// ---- GateAgentImagePull ----

// GateAgentImagePull supplies the credentials for the agent image registry.
type GateAgentImagePull struct {
	Registry *string `pulumi:"registry,optional"`
	Username string  `pulumi:"username"`
	Password string  `pulumi:"password" provider:"secret"`
}

// GateAgentImagePullInput is a GateAgentImagePull, or an Output of one.
type GateAgentImagePullInput interface {
	pulumi.Input
	ToGateAgentImagePullOutput() GateAgentImagePullOutput
	ToGateAgentImagePullOutputWithContext(context.Context) GateAgentImagePullOutput
}

// GateAgentImagePullArgs is a GateAgentImagePull whose fields may each be Outputs.
type GateAgentImagePullArgs struct {
	Registry pulumi.StringPtrInput `pulumi:"registry"`
	Username pulumi.StringInput    `pulumi:"username"`
	Password pulumi.StringInput    `pulumi:"password"`
}

func (GateAgentImagePullArgs) ElementType() reflect.Type {
	return reflect.TypeFor[GateAgentImagePull]()
}

func (a GateAgentImagePullArgs) ToGateAgentImagePullOutput() GateAgentImagePullOutput {
	return a.ToGateAgentImagePullOutputWithContext(context.Background())
}

func (a GateAgentImagePullArgs) ToGateAgentImagePullOutputWithContext(ctx context.Context) GateAgentImagePullOutput {
	return pulumi.ToOutputWithContext(ctx, a).(GateAgentImagePullOutput)
}

// GateAgentImagePullOutput is an Output of a GateAgentImagePull.
type GateAgentImagePullOutput struct{ *pulumi.OutputState }

func (GateAgentImagePullOutput) ElementType() reflect.Type {
	return reflect.TypeFor[GateAgentImagePull]()
}

func (o GateAgentImagePullOutput) ToGateAgentImagePullOutput() GateAgentImagePullOutput { return o }

func (o GateAgentImagePullOutput) ToGateAgentImagePullOutputWithContext(context.Context) GateAgentImagePullOutput {
	return o
}

func init() {
	pulumi.RegisterInputType(reflect.TypeFor[GitHubAppInput](), GitHubAppArgs{})
	pulumi.RegisterOutputType(GitHubAppOutput{})
	pulumi.RegisterInputType(reflect.TypeFor[GateAgentImagePullInput](), GateAgentImagePullArgs{})
	pulumi.RegisterOutputType(GateAgentImagePullOutput{})
}
