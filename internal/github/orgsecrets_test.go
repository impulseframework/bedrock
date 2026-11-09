package github_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

// TestPutOrgSecret: the value arrives sealed with the organization's key, so that what
// the stand-in opens is the value, with the selection as an array, an empty one for none.
func TestPutOrgSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   []byte
		ids     []int64
		wantIDs []int64
	}{
		{name: "a value for two repositories", value: []byte("the deploy key"), ids: []int64{1001, 1002}, wantIDs: []int64{1001, 1002}},
		{name: "no repository, given as nil, arrives as an empty list", value: []byte("the deploy key"), ids: nil, wantIDs: []int64{}},
		{name: "no repository, given as an empty list, arrives as one", value: []byte("the deploy key"), ids: []int64{}, wantIDs: []int64{}},
		{name: "a value of several lines and bytes that are not text", value: []byte("-----BEGIN KEY-----\nline\n\x00\xff\n-----END KEY-----\n"), ids: []int64{7}, wantIDs: []int64{7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := githubtest.New(t)
			before := time.Now()
			if err := srv.Client().PutOrgSecret(t.Context(), "acme", "DEPLOY_KEY", tt.value, tt.ids); err != nil {
				t.Fatalf("PutOrgSecret() error = %v", err)
			}
			got := srv.OrgSecrets["acme"]["DEPLOY_KEY"]
			if got == nil {
				t.Fatal("the stand-in holds no secret after the put")
			}
			if !bytes.Equal(got.Value, tt.value) {
				t.Errorf("the stand-in opened %q, want %q", got.Value, tt.value)
			}
			if got.Visibility != github.VisibilitySelected {
				t.Errorf("visibility = %q, want %q", got.Visibility, github.VisibilitySelected)
			}
			if diff := cmp.Diff(tt.wantIDs, got.RepositoryIDs); diff != "" {
				t.Errorf("repository ids mismatch (-want +got):\n%s", diff)
			}
			if got.UpdatedAt.Before(before) {
				t.Errorf("updated at %s, before the put at %s", got.UpdatedAt, before)
			}
			wantCalls := []string{"GET /orgs/acme/actions/secrets/public-key", "PUT /orgs/acme/actions/secrets/DEPLOY_KEY"}
			if diff := cmp.Diff(wantCalls, srv.Calls); diff != "" {
				t.Errorf("calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPutOrgSecretAgain: a second put replaces the value and the selection and moves the
// time of the secret on.
func TestPutOrgSecretAgain(t *testing.T) {
	t.Parallel()

	srv := githubtest.New(t)
	c := srv.Client()
	if err := c.PutOrgSecret(t.Context(), "acme", "DEPLOY_KEY", []byte("the first value"), []int64{1001, 1002}); err != nil {
		t.Fatalf("PutOrgSecret() error = %v", err)
	}
	first := srv.OrgSecrets["acme"]["DEPLOY_KEY"].UpdatedAt
	if err := c.PutOrgSecret(t.Context(), "acme", "DEPLOY_KEY", []byte("the second value"), []int64{1002}); err != nil {
		t.Fatalf("PutOrgSecret() again error = %v", err)
	}
	got := srv.OrgSecrets["acme"]["DEPLOY_KEY"]
	if string(got.Value) != "the second value" {
		t.Errorf("the stand-in holds %q, want the second value", got.Value)
	}
	if diff := cmp.Diff([]int64{1002}, got.RepositoryIDs); diff != "" {
		t.Errorf("repository ids mismatch (-want +got):\n%s", diff)
	}
	if got.UpdatedAt.Before(first) {
		t.Errorf("updated at %s after the second put, before the first's %s", got.UpdatedAt, first)
	}
	if len(srv.OrgSecrets["acme"]) != 1 {
		t.Errorf("the organization holds %d secrets, want 1", len(srv.OrgSecrets["acme"]))
	}
}

// TestPutOrgSecretWire: what the put sends is the sealed value in base64, the id of the
// key it was sealed for, the visibility selected and the selection as an array (an empty
// one, never null, for none); the value itself is nowhere in it, nor in an error when
// the key is unusable or GitHub refuses.
func TestPutOrgSecretWire(t *testing.T) {
	t.Parallel()

	const value = "the-value-that-must-not-leak"
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	goodKey := base64.StdEncoding.EncodeToString(private.PublicKey().Bytes())
	tests := []struct {
		name string
		// key is the public key GitHub answers; keyStatus and putStatus the statuses of
		// the two requests (200 and 201 when 0).
		key       string
		keyStatus int
		putStatus int
		ids       []int64
		// wantIDs is the selection the put must carry, as JSON.
		wantIDs string
		wantErr string
		// wantPuts is how many puts reach GitHub.
		wantPuts int
	}{
		{name: "no repository is an empty array", key: goodKey, ids: nil, wantIDs: `[]`, wantPuts: 1},
		{name: "the repositories are an array of their ids", key: goodKey, ids: []int64{1001, 1002}, wantIDs: `[1001,1002]`, wantPuts: 1},
		{name: "a public key that is not base64 seals nothing", key: "not base64!", wantErr: "illegal base64 data"},
		{name: "a public key of another size seals nothing", key: base64.StdEncoding.EncodeToString(make([]byte, 16)), wantErr: "is 16 bytes, expected 32"},
		{name: "a public key GitHub refuses seals nothing", keyStatus: http.StatusForbidden, wantErr: "GET /orgs/acme/actions/secrets/public-key answered 403"},
		{name: "a put GitHub refuses is its refusal", key: goodKey, putStatus: http.StatusUnprocessableEntity, wantErr: "PUT /orgs/acme/actions/secrets/DEPLOY_KEY answered 422", wantPuts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var puts [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				status := tt.putStatus
				answer := map[string]string{"message": "refused"}
				if r.Method == http.MethodGet {
					status = tt.keyStatus
					if status == 0 {
						answer = map[string]string{"key_id": "k1", "key": tt.key}
					}
				} else {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("reading the put: %v", err)
					}
					mu.Lock()
					puts = append(puts, body)
					mu.Unlock()
					if status == 0 {
						status = http.StatusCreated
					}
				}
				if status != 0 {
					w.WriteHeader(status)
				}
				if err := json.NewEncoder(w).Encode(answer); err != nil {
					t.Errorf("answering: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			err := github.New(server.URL, "test-token").PutOrgSecret(t.Context(), "acme", "DEPLOY_KEY", []byte(value), tt.ids)
			mu.Lock()
			defer mu.Unlock()
			if len(puts) != tt.wantPuts {
				t.Fatalf("puts = %d, want %d", len(puts), tt.wantPuts)
			}
			for _, put := range puts {
				if bytes.Contains(put, []byte(value)) {
					t.Errorf("the put carries the value unsealed: %s", put)
				}
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("PutOrgSecret() error = %v, wantErr %q", err, tt.wantErr)
				}
				if strings.Contains(fmt.Sprintf("%v %+v", err, err), value) {
					t.Errorf("the error carries the value: %v", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("PutOrgSecret() error = %v", err)
			}
			var sent map[string]json.RawMessage
			if err := json.Unmarshal(puts[0], &sent); err != nil {
				t.Fatalf("the put is not JSON: %v", err)
			}
			sealed, _ := base64.StdEncoding.DecodeString(strings.Trim(string(sent["encrypted_value"]), `"`))
			// A sealed box is the value behind a 32-byte ephemeral public key and a 16-byte tag.
			if want := len(value) + 48; len(sealed) != want {
				t.Errorf("encrypted_value holds %d bytes, want the %d of a sealed box", len(sealed), want)
			}
			delete(sent, "encrypted_value")
			want := map[string]json.RawMessage{"key_id": json.RawMessage(`"k1"`), "visibility": json.RawMessage(`"selected"`), "selected_repository_ids": json.RawMessage(tt.wantIDs)}
			if diff := cmp.Diff(want, sent, cmp.Transformer("text", func(m json.RawMessage) string {
				return string(m)
			})); diff != "" {
				t.Errorf("the put mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOrgSecret(t *testing.T) {
	t.Parallel()

	written := time.Date(2026, time.March, 1, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name     string
		secret   string
		want     *github.OrgSecret
		notFound bool
	}{
		{name: "a secret reads back without its value", secret: "DEPLOY_KEY", want: &github.OrgSecret{Name: "DEPLOY_KEY", Visibility: github.VisibilitySelected, UpdatedAt: written}},
		{name: "a secret every repository sees", secret: "SHARED", want: &github.OrgSecret{Name: "SHARED", Visibility: github.VisibilityAll, UpdatedAt: written.Add(time.Hour)}},
		{name: "an absent secret is not found", secret: "NOWHERE", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := githubtest.New(t)
			srv.OrgSecrets["acme"] = map[string]*githubtest.OrgSecret{
				"DEPLOY_KEY": {Value: []byte("the deploy key"), Visibility: github.VisibilitySelected, RepositoryIDs: []int64{1001}, UpdatedAt: written},
				"SHARED":     {Value: []byte("the shared one"), Visibility: github.VisibilityAll, UpdatedAt: written.Add(time.Hour)},
			}
			got, err := srv.Client().OrgSecret(t.Context(), "acme", tt.secret)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("OrgSecret() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("OrgSecret() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("OrgSecret() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestOrgSecretRepositories: the repositories selected for a secret are listed in the
// selection's order, every page of them, each as the repository reads.
func TestOrgSecretRepositories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// selected is how many repositories the secret is selected for.
		selected int
		// wantPages is how many pages the listing reads.
		wantPages int
	}{
		{name: "no repository", selected: 0, wantPages: 1},
		{name: "two repositories", selected: 2, wantPages: 1},
		{name: "a full page is followed by the empty one after it", selected: 100, wantPages: 2},
		{name: "more than a page", selected: 130, wantPages: 2},
		{name: "more than two pages", selected: 201, wantPages: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := githubtest.New(t)
			ids := make([]int64, 0, tt.selected)
			var want []string
			for i := range tt.selected {
				name := fmt.Sprintf("quill-%03d", i)
				ids = append(ids, srv.AddRepo("acme", name, &githubtest.Repo{Private: true}).ID)
				want = append(want, fmt.Sprintf("acme/%s#%d private", name, ids[i]))
			}
			srv.AddRepo("acme", "not-selected", &githubtest.Repo{})
			srv.OrgSecrets["acme"] = map[string]*githubtest.OrgSecret{"DEPLOY_KEY": {Visibility: github.VisibilitySelected, RepositoryIDs: ids}}
			repos, err := srv.Client().OrgSecretRepositories(t.Context(), "acme", "DEPLOY_KEY")
			if err != nil {
				t.Fatalf("OrgSecretRepositories() error = %v", err)
			}
			var got []string
			for _, repo := range repos {
				line := fmt.Sprintf("%s#%d", repo.FullName, repo.ID)
				if repo.Private {
					line += " private"
				}
				got = append(got, line)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("OrgSecretRepositories() mismatch (-want +got):\n%s", diff)
			}
			if len(srv.Calls) != tt.wantPages {
				t.Errorf("the listing read %d pages, want %d: %v", len(srv.Calls), tt.wantPages, srv.Calls)
			}
		})
	}
}

func TestOrgSecretRepositoriesNotFound(t *testing.T) {
	t.Parallel()

	srv := githubtest.New(t)
	if _, err := srv.Client().OrgSecretRepositories(t.Context(), "acme", "NOWHERE"); !github.NotFound(err) {
		t.Errorf("OrgSecretRepositories() of an absent secret = %v, want not found", err)
	}
}

// TestSetOrgSecretRepositories: the selection is replaced whole, by an empty array when
// there is none, and the value stays.
func TestSetOrgSecretRepositories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		secret   string
		ids      []int64
		want     []int64
		notFound bool
	}{
		{name: "another selection replaces the one there was", secret: "DEPLOY_KEY", ids: []int64{1002, 1003}, want: []int64{1002, 1003}},
		{name: "no repository, given as nil, arrives as an empty list", secret: "DEPLOY_KEY", ids: nil, want: []int64{}},
		{name: "no repository, given as an empty list, arrives as one", secret: "DEPLOY_KEY", ids: []int64{}, want: []int64{}},
		{name: "an absent secret is not found", secret: "NOWHERE", ids: []int64{1002}, notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := githubtest.New(t)
			written := time.Date(2026, time.March, 1, 12, 30, 0, 0, time.UTC)
			secret := &githubtest.OrgSecret{Value: []byte("the deploy key"), Visibility: github.VisibilitySelected, RepositoryIDs: []int64{1001}, UpdatedAt: written}
			srv.OrgSecrets["acme"] = map[string]*githubtest.OrgSecret{"DEPLOY_KEY": secret}
			err := srv.Client().SetOrgSecretRepositories(t.Context(), "acme", tt.secret, tt.ids)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("SetOrgSecretRepositories() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("SetOrgSecretRepositories() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, secret.RepositoryIDs); diff != "" {
				t.Errorf("repository ids mismatch (-want +got):\n%s", diff)
			}
			if string(secret.Value) != "the deploy key" || !secret.UpdatedAt.Equal(written) {
				t.Errorf("the value changed with the selection: %q, written %s", secret.Value, secret.UpdatedAt)
			}
		})
	}
}

func TestDeleteOrgSecret(t *testing.T) {
	t.Parallel()

	srv := githubtest.New(t)
	c := srv.Client()
	if err := c.PutOrgSecret(t.Context(), "acme", "DEPLOY_KEY", []byte("the deploy key"), nil); err != nil {
		t.Fatalf("PutOrgSecret() error = %v", err)
	}
	if err := c.DeleteOrgSecret(t.Context(), "acme", "DEPLOY_KEY"); err != nil {
		t.Fatalf("DeleteOrgSecret() error = %v", err)
	}
	if _, held := srv.OrgSecrets["acme"]["DEPLOY_KEY"]; held {
		t.Error("the stand-in still holds the secret")
	}
	if _, err := c.OrgSecret(t.Context(), "acme", "DEPLOY_KEY"); !github.NotFound(err) {
		t.Errorf("OrgSecret() of a removed secret = %v, want not found", err)
	}
	if err := c.DeleteOrgSecret(t.Context(), "acme", "DEPLOY_KEY"); !github.NotFound(err) {
		t.Errorf("DeleteOrgSecret() of an absent secret = %v, want not found", err)
	}
}

// TestSecretPutRefused: the stand-in refuses, as GitHub does, a secret that is not sealed
// for its key: one naming another key, one that is not base64 and one that does not open,
// for an organization's secret and an environment's alike; and it keeps nothing of them.
func TestSecretPutRefused(t *testing.T) {
	t.Parallel()

	secrets := []struct {
		kind string
		path string
	}{
		{kind: "an organization's secret", path: "/orgs/acme/actions/secrets/DEPLOY_KEY"},
		{kind: "an environment's secret", path: "/repos/acme/quill/environments/tst/secrets/DEPLOY_KEY"},
	}
	tests := []struct {
		name string
		// body is the put's body, with %s where the id of the stand-in's key goes.
		body string
		want string
	}{
		{name: "sealed for another key", body: `{"encrypted_value":"AAAA","key_id":"another","visibility":"selected","selected_repository_ids":[]}`, want: "key_id is not the id of the public key"},
		{name: "not base64", body: `{"encrypted_value":"not base64!","key_id":"%s","visibility":"selected","selected_repository_ids":[]}`, want: "encrypted_value is not base64"},
		{name: "not a sealed box", body: `{"encrypted_value":"dGhlIGRlcGxveSBrZXkgaW4gdGhlIGNsZWFyLCBsb25nIGVub3VnaCB0byBiZSBhIGJveCBidXQgbm90IG9uZQ==","key_id":"%s","visibility":"selected","selected_repository_ids":[]}`, want: "encrypted_value does not open with the public key"},
	}
	for _, secret := range secrets {
		for _, tt := range tests {
			t.Run(secret.kind+" "+tt.name, func(t *testing.T) {
				t.Parallel()

				srv := githubtest.New(t)
				srv.AddRepo("acme", "quill", &githubtest.Repo{Environments: map[string]*githubtest.Environment{"tst": {Custom: true}}})
				var key struct {
					KeyID string `json:"key_id"`
				}
				if status := send(t, srv, http.MethodGet, strings.TrimSuffix(secret.path, "DEPLOY_KEY")+"public-key", "", &key); status != http.StatusOK || key.KeyID == "" {
					t.Fatalf("the public key answered %d with the id %q", status, key.KeyID)
				}
				body := tt.body
				if strings.Contains(body, "%s") {
					body = fmt.Sprintf(body, key.KeyID)
				}
				var refusal struct {
					Message string `json:"message"`
				}
				if status := send(t, srv, http.MethodPut, secret.path, body, &refusal); status != http.StatusUnprocessableEntity || refusal.Message != tt.want {
					t.Errorf("the put answered %d %q, want 422 %q", status, refusal.Message, tt.want)
				}
				if len(srv.OrgSecrets["acme"]) != 0 || len(srv.Repos["acme/quill"].Environments["tst"].Secrets) != 0 {
					t.Error("the stand-in kept a secret it refused")
				}
			})
		}
	}
}

// send sends one request to the stand-in as its token, with the JSON body when there is
// one, decodes the answer into out and returns the status.
func send(t *testing.T, srv *githubtest.Server, method, path, body string, out any) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s answered %d with %q, which is not JSON: %v", method, path, resp.StatusCode, data, err)
		}
	}

	return resp.StatusCode
}
