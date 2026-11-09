// orgsecrets.go is the GitHub organization's Actions secrets: values GitHub keeps for the
// workflows of the repositories a secret is shared with, and never gives back. A value is
// sent sealed with the organization's public key, so only GitHub opens it; what can be
// read of a secret is its name, who sees it and when it was last written. The sealing is
// here for an environment's secrets too (environments.go).

package github

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/go-playground/errors/v5"
	"golang.org/x/crypto/nacl/box"
)

// VisibilitySelected is a secret's visibility when only the repositories selected for it
// see it.
const VisibilitySelected = "selected"

// publicKeySize is the size of the Curve25519 public key GitHub seals a secret with.
const publicKeySize = 32

// OrgSecret is an organization's Actions secret as the API describes it: its name, its
// visibility (all, private or selected) and when its value was last written. The value
// itself is never read back.
type OrgSecret struct {
	Name       string    `json:"name"`
	Visibility string    `json:"visibility"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// OrgSecret reads the organization's secret, without its value; an absent one is
// NotFound.
func (c *Client) OrgSecret(ctx context.Context, org, name string) (*OrgSecret, error) {
	out := &OrgSecret{}
	if err := c.do(ctx, http.MethodGet, "/orgs/"+org+"/actions/secrets/"+name, nil, out); err != nil {
		return nil, err
	}

	return out, nil
}

// PutOrgSecret makes the organization's secret, or replaces its value and its selection,
// visible to the repositories with the ids alone (none sees it when there are none). It
// reads the organization's public key and sends the value sealed with it (a libsodium
// sealed box, the only form GitHub takes), so the value crosses the wire readable by
// GitHub alone. The value is in no error this returns.
func (c *Client) PutOrgSecret(ctx context.Context, org, name string, value []byte, repositoryIDs []int64) error {
	secrets := "/orgs/" + org + "/actions/secrets/"
	sealed, err := c.seal(ctx, secrets+"public-key", value)
	if err != nil {
		return err
	}
	in := struct {
		sealedSecret
		Visibility    string  `json:"visibility"`
		RepositoryIDs []int64 `json:"selected_repository_ids"`
	}{sealedSecret: *sealed, Visibility: VisibilitySelected, RepositoryIDs: selection(repositoryIDs)}

	return c.do(ctx, http.MethodPut, secrets+name, in, nil)
}

// sealedSecret is a secret's value as GitHub takes it: sealed with a public key of
// GitHub's, in base64, beside the id of that key.
type sealedSecret struct {
	EncryptedValue string `json:"encrypted_value"`
	KeyID          string `json:"key_id"`
}

// seal reads the public key at keyPath (an organization's for its secrets, or an
// environment's) and seals the value with it: a libsodium sealed box, which only the
// holder of the private key, GitHub, opens. The value is in no error it returns.
func (c *Client) seal(ctx context.Context, keyPath string, value []byte) (*sealedSecret, error) {
	var key struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := c.do(ctx, http.MethodGet, keyPath, nil, &key); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(key.Key)
	if err != nil {
		return nil, errors.Wrap(err, "base64.StdEncoding.DecodeString()")
	}
	if len(raw) != publicKeySize {
		return nil, errors.Newf("the public key GitHub gave at %s is %d bytes, expected %d", keyPath, len(raw), publicKeySize)
	}
	var public [publicKeySize]byte
	copy(public[:], raw)
	sealed, err := box.SealAnonymous(nil, value, &public, rand.Reader)
	if err != nil {
		return nil, errors.Wrap(err, "box.SealAnonymous()")
	}

	return &sealedSecret{EncryptedValue: base64.StdEncoding.EncodeToString(sealed), KeyID: key.KeyID}, nil
}

// OrgSecretRepositories lists the repositories selected for the organization's secret,
// every page of them.
func (c *Client) OrgSecretRepositories(ctx context.Context, org, name string) ([]Repository, error) {
	var all []Repository
	for page := 1; ; page++ {
		var answer struct {
			Repositories []Repository `json:"repositories"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s/actions/secrets/%s/repositories?per_page=%d&page=%d", org, name, perPage, page), nil, &answer); err != nil {
			return nil, err
		}
		all = append(all, answer.Repositories...)
		if len(answer.Repositories) < perPage {
			return all, nil
		}
	}
}

// SetOrgSecretRepositories replaces the selection of the organization's secret with the
// repositories with the ids: the ones left out stop seeing it, and none sees it when
// there are none. The value stays as it is.
func (c *Client) SetOrgSecretRepositories(ctx context.Context, org, name string, repositoryIDs []int64) error {
	in := struct {
		RepositoryIDs []int64 `json:"selected_repository_ids"`
	}{RepositoryIDs: selection(repositoryIDs)}

	return c.do(ctx, http.MethodPut, "/orgs/"+org+"/actions/secrets/"+name+"/repositories", in, nil)
}

// DeleteOrgSecret removes the organization's secret.
func (c *Client) DeleteOrgSecret(ctx context.Context, org, name string) error {
	return c.do(ctx, http.MethodDelete, "/orgs/"+org+"/actions/secrets/"+name, nil, nil)
}

// selection is the ids as the API takes a selection: an empty array, never null, when
// there are none.
func selection(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}

	return ids
}
