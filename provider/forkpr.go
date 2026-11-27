package githubrunners

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

const defaultGitHubAPI = "https://api.github.com"

// The three values GitHub accepts, from least to most strict.
var forkPrPolicies = []string{
	"first_time_contributors_new_to_github",
	"first_time_contributors",
	"all_external_contributors",
}

// ForkPrApproval is an organisation's "Approval for running fork pull request workflows from
// contributors" setting.
//
// The GitHub provider has no resource for it, so this calls the REST API itself. Self-hosted
// runners that accept public repositories depend on it as a backstop for the job hook that refuses
// forks. Delete leaves the setting as it is: removing the resource from a stack must never quietly
// loosen an organisation.
type ForkPrApproval struct {
	api string
}

// ForkPrApprovalArgs are the inputs of a ForkPrApproval.
type ForkPrApprovalArgs struct {
	Org    string `pulumi:"org"`
	Policy string `pulumi:"policy"`
	Token  string `pulumi:"token" provider:"secret"`
}

// ForkPrApprovalState is what the organisation reports.
type ForkPrApprovalState struct {
	ForkPrApprovalArgs
}

func (a *ForkPrApprovalArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Org, "The organisation's login.")
	an.Describe(&a.Policy, "Which contributors need approval before a fork's pull request runs workflows: "+
		"first_time_contributors_new_to_github, first_time_contributors or all_external_contributors.")
	an.Describe(&a.Token, "A token with organisation Administration: read and write.")
}

func (f *ForkPrApproval) Annotate(an infer.Annotator) {
	an.Describe(f, "An organisation's fork pull request approval policy. Deleting the resource leaves the setting as it is.")
}

func checkForkPrArgs(args ForkPrApprovalArgs) error {
	if args.Org == "" {
		return fmt.Errorf("org is required")
	}
	for _, p := range forkPrPolicies {
		if args.Policy == p {
			return nil
		}
	}
	return fmt.Errorf("policy %q is not one of %v", args.Policy, forkPrPolicies)
}

type forkPrBody struct {
	ApprovalPolicy string `json:"approval_policy"`
}

// call makes one request to the setting's endpoint and decodes the reply into out when given.
func (f *ForkPrApproval) call(ctx context.Context, method string, args ForkPrApprovalArgs, body any, out any) error {
	base := f.api
	if base == "" {
		base = defaultGitHubAPI
	}
	endpoint := fmt.Sprintf("%s/orgs/%s/actions/permissions/fork-pr-contributor-approval", base, url.PathEscape(args.Org))
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+args.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s fork PR approval for %s: %w", method, args.Org, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The token never appears here: GitHub does not echo it, and only the body is reported.
		return fmt.Errorf("%s fork PR approval for %s: HTTP %d: %s", method, args.Org, resp.StatusCode, bytes.TrimSpace(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s fork PR approval for %s: %w", method, args.Org, err)
		}
	}
	return nil
}

func (f *ForkPrApproval) set(ctx context.Context, args ForkPrApprovalArgs) error {
	if err := checkForkPrArgs(args); err != nil {
		return err
	}
	return f.call(ctx, http.MethodPut, args, forkPrBody{ApprovalPolicy: args.Policy}, nil)
}

func (f *ForkPrApproval) Create(ctx context.Context, req infer.CreateRequest[ForkPrApprovalArgs]) (infer.CreateResponse[ForkPrApprovalState], error) {
	if err := checkForkPrArgs(req.Inputs); err != nil {
		return infer.CreateResponse[ForkPrApprovalState]{}, err
	}
	state := ForkPrApprovalState{req.Inputs}
	if req.DryRun {
		return infer.CreateResponse[ForkPrApprovalState]{ID: req.Inputs.Org, Output: state}, nil
	}
	if err := f.set(ctx, req.Inputs); err != nil {
		return infer.CreateResponse[ForkPrApprovalState]{}, err
	}
	return infer.CreateResponse[ForkPrApprovalState]{ID: req.Inputs.Org, Output: state}, nil
}

// Read reports the policy the organisation has now, so a change made in GitHub's settings shows as
// drift on the next refresh.
func (f *ForkPrApproval) Read(ctx context.Context, req infer.ReadRequest[ForkPrApprovalArgs, ForkPrApprovalState]) (infer.ReadResponse[ForkPrApprovalArgs, ForkPrApprovalState], error) {
	args := req.State.ForkPrApprovalArgs
	if args.Org == "" {
		args.Org = req.ID
	}
	if args.Token == "" {
		args.Token = req.Inputs.Token
	}
	var got forkPrBody
	if err := f.call(ctx, http.MethodGet, args, nil, &got); err != nil {
		return infer.ReadResponse[ForkPrApprovalArgs, ForkPrApprovalState]{}, err
	}
	state := ForkPrApprovalState{args}
	state.Policy = got.ApprovalPolicy
	return infer.ReadResponse[ForkPrApprovalArgs, ForkPrApprovalState]{ID: req.ID, Inputs: req.Inputs, State: state}, nil
}

func (f *ForkPrApproval) Update(ctx context.Context, req infer.UpdateRequest[ForkPrApprovalArgs, ForkPrApprovalState]) (infer.UpdateResponse[ForkPrApprovalState], error) {
	if err := checkForkPrArgs(req.Inputs); err != nil {
		return infer.UpdateResponse[ForkPrApprovalState]{}, err
	}
	state := ForkPrApprovalState{req.Inputs}
	if req.DryRun {
		return infer.UpdateResponse[ForkPrApprovalState]{Output: state}, nil
	}
	if err := f.set(ctx, req.Inputs); err != nil {
		return infer.UpdateResponse[ForkPrApprovalState]{}, err
	}
	return infer.UpdateResponse[ForkPrApprovalState]{Output: state}, nil
}

// Diff replaces on a change of organisation and updates on anything else, the token included, so a
// rotated token is used for the next write without touching the setting.
func (f *ForkPrApproval) Diff(_ context.Context, req infer.DiffRequest[ForkPrApprovalArgs, ForkPrApprovalState]) (p.DiffResponse, error) {
	resp := p.DiffResponse{DetailedDiff: map[string]p.PropertyDiff{}}
	if req.Inputs.Org != req.State.Org {
		resp.DetailedDiff["org"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if req.Inputs.Policy != req.State.Policy {
		resp.DetailedDiff["policy"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.Inputs.Token != req.State.Token {
		resp.DetailedDiff["token"] = p.PropertyDiff{Kind: p.Update}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete leaves the organisation's setting alone, on purpose: see ForkPrApproval.
func (f *ForkPrApproval) Delete(context.Context, infer.DeleteRequest[ForkPrApprovalState]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, nil
}
