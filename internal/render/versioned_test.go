package render

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// stackFunctions are the functions the rendered secret locals call, as tofu has them:
// go-cty's own where it has one, and toset, tostring and alltrue written here.
func stackFunctions() map[string]function.Function {
	return map[string]function.Function{
		"merge":      stdlib.MergeFunc,
		"contains":   stdlib.ContainsFunc,
		"keys":       stdlib.KeysFunc,
		"lookup":     stdlib.LookupFunc,
		"join":       stdlib.JoinFunc,
		"jsonencode": stdlib.JSONEncodeFunc,
		"setproduct": stdlib.SetProductFunc,
		"setunion":   stdlib.SetUnionFunc,
		"toset": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "v", Type: cty.DynamicPseudoType}},
			Type:   function.StaticReturnType(cty.Set(cty.String)),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				if args[0].LengthInt() == 0 {
					return cty.SetValEmpty(cty.String), nil
				}

				return cty.SetVal(args[0].AsValueSlice()), nil
			},
		}),
		"tostring": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "v", Type: cty.DynamicPseudoType}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				return convert.Convert(args[0], cty.String)
			},
		}),
		"alltrue": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "list", Type: cty.DynamicPseudoType}},
			Type:   function.StaticReturnType(cty.Bool),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				for it := args[0].ElementIterator(); it.Next(); {
					if _, v := it.Element(); v.False() {
						return cty.False, nil
					}
				}

				return cty.True, nil
			},
		}),
	}
}

// secretLocals evaluates the rendered secret locals of a golden stack for the tst
// environment, in the order they depend on one another, over the variables a plan is
// given: the placement's secret_versions for the environment, the generated secrets'
// versions and the versioned secrets' lists. secrets replaces the golden's own secrets
// local when it is not nil. What it answers is the stack's local object with every one of
// them evaluated.
func secretLocals(t *testing.T, golden string, declared, generated map[string]string, versioned map[string][]string, secrets *cty.Value) map[string]cty.Value {
	t.Helper()

	const environment = "tst"
	locals := localsOf(t, parseBody(t, filepath.Join("testdata", golden, "locals.tf")))
	local := map[string]cty.Value{
		"name":       cty.StringVal("imp-" + environment),
		"app":        cty.StringVal(golden),
		"project_id": cty.StringVal("p-" + environment),
		"is_pr":      cty.False,
	}
	vars := map[string]cty.Value{
		"environment":               cty.StringVal(environment),
		"secret_versions":           cty.ObjectVal(map[string]cty.Value{environment: stringMap(declared)}),
		"generated_secret_versions": stringMap(generated),
		"versioned_secret_versions": listMap(versioned),
	}
	names := []string{"secrets", "secret_ids", "generated_secrets", "versioned_secrets", "secret_words", "migrate_secrets", "declared_versions", "secret_versions", "versioned_versions", "mounted_secrets", "secret_mounts", "jobs_secrets", "jobs_secret_mounts"}
	for _, name := range names {
		attr, ok := locals[name]
		if !ok {
			if strings.HasPrefix(name, "jobs_") {
				continue
			}
			t.Fatalf("%s: locals.tf declares no %s", golden, name)
		}
		if name == "secrets" && secrets != nil {
			local[name] = *secrets

			continue
		}
		ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(local), "var": cty.ObjectVal(vars)}, Functions: stackFunctions()}
		value, diags := attr.Expr.Value(ctx)
		if diags.HasErrors() {
			t.Fatalf("%s: %s: %s", golden, name, diags.Error())
		}
		local[name] = value
	}

	return local
}

// stringMap is a map of strings as tofu passes one, the empty object for none.
func stringMap(m map[string]string) cty.Value {
	if len(m) == 0 {
		return cty.EmptyObjectVal
	}
	values := make(map[string]cty.Value, len(m))
	for k, v := range m {
		values[k] = cty.StringVal(v)
	}

	return cty.ObjectVal(values)
}

// listMap is a map of lists of strings as tofu passes one, the empty object for none.
func listMap(m map[string][]string) cty.Value {
	if len(m) == 0 {
		return cty.EmptyObjectVal
	}
	values := make(map[string]cty.Value, len(m))
	for k, list := range m {
		items := make([]cty.Value, 0, len(list))
		for _, v := range list {
			items = append(items, cty.StringVal(v))
		}
		values[k] = cty.TupleVal(items)
	}

	return cty.ObjectVal(values)
}

// objectItem is the expression an object constructor gives the key.
func objectItem(t *testing.T, expr hclsyntax.Expression, key string) hclsyntax.Expression {
	t.Helper()

	obj, ok := expr.(*hclsyntax.ObjectConsExpr)
	if !ok {
		t.Fatalf("%s: not an object constructor", key)
	}
	for _, item := range obj.Items {
		if hcl.ExprAsKeyword(item.KeyExpr) == key {
			return item.ValueExpr
		}
	}
	t.Fatalf("no %s in the object", key)

	return nil
}

// outputValue is the value expression of the named output of a rendered outputs.tf.
func outputValue(t *testing.T, golden, name string) hclsyntax.Expression {
	t.Helper()

	for _, b := range parseBody(t, filepath.Join("testdata", golden, "outputs.tf")).Blocks {
		if b.Type == "output" && len(b.Labels) == 1 && b.Labels[0] == name {
			return b.Body.Attributes["value"].Expr
		}
	}
	t.Fatalf("%s: outputs.tf declares no output %s", golden, name)

	return nil
}

// mountLines spells evaluated secret mounts, variable=container/version, sorted.
func mountLines(mounts cty.Value) []string {
	lines := []string{}
	for name, mount := range mounts.AsValueMap() {
		ref := mount.AsValueMap()
		lines = append(lines, name+"="+ref["secret"].AsString()+"/"+ref["version"].AsString())
	}
	slices.Sort(lines)

	return lines
}

// harborVersionedSecrets is a secrets local for the harbor golden, whose code declares no
// versioned secret: a versioned application secret at the data level, which harbor's job
// process constructs, one at the site level, which it does not, and a generated one. The
// golden's migrate command reads none of the three.
func harborVersionedSecrets() cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"APP_SECRET_KEY": cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("secret-key"), "level": cty.StringVal("data"), "generated": cty.True, "versioned": cty.True}),
		"APP_SITE_KEY":   cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("site-key"), "level": cty.StringVal("site"), "generated": cty.True, "versioned": cty.True}),
		"APP_COOKIE_KEY": cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("cookie-key"), "level": cty.StringVal("data"), "generated": cty.True, "versioned": cty.False}),
	})
}

// TestVersionedSecretMounts evaluates what the rendered stack hands a process from Secret
// Manager, as tofu evaluates it at a plan: the revision_templates output's secret mounts
// of the service (and of the job process where it reads the level), and the migrate
// command's (_MIGRATE_SECRETS). A versioned secret reaches each of them once per enabled
// version the plan step listed, under the variable's name, an underscore and the
// version's number, and never under the bare name; a generated secret keeps its one
// variable; a list passed for a secret that is not versioned mounts nothing.
func TestVersionedSecretMounts(t *testing.T) {
	t.Parallel()

	beaconDeclared := map[string]string{"APP_COOKIE_KEY": "generated", "APP_SECRET_KEY": "versioned", "APP_KEEPERS_ADMINISTRATOR_PASSWORD": "generated", "APP_MAIL_APP_PASSWORD": "4"}
	beaconGenerated := map[string]string{"APP_COOKIE_KEY": "3", "APP_KEEPERS_ADMINISTRATOR_PASSWORD": "1"}
	harborSecrets := harborVersionedSecrets()
	tests := []struct {
		name      string
		golden    string
		declared  map[string]string
		generated map[string]string
		versioned map[string][]string
		secrets   *cty.Value
		// wantService, wantJob and wantMigrate are the mounts, variable=container/version;
		// wantJob is not read for a stack whose job process mounts none.
		wantService []string
		wantJob     []string
		wantMigrate []string
	}{
		{
			name:      "two enabled versions: one variable each, beside the generated secrets' one",
			golden:    "beacon",
			declared:  beaconDeclared,
			generated: beaconGenerated,
			versioned: map[string][]string{"APP_SECRET_KEY": {"1", "2"}},
			wantService: []string{
				"APP_COOKIE_KEY=imp-tst-gbl-beacon-cookie-key/3",
				"APP_KEEPERS_ADMINISTRATOR_PASSWORD=imp-tst-gbl-beacon-keepers-administrator-password/1",
				"APP_MAIL_APP_PASSWORD=imp-tst-gbl-beacon-mail-app-password/4",
				"APP_SECRET_KEY_1=imp-tst-gbl-beacon-secret-key/1",
				"APP_SECRET_KEY_2=imp-tst-gbl-beacon-secret-key/2",
			},
			wantMigrate: []string{
				"APP_KEEPERS_ADMINISTRATOR_PASSWORD=projects/p-tst/secrets/imp-tst-gbl-beacon-keepers-administrator-password/versions/1",
				"APP_MAIL_APP_PASSWORD=projects/p-tst/secrets/imp-tst-gbl-beacon-mail-app-password/versions/4",
				"APP_SECRET_KEY_1=projects/p-tst/secrets/imp-tst-gbl-beacon-secret-key/versions/1",
				"APP_SECRET_KEY_2=projects/p-tst/secrets/imp-tst-gbl-beacon-secret-key/versions/2",
			},
		},
		{
			name:      "a version retired in between leaves a gap, and the numbers are Secret Manager's",
			golden:    "beacon",
			declared:  beaconDeclared,
			generated: beaconGenerated,
			versioned: map[string][]string{"APP_SECRET_KEY": {"2", "5", "11"}},
			wantService: []string{
				"APP_COOKIE_KEY=imp-tst-gbl-beacon-cookie-key/3",
				"APP_KEEPERS_ADMINISTRATOR_PASSWORD=imp-tst-gbl-beacon-keepers-administrator-password/1",
				"APP_MAIL_APP_PASSWORD=imp-tst-gbl-beacon-mail-app-password/4",
				"APP_SECRET_KEY_11=imp-tst-gbl-beacon-secret-key/11",
				"APP_SECRET_KEY_2=imp-tst-gbl-beacon-secret-key/2",
				"APP_SECRET_KEY_5=imp-tst-gbl-beacon-secret-key/5",
			},
			wantMigrate: []string{
				"APP_KEEPERS_ADMINISTRATOR_PASSWORD=projects/p-tst/secrets/imp-tst-gbl-beacon-keepers-administrator-password/versions/1",
				"APP_MAIL_APP_PASSWORD=projects/p-tst/secrets/imp-tst-gbl-beacon-mail-app-password/versions/4",
				"APP_SECRET_KEY_11=projects/p-tst/secrets/imp-tst-gbl-beacon-secret-key/versions/11",
				"APP_SECRET_KEY_2=projects/p-tst/secrets/imp-tst-gbl-beacon-secret-key/versions/2",
				"APP_SECRET_KEY_5=projects/p-tst/secrets/imp-tst-gbl-beacon-secret-key/versions/5",
			},
		},
		{
			name:      "a plan given no list (run by hand) mounts no versioned secret",
			golden:    "beacon",
			declared:  beaconDeclared,
			generated: beaconGenerated,
			wantService: []string{
				"APP_COOKIE_KEY=imp-tst-gbl-beacon-cookie-key/3",
				"APP_KEEPERS_ADMINISTRATOR_PASSWORD=imp-tst-gbl-beacon-keepers-administrator-password/1",
				"APP_MAIL_APP_PASSWORD=imp-tst-gbl-beacon-mail-app-password/4",
			},
			wantMigrate: []string{
				"APP_KEEPERS_ADMINISTRATOR_PASSWORD=projects/p-tst/secrets/imp-tst-gbl-beacon-keepers-administrator-password/versions/1",
				"APP_MAIL_APP_PASSWORD=projects/p-tst/secrets/imp-tst-gbl-beacon-mail-app-password/versions/4",
			},
		},
		{
			name:      "a list for a secret that is not versioned mounts nothing, and a number for a versioned one pins nothing",
			golden:    "beacon",
			declared:  beaconDeclared,
			generated: map[string]string{"APP_COOKIE_KEY": "3", "APP_SECRET_KEY": "9"},
			versioned: map[string][]string{"APP_COOKIE_KEY": {"1", "2"}},
			wantService: []string{
				"APP_COOKIE_KEY=imp-tst-gbl-beacon-cookie-key/3",
				"APP_MAIL_APP_PASSWORD=imp-tst-gbl-beacon-mail-app-password/4",
			},
			wantMigrate: []string{
				"APP_MAIL_APP_PASSWORD=projects/p-tst/secrets/imp-tst-gbl-beacon-mail-app-password/versions/4",
			},
		},
		{
			name:      "the job process receives the versions of the versioned secrets at the levels it constructs",
			golden:    "harbor",
			declared:  map[string]string{"APP_SECRET_KEY": "versioned", "APP_SITE_KEY": "versioned", "APP_COOKIE_KEY": "generated"},
			generated: map[string]string{"APP_COOKIE_KEY": "7"},
			versioned: map[string][]string{"APP_SECRET_KEY": {"1", "2"}, "APP_SITE_KEY": {"4"}},
			secrets:   &harborSecrets,
			wantService: []string{
				"APP_COOKIE_KEY=imp-tst-gbl-harbor-cookie-key/7",
				"APP_SECRET_KEY_1=imp-tst-gbl-harbor-secret-key/1",
				"APP_SECRET_KEY_2=imp-tst-gbl-harbor-secret-key/2",
				"APP_SITE_KEY_4=imp-tst-gbl-harbor-site-key/4",
			},
			wantJob: []string{
				"APP_COOKIE_KEY=imp-tst-gbl-harbor-cookie-key/7",
				"APP_SECRET_KEY_1=imp-tst-gbl-harbor-secret-key/1",
				"APP_SECRET_KEY_2=imp-tst-gbl-harbor-secret-key/2",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			local := secretLocals(t, tt.golden, tt.declared, tt.generated, tt.versioned, tt.secrets)
			ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(local)}, Functions: stackFunctions()}
			templates := outputValue(t, tt.golden, "revision_templates")
			services, ok := objectItem(t, templates, "services").(*hclsyntax.ForExpr)
			if !ok {
				t.Fatal("revision_templates.services is not a for expression")
			}
			service, diags := objectItem(t, services.ValExpr, "secrets").Value(ctx)
			if diags.HasErrors() {
				t.Fatalf("the service's secrets: %s", diags.Error())
			}
			if diff := cmp.Diff(tt.wantService, mountLines(service)); diff != "" {
				t.Errorf("the service's secret mounts (-want +got):\n%s", diff)
			}
			if tt.wantJob != nil {
				job, diags := objectItem(t, objectItem(t, templates, "job"), "secrets").Value(ctx)
				if diags.HasErrors() {
					t.Fatalf("the job's secrets: %s", diags.Error())
				}
				if diff := cmp.Diff(tt.wantJob, mountLines(job)); diff != "" {
					t.Errorf("the job process's secret mounts (-want +got):\n%s", diff)
				}
			}
			if tt.wantMigrate == nil {
				return
			}
			substitutions := localsOf(t, parseBody(t, filepath.Join("testdata", tt.golden, "cloud-build.tf")))["substitutions"]
			encoded, diags := objectItem(t, substitutions.Expr, "_MIGRATE_SECRETS").Value(ctx)
			if diags.HasErrors() {
				t.Fatalf("_MIGRATE_SECRETS: %s", diags.Error())
			}
			var migrate map[string]string
			if err := json.Unmarshal([]byte(encoded.AsString()), &migrate); err != nil {
				t.Fatalf("_MIGRATE_SECRETS is not a JSON object: %v", err)
			}
			got := []string{}
			for _, name := range slices.Sorted(maps.Keys(migrate)) {
				got = append(got, name+"="+migrate[name])
			}
			if diff := cmp.Diff(tt.wantMigrate, got); diff != "" {
				t.Errorf("the migrate command's secrets (-want +got):\n%s", diff)
			}
		})
	}
}

// TestVersionedSecretGrants evaluates who the rendered stack lets change a versioned
// secret's versions (secret-manager.tf): the apply and operations identities add to its
// container as to a generated secret's, and the operations identity alone holds 1-org's
// secretVersionDisabler, on the versioned containers and on no generated or pinned one;
// a pull-request stack grants nothing; no grant on a secret here destroys a version.
func TestVersionedSecretGrants(t *testing.T) {
	t.Parallel()

	const disabler = "projects/p-tst/roles/secretVersionDisabler"
	tests := []struct {
		name          string
		pullRequest   bool
		wantAdders    []string
		wantDisablers []string
	}{
		{
			name: "an environment's stack",
			wantAdders: []string{
				"APP_COOKIE_KEY/apply", "APP_COOKIE_KEY/operations",
				"APP_KEEPERS_ADMINISTRATOR_PASSWORD/apply", "APP_KEEPERS_ADMINISTRATOR_PASSWORD/operations",
				"APP_SECRET_KEY/apply", "APP_SECRET_KEY/operations",
			},
			wantDisablers: []string{"APP_SECRET_KEY: imp-tst-gbl-beacon-secret-key " + disabler + " serviceAccount:ops"},
		},
		{name: "a pull-request stack", pullRequest: true, wantAdders: []string{}, wantDisablers: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			local := secretLocals(t, "beacon", nil, nil, nil, nil)
			local["is_pr"] = cty.BoolVal(tt.pullRequest)
			local["identities"] = cty.ObjectVal(map[string]cty.Value{"apply_identity_member": cty.StringVal("serviceAccount:apply"), "operations_identity_member": cty.StringVal("serviceAccount:ops")})
			local["org"] = cty.ObjectVal(map[string]cty.Value{"secret_version_disabler_role": cty.ObjectVal(map[string]cty.Value{"tst": cty.StringVal(disabler)})})
			vars := cty.ObjectVal(map[string]cty.Value{"environment": cty.StringVal("tst")})
			manager := parseBody(t, filepath.Join("testdata", "beacon", "secret-manager.tf"))
			ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(local), "var": vars}, Functions: stackFunctions()}
			adders, diags := localsOf(t, manager)["generated_adders"].Expr.Value(ctx)
			if diags.HasErrors() {
				t.Fatalf("generated_adders: %s", diags.Error())
			}
			local["generated_adders"] = adders
			ctx = &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(local), "var": vars}, Functions: stackFunctions()}

			adder := resourceBody(t, manager, "google_secret_manager_secret_iam_member.generated_adder")
			if got := slices.Sorted(maps.Keys(evalAttribute(t, ctx, adder, "for_each").AsValueMap())); !slices.Equal(got, tt.wantAdders) {
				t.Errorf("the identities that add versions = %v, want %v", got, tt.wantAdders)
			}

			grant := resourceBody(t, manager, "google_secret_manager_secret_iam_member.versioned_disabler")
			got := []string{}
			for key, s := range evalAttribute(t, ctx, grant, "for_each").AsValueMap() {
				each := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(local), "var": vars, "each": cty.ObjectVal(map[string]cty.Value{"key": cty.StringVal(key), "value": s})}, Functions: stackFunctions()}
				got = append(got, key+": "+evalAttribute(t, each, grant, "secret_id").AsString()+" "+evalAttribute(t, each, grant, "role").AsString()+" "+evalAttribute(t, each, grant, "member").AsString())
			}
			slices.Sort(got)
			if diff := cmp.Diff(tt.wantDisablers, got); diff != "" {
				t.Errorf("the identities that disable a version (-want +got):\n%s", diff)
			}
			for _, b := range manager.Blocks {
				if b.Type != "resource" || b.Labels[0] != "google_secret_manager_secret_iam_member" {
					continue
				}
				role, diags := b.Body.Attributes["role"].Expr.Value(nil)
				if !diags.HasErrors() && (strings.Contains(role.AsString(), "secretVersionManager") || strings.Contains(role.AsString(), "secretmanager.admin")) {
					t.Errorf("%s grants %s, which destroys versions", b.Labels[1], role.AsString())
				}
			}
		})
	}
}

// TestMigrateAccessor evaluates which containers the rendered stack lets the deploy
// identity read (secret-manager.tf, migrate_accessor): the migrate command's own secrets,
// and every versioned secret's container whether or not the command constructs its level,
// because a retirement hands the command every enabled version of the secret it rewrites;
// never a generated secret the command does not read; nothing on a pull-request stack.
func TestMigrateAccessor(t *testing.T) {
	t.Parallel()

	harborSecrets := harborVersionedSecrets()
	tests := []struct {
		name        string
		golden      string
		secrets     *cty.Value
		pullRequest bool
		want        []string
	}{
		{name: "the command's secrets, the versioned one among them", golden: "beacon", want: []string{"APP_KEEPERS_ADMINISTRATOR_PASSWORD", "APP_MAIL_APP_PASSWORD", "APP_SECRET_KEY"}},
		{name: "versioned secrets the command does not read", golden: "harbor", secrets: &harborSecrets, want: []string{"APP_MAIL_APP_PASSWORD", "APP_SECRET_KEY", "APP_SITE_KEY"}},
		{name: "no versioned secret", golden: "harbor", want: []string{"APP_MAIL_APP_PASSWORD"}},
		{name: "a pull-request stack", golden: "harbor", secrets: &harborSecrets, pullRequest: true, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			local := secretLocals(t, tt.golden, nil, nil, nil, tt.secrets)
			local["is_pr"] = cty.BoolVal(tt.pullRequest)
			ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(local)}, Functions: stackFunctions()}
			grant := resourceBody(t, parseBody(t, filepath.Join("testdata", tt.golden, "secret-manager.tf")), "google_secret_manager_secret_iam_member.migrate_accessor")
			got := []string{}
			for it := evalAttribute(t, ctx, grant, "for_each").ElementIterator(); it.Next(); {
				_, v := it.Element()
				got = append(got, v.AsString())
			}
			slices.Sort(got)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("the containers the deploy identity reads (-want +got):\n%s", diff)
			}
			if role := evalAttribute(t, ctx, grant, "role").AsString(); role != "roles/secretmanager.secretAccessor" {
				t.Errorf("role = %s, want roles/secretmanager.secretAccessor", role)
			}
		})
	}
}

// TestSecretWordPreconditions evaluates the two preconditions the rendered service
// declares on secret_versions (cloud-run.tf), as tofu evaluates them at a plan: the entry
// of a secret the tooling makes is its own tag's word or absent, and neither word stands
// on a secret someone puts in place.
func TestSecretWordPreconditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		declared map[string]string
		// wantOwn is the first precondition's verdict (each tool-made secret carries its own
		// word); wantOthers the second's (no other secret carries either word).
		wantOwn    bool
		wantOthers bool
	}{
		{name: "each its own word", declared: map[string]string{"APP_COOKIE_KEY": "generated", "APP_SECRET_KEY": "versioned", "APP_MAIL_APP_PASSWORD": "2"}, wantOwn: true, wantOthers: true},
		{name: "no entry at all", wantOwn: true, wantOthers: true},
		{name: "a versioned secret carrying the word generated", declared: map[string]string{"APP_SECRET_KEY": "generated"}, wantOthers: true},
		{name: "a generated secret carrying the word versioned", declared: map[string]string{"APP_COOKIE_KEY": "versioned"}, wantOthers: true},
		{name: "a versioned secret pinned to a number", declared: map[string]string{"APP_SECRET_KEY": "3"}, wantOthers: true},
		{name: "the word versioned on a secret someone puts in place", declared: map[string]string{"APP_MAIL_APP_PASSWORD": "versioned"}, wantOwn: true},
		{name: "the word generated on a secret someone puts in place", declared: map[string]string{"APP_MAIL_APP_PASSWORD": "generated"}, wantOwn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			local := secretLocals(t, "beacon", tt.declared, nil, nil, nil)
			ctx := &hcl.EvalContext{Variables: map[string]cty.Value{"local": cty.ObjectVal(local), "var": cty.ObjectVal(map[string]cty.Value{"environment": cty.StringVal("tst")})}, Functions: stackFunctions()}
			service := resourceBody(t, parseBody(t, filepath.Join("testdata", "beacon", "cloud-run.tf")), "google_cloud_run_v2_service.app")
			var verdicts []bool
			for _, b := range nestedBlock(t, service, "lifecycle").Blocks {
				if b.Type != "precondition" {
					continue
				}
				message := b.Body.Attributes["error_message"].Expr
				if !slices.Contains(localsReferenced(b.Body.Attributes["condition"].Expr), "secret_words") {
					continue
				}
				verdict := evalAttribute(t, ctx, b.Body, "condition")
				if _, diags := message.Value(ctx); diags.HasErrors() {
					t.Errorf("error_message: %s", diags.Error())
				}
				verdicts = append(verdicts, verdict.True())
			}
			if want := []bool{tt.wantOwn, tt.wantOthers}; !slices.Equal(verdicts, want) {
				t.Errorf("the preconditions hold = %v, want %v (own word, no other secret)", verdicts, want)
			}
		})
	}
}

// operationsWorkflow renders the fixture and answers its operations workflow.
func operationsWorkflow(t *testing.T, fixture, placement string) string {
	t.Helper()

	files, err := Render(deriveFixture(t, fixture, placement))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, f := range files {
		if f.Root && strings.HasSuffix(f.Path, OperationsWorkflow) {
			return string(f.Content)
		}
	}
	t.Fatalf("the render of %s holds no %s", fixture, OperationsWorkflow)

	return ""
}

// jobOf is one job of the rendered workflow: from its key at the jobs' indentation to
// the next job's.
func jobOf(t *testing.T, workflow, job string) string {
	t.Helper()

	_, rest, ok := strings.Cut(workflow, "\n  "+job+":\n")
	if !ok {
		t.Fatalf("the workflow has no %s job", job)
	}
	for i, line := range strings.SplitAfter(rest, "\n") {
		if i > 0 && len(line) > 3 && strings.HasPrefix(line, "  ") && line[2] != ' ' && line[2] != '#' {
			before, _, _ := strings.Cut(rest, line)

			return before
		}
	}

	return rest
}

// TestRetireWorkflow reads the rendered operations workflow's retire action: the job runs
// on the action alone and no other job runs with it; it takes the versioned secrets the
// code declares, by container; it runs the live release's build with the instruction and
// who asked, and disables the version only in a step after that build's step, which fails
// unless the build succeeded and its deployment record says the version was retired; it
// never destroys a version. The rotate action takes a
// versioned secret as it takes a generated one, and says of a build that failed that the
// version stays enabled and the next release takes it up.
func TestRetireWorkflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		fixture       string
		placement     string
		wantVersioned string
		wantRotatable string
	}{
		{name: "beacon, with a versioned application secret", fixture: "beacon", placement: "placement-beacon.json", wantVersioned: "APP_SECRET_KEY=secret-key", wantRotatable: "APP_COOKIE_KEY=cookie-key APP_SECRET_KEY=secret-key APP_KEEPERS_ADMINISTRATOR_PASSWORD=keepers-administrator-password"},
		{name: "harbor, with none", fixture: "harbor", placement: "placement.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workflow := operationsWorkflow(t, tt.fixture, tt.placement)
			if !strings.Contains(workflow, "          - rotate\n          - retire\n") {
				t.Errorf("the action input does not offer retire after rotate")
			}
			if strings.Contains(workflow, "versions destroy") {
				t.Errorf("the workflow destroys a secret version")
			}
			retire := jobOf(t, workflow, "retire")
			containsAll(t, retire,
				"    if: ${{ inputs.action == 'retire' }}\n",
				"    environment: ${{ inputs.environment }}\n",
				"      VERSIONED: \""+tt.wantVersioned+"\"\n",
				"      VERSION: ${{ inputs.version }}\n",
				`--substitutions "_REQUESTER=$REQUESTER,_RETIRE_SECRET=$VARIABLE,_RETIRE_VERSION=$VERSION"`,
				`gcloud secrets versions disable "$VERSION" --secret "$CONTAINER" --project "$PROJECT" --quiet`,
				"Version $VERSION of $VARIABLE stays enabled, nothing was disabled",
			)
			build := strings.Index(retire, "gcloud builds triggers run")
			disableStep := strings.Index(retire, "      - name: Disable the version\n")
			disable := strings.Index(retire, "gcloud secrets versions disable")
			if build < 0 || disableStep < build || disable < disableStep {
				t.Errorf("the version is not disabled in a step of its own after the build's (build at %d, the step at %d, the command at %d)", build, disableStep, disable)
			}
			// The build's step ends the job on anything but success, and on a success whose
			// deployment record does not say the version was retired, so the step after it
			// never runs on a build that failed or that retired nothing.
			buildStep := retire[build:disableStep]
			containsAll(t, buildStep,
				"SUCCESS) break ;;", "              FAILURE|", "|TIMEOUT|INTERNAL_ERROR|EXPIRED) echo \"::error::", "exit 1 ;;",
				`select(.build == $build and .status == "live" and .retiredSecret != null)`,
				`if [ "$retired" != "$VARIABLE/$VERSION" ]; then`,
				"its deployment record does not say that version $VERSION of $VARIABLE was retired",
			)
			if confirm, refuse := strings.Index(buildStep, `if [ "$retired" != "$VARIABLE/$VERSION" ]; then`), strings.LastIndex(buildStep, "exit 1"); confirm < 0 || refuse < confirm {
				t.Errorf("a build whose record does not say the version was retired does not end the job before the version is disabled")
			}
			if strings.Contains(buildStep, "exit 0") {
				t.Errorf("the build's step ends well somewhere before its record is read")
			}
			if !strings.Contains(jobOf(t, workflow, "migration"), "inputs.action != 'rotate' && inputs.action != 'retire' }}") {
				t.Errorf("the migration job, which runs on every action no other job names, does not stand down on retire")
			}
			rotate := jobOf(t, workflow, "rotate")
			containsAll(t, rotate,
				"      GENERATED: \""+tt.wantRotatable+"\"\n",
				"Version $VERSION of $VARIABLE stays enabled in Secret Manager, and the next release takes it up.",
			)
		})
	}
}

// containsAll reports every want the text lacks.
func containsAll(t *testing.T, got string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("the text lacks %q", want)
		}
	}
}
