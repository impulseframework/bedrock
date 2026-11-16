// generated.go derives the versions of the secrets the tooling makes, the variables the
// code declares secret:"generated" or secret:"versioned": values nobody types. The
// placement carries the tag's word for them, and the versions an environment runs are
// read at each plan from Secret Manager, the way the image digest is read at each build.
// A generated secret runs on the newest enabled version of its container, passed to the
// plan as var.generated_secret_versions, so the revision template pins versions/N exactly
// as a committed pin does, and the record names N. A versioned secret runs on every
// enabled version, passed as var.versioned_secret_versions, so the revision receives one
// variable per version, and the record names them all. A tag build's plan step, as the
// apply identity, also seeds: a container the project lacks is created with the stack's
// labels (the apply then adopts it with no change), the identity grants itself the adding
// of versions on it (the stack declares the same grant, durably, for the apply and
// operations identities), and a version is added (32 random bytes, base64) when no
// enabled version exists. A pull request's reads seed nothing: a secret with no version
// yet is left unmounted and said.

package deploy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-playground/errors/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/impulseframework/bedrock/internal/derive"
	"github.com/impulseframework/bedrock/internal/secret"
)

const (
	// generatedFact is what the plan step leaves for the record: VARIABLE=N, comma
	// separated, sorted by variable. versionedFact is the same for the versioned secrets,
	// each with its versions, oldest first, a space between them.
	generatedFact = "GENERATED_SECRETS"
	versionedFact = "VERSIONED_SECRETS"
	// generatedVar and versionedVar are the stack variables the derived versions are
	// passed in.
	generatedVar = "generated_secret_versions"
	versionedVar = "versioned_secret_versions"
	// generatedBytes is the size of a generated value, before base64.
	generatedBytes = 32
	// versionAdderRole adds versions to a container and reads none.
	versionAdderRole = "roles/secretmanager.secretVersionAdder"
	// grantWait and grantTries bound the wait for a fresh grant to take effect: IAM
	// propagates a secret's policy in seconds, up to minutes.
	grantWait  = 10 * time.Second
	grantTries = 18
	// identityDomain is what a service account's email ends with, after its project.
	identityDomain = ".iam.gserviceaccount.com"
)

// derivedPins are the versions a plan step derived for the secrets the tooling makes:
// what the plan is told, and what the record names.
type derivedPins struct {
	// Generated is the newest enabled version of each generated secret, by variable.
	Generated map[string]string
	// Versioned are the enabled versions of each versioned secret, by variable, oldest
	// first: every one of them reaches the processes.
	Versioned map[string][]string
}

// args are the plan's variables for the derived versions, as tofu takes a map on the
// command line; none for a kind without any.
func (p *derivedPins) args() []string {
	if p == nil {
		return nil
	}
	var args []string
	if len(p.Generated) > 0 {
		entries := make([]string, 0, len(p.Generated))
		for _, name := range slices.Sorted(maps.Keys(p.Generated)) {
			entries = append(entries, name+`="`+p.Generated[name]+`"`)
		}
		args = append(args, varFlag, generatedVar+"={"+strings.Join(entries, ", ")+"}")
	}
	if len(p.Versioned) > 0 {
		entries := make([]string, 0, len(p.Versioned))
		for _, name := range slices.Sorted(maps.Keys(p.Versioned)) {
			quoted := make([]string, 0, len(p.Versioned[name]))
			for _, version := range p.Versioned[name] {
				quoted = append(quoted, `"`+version+`"`)
			}
			entries = append(entries, name+"=["+strings.Join(quoted, ", ")+"]")
		}
		args = append(args, varFlag, versionedVar+"={"+strings.Join(entries, ", ")+"}")
	}

	return args
}

// facts are what the plan step leaves for the record (generatedOf, versionedOf): a fact
// per kind that derived any.
func (p *derivedPins) facts() map[string]string {
	facts := map[string]string{}
	if p == nil {
		return facts
	}
	if len(p.Generated) > 0 {
		entries := make([]string, 0, len(p.Generated))
		for _, name := range slices.Sorted(maps.Keys(p.Generated)) {
			entries = append(entries, name+"="+p.Generated[name])
		}
		facts[generatedFact] = strings.Join(entries, ",")
	}
	if len(p.Versioned) > 0 {
		entries := make([]string, 0, len(p.Versioned))
		for _, name := range slices.Sorted(maps.Keys(p.Versioned)) {
			entries = append(entries, name+"="+strings.Join(p.Versioned[name], " "))
		}
		facts[versionedFact] = strings.Join(entries, ",")
	}

	return facts
}

// generatedPins lists, as identity, the enabled versions of the container of each secret
// the tooling makes in the environment's project, and answers the versions by variable:
// the newest for a generated secret, every one for a versioned secret. With seed, a
// container the project lacks is created and one with no enabled version gets a version
// added (seedGenerated): the tag build's plan step, as the apply identity. Without seed (a
// pull request's plans, as the plan identity of each environment or tst's apply
// identity), such a secret is left out and said. Nothing is opened when the code declares
// no such secret, and nil is answered. Secret Manager is opened as the identity when
// impersonate says the build is not that identity (the deploy identity reading as the
// apply identity, the apply identity as a plan identity), and directly when the build
// runs as it (the pull-request stack job): a service account is granted no token for
// itself.
func generatedPins(ctx context.Context, clients *Clients, identity string, impersonate bool, m *derive.Model, app, env, project string, seed bool, out io.Writer) (*derivedPins, error) {
	var generated []derive.Secret
	for _, s := range m.Secrets {
		if s.Generated {
			generated = append(generated, s)
		}
	}
	if len(generated) == 0 {
		return nil, nil
	}
	secrets, err := openSecrets(ctx, clients, identity, impersonate)
	if err != nil {
		return nil, err
	}
	defer secrets.Close()
	pins := &derivedPins{Generated: map[string]string{}, Versioned: map[string][]string{}}
	for _, s := range generated {
		id := secretContainerID(m, app, env, &s)
		container := "projects/" + project + "/secrets/" + id
		exists, err := secrets.Container(ctx, container)
		if err != nil {
			return nil, err
		}
		var versions []string
		if exists {
			if versions, err = secrets.EnabledVersions(ctx, container); err != nil {
				return nil, err
			}
		}
		switch {
		case len(versions) > 0 && s.Versioned:
			fmt.Fprintf(out, "%s: %s of %s %s enabled, and a process receives every one (a versioned secret: %s; the highest number is the current version).\n", s.Variable.Name, versionsPhrase(versions), id, isAre(versions), versionVariables(s.Variable.Name, versions))
		case len(versions) > 0:
			fmt.Fprintf(out, "%s: version %s of %s, its newest enabled, is the pin (a generated secret: its version is derived at each plan).\n", s.Variable.Name, versions[len(versions)-1], id)
		case !seed:
			fmt.Fprintf(out, "%s: %s has no enabled version yet (a %s secret, whose first version %s's next release build adds): the plan mounts nothing for it.\n", s.Variable.Name, id, s.Word(), env)

			continue
		default:
			version, err := seedGenerated(ctx, clients, secrets, identity, m, app, env, project, &s, id, exists, out)
			if err != nil {
				return nil, err
			}
			versions = []string{version}
		}
		if s.Versioned {
			pins.Versioned[s.Variable.Name] = versions

			continue
		}
		pins.Generated[s.Variable.Name] = versions[len(versions)-1]
	}

	return pins, nil
}

// secretContainerID is the name of the secret's container in the environment, as the
// stack names it: <prefix>-<env>-gbl-<app>-<kebab name>.
func secretContainerID(m *derive.Model, app, env string, s *derive.Secret) string {
	return m.Placement.Prefix + "-" + env + "-gbl-" + app + "-" + s.Name
}

// versionsPhrase spells versions for a line of the log: "version 1", "versions 1 and 2",
// "versions 1, 2 and 3".
func versionsPhrase(versions []string) string {
	switch len(versions) {
	case 0:
		return "no version"
	case 1:
		return "version " + versions[0]
	default:
		return "versions " + strings.Join(versions[:len(versions)-1], ", ") + " and " + versions[len(versions)-1]
	}
}

// isAre is the verb after versionsPhrase: "are" for several versions, "is" for one and
// for none ("no version is enabled").
func isAre(versions []string) string {
	if len(versions) > 1 {
		return "are"
	}

	return "is"
}

// versionVariables are the variables a process receives a versioned secret's versions
// under, comma-separated.
func versionVariables(variable string, versions []string) string {
	names := make([]string, 0, len(versions))
	for _, version := range versions {
		names = append(names, derive.VersionVariable(variable, version))
	}

	return strings.Join(names, ", ")
}

// seedGenerated gives a generated or versioned secret its first enabled version as the identity: the
// container, when the project lacks it, with the stack's labels (bedrock secret add's,
// which the apply adopts with no change); the grant that lets the identity add; then a
// version of generatedBytes random bytes, base64, never printed. A fresh grant may take
// a while to be in effect, so the add is tried again while Secret Manager refuses it.
func seedGenerated(ctx context.Context, clients *Clients, secrets Secrets, identity string, m *derive.Model, app, env, project string, s *derive.Secret, id string, exists bool, out io.Writer) (string, error) {
	variable := s.Variable.Name
	container := "projects/" + project + "/secrets/" + id
	if !exists {
		labels := secret.ContainerLabels(app, env, m.Placement.Repository, variable, m.Placement.Labels)
		if err := secrets.Create(ctx, project, id, labels); err != nil {
			return "", err
		}
		fmt.Fprintf(out, "%s: created %s in %s with the stack's labels, ahead of the apply, which adopts it.\n", variable, id, project)
	}
	if err := secrets.Grant(ctx, container, versionAdderRole, "serviceAccount:"+identity); err != nil {
		return "", err
	}
	value := make([]byte, generatedBytes)
	if _, err := rand.Read(value); err != nil {
		return "", errors.Wrap(err, "rand.Read()")
	}
	payload := []byte(base64.StdEncoding.EncodeToString(value))
	var version string
	for try := 1; ; try++ {
		var err error
		if version, err = secrets.Add(ctx, container, payload); err == nil {
			break
		}
		if status.Code(errors.Cause(err)) != codes.PermissionDenied || try >= grantTries {
			return "", err
		}
		fmt.Fprintf(out, "Waiting for the grant on %s to take effect: Secret Manager refused the add (%ds).\n", id, int((time.Duration(try) * grantWait).Seconds()))
		if err := clients.sleep()(ctx, grantWait); err != nil {
			return "", err
		}
	}
	what := "the pin"
	if s.Versioned {
		what = "its first version, which a process receives as " + derive.VersionVariable(variable, version)
	}
	fmt.Fprintf(out, "%s: added version %s of %s as %s (%d random bytes, base64, never printed): %s; a rotation (bedrock secret rotate %s %s) adds the next.\n", variable, version, id, identity, generatedBytes, what, env, variable)

	return version, nil
}

// generatedArgs are the plan's variables for the derived versions (derivedPins.args);
// none without any.
func generatedArgs(pins *derivedPins) []string {
	return pins.args()
}

// generatedOf is the record's map of the generated secrets' derived versions, nil when
// the run derived none.
func generatedOf(env map[string]string) map[string]string {
	if env[generatedFact] == "" {
		return nil
	}
	pins := map[string]string{}
	for _, pair := range strings.Split(env[generatedFact], ",") {
		name, version, ok := strings.Cut(pair, "=")
		if ok {
			pins[name] = version
		}
	}

	return pins
}

// versionedOf is the record's map of the versioned secrets' enabled versions, oldest
// first, nil when the run listed none.
func versionedOf(env map[string]string) map[string][]string {
	if env[versionedFact] == "" {
		return nil
	}
	pins := map[string][]string{}
	for _, pair := range strings.Split(env[versionedFact], ",") {
		name, versions, ok := strings.Cut(pair, "=")
		if ok {
			pins[name] = strings.Fields(versions)
		}
	}

	return pins
}

// identityProject is the project a service account's email belongs to:
// <name>@<project>.iam.gserviceaccount.com.
func identityProject(email string) string {
	_, domain, _ := strings.Cut(email, "@")

	return strings.TrimSuffix(domain, identityDomain)
}

// openSecrets opens Secret Manager as the identity, or directly when the build runs as it.
func openSecrets(ctx context.Context, clients *Clients, identity string, impersonate bool) (Secrets, error) {
	if impersonate {
		return clients.SecretsAs(ctx, identity)
	}

	return clients.Secrets(ctx)
}
