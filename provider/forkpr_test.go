package githubrunners

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// fakeOrg is one organisation's setting behind GitHub's endpoint, recording every request.
type fakeOrg struct {
	mu       sync.Mutex
	policy   string
	token    string
	requests []string
}

func (f *fakeOrg) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/orgs/acme/actions/permissions/fork-pr-contributor-approval" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(forkPrBody{ApprovalPolicy: f.policy})
		case http.MethodPut:
			var body forkPrBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.policy = body.ApprovalPolicy
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func args(policy string) ForkPrApprovalArgs {
	return ForkPrApprovalArgs{Org: "acme", Policy: policy, Token: "t0k"}
}

func TestCreateSetsThePolicy(t *testing.T) {
	org := &fakeOrg{policy: "first_time_contributors", token: "t0k"}
	r := &ForkPrApproval{api: org.server(t).URL}
	resp, err := r.Create(context.Background(), infer.CreateRequest[ForkPrApprovalArgs]{Inputs: args("all_external_contributors")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "acme" || org.policy != "all_external_contributors" {
		t.Fatalf("id %q, policy now %q", resp.ID, org.policy)
	}
}

func TestPreviewWritesNothing(t *testing.T) {
	org := &fakeOrg{policy: "first_time_contributors", token: "t0k"}
	r := &ForkPrApproval{api: org.server(t).URL}
	if _, err := r.Create(context.Background(), infer.CreateRequest[ForkPrApprovalArgs]{Inputs: args("all_external_contributors"), DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(context.Background(), infer.UpdateRequest[ForkPrApprovalArgs, ForkPrApprovalState]{Inputs: args("all_external_contributors"), DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if len(org.requests) != 0 || org.policy != "first_time_contributors" {
		t.Fatalf("a preview reached GitHub: %v, policy %q", org.requests, org.policy)
	}
}

func TestReadReportsAChangeMadeInGitHub(t *testing.T) {
	org := &fakeOrg{policy: "first_time_contributors", token: "t0k"}
	r := &ForkPrApproval{api: org.server(t).URL}
	state := ForkPrApprovalState{args("all_external_contributors")}
	resp, err := r.Read(context.Background(), infer.ReadRequest[ForkPrApprovalArgs, ForkPrApprovalState]{ID: "acme", Inputs: args("all_external_contributors"), State: state})
	if err != nil {
		t.Fatal(err)
	}
	if resp.State.Policy != "first_time_contributors" {
		t.Fatalf("read %q, want the organisation's first_time_contributors", resp.State.Policy)
	}
}

func TestDeleteLeavesTheSettingAlone(t *testing.T) {
	org := &fakeOrg{policy: "all_external_contributors", token: "t0k"}
	r := &ForkPrApproval{api: org.server(t).URL}
	if _, err := r.Delete(context.Background(), infer.DeleteRequest[ForkPrApprovalState]{ID: "acme", State: ForkPrApprovalState{args("all_external_contributors")}}); err != nil {
		t.Fatal(err)
	}
	if len(org.requests) != 0 || org.policy != "all_external_contributors" {
		t.Fatalf("delete touched the organisation: %v", org.requests)
	}
}

func TestAnUnknownPolicyIsRefusedBeforeAnyRequest(t *testing.T) {
	org := &fakeOrg{policy: "first_time_contributors", token: "t0k"}
	r := &ForkPrApproval{api: org.server(t).URL}
	_, err := r.Create(context.Background(), infer.CreateRequest[ForkPrApprovalArgs]{Inputs: args("everyone")})
	if err == nil || !strings.Contains(err.Error(), `policy "everyone"`) || len(org.requests) != 0 {
		t.Fatalf("err %v, requests %v", err, org.requests)
	}
}

func TestAForbiddenTokenReportsGitHubsAnswerWithoutTheToken(t *testing.T) {
	org := &fakeOrg{policy: "first_time_contributors", token: "right"}
	r := &ForkPrApproval{api: org.server(t).URL}
	_, err := r.Create(context.Background(), infer.CreateRequest[ForkPrApprovalArgs]{Inputs: args("all_external_contributors")})
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "personal access token") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "t0k") {
		t.Fatalf("the error leaks the token: %v", err)
	}
}

func TestDiff(t *testing.T) {
	r := &ForkPrApproval{}
	cases := []struct {
		name    string
		inputs  ForkPrApprovalArgs
		changes map[string]p.DiffKind
	}{
		{"nothing changed", args("all_external_contributors"), map[string]p.DiffKind{}},
		{"policy", args("first_time_contributors"), map[string]p.DiffKind{"policy": p.Update}},
		{"rotated token", ForkPrApprovalArgs{Org: "acme", Policy: "all_external_contributors", Token: "new"}, map[string]p.DiffKind{"token": p.Update}},
		{"another organisation", ForkPrApprovalArgs{Org: "other", Policy: "all_external_contributors", Token: "t0k"}, map[string]p.DiffKind{"org": p.UpdateReplace}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := r.Diff(context.Background(), infer.DiffRequest[ForkPrApprovalArgs, ForkPrApprovalState]{
				Inputs: c.inputs, State: ForkPrApprovalState{args("all_external_contributors")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if resp.HasChanges != (len(c.changes) > 0) || len(resp.DetailedDiff) != len(c.changes) {
				t.Fatalf("got %+v", resp)
			}
			for k, kind := range c.changes {
				if resp.DetailedDiff[k].Kind != kind {
					t.Errorf("%s: %v, want %v", k, resp.DetailedDiff[k].Kind, kind)
				}
			}
		})
	}
}
