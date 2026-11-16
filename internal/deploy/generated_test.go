package deploy

import (
	"maps"
	"slices"
	"testing"
)

// TestDerivedPins: the plan's variables for the derived versions, sorted by variable as
// tofu takes a map on the command line (a number per generated secret, a list per
// versioned one, oldest first), and nothing without any; the facts the record reads back,
// and the reads.
func TestDerivedPins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		pins          *derivedPins
		wantArgs      []string
		wantGenerated string
		wantVersioned string
	}{
		{name: "no secret the tooling makes", pins: nil},
		{name: "none derived", pins: &derivedPins{Generated: map[string]string{}, Versioned: map[string][]string{}}},
		{
			name:          "one generated",
			pins:          &derivedPins{Generated: map[string]string{"APP_COOKIE_KEY": "3"}},
			wantArgs:      []string{"-var", `generated_secret_versions={APP_COOKIE_KEY="3"}`},
			wantGenerated: "APP_COOKIE_KEY=3",
		},
		{
			name:          "several generated, sorted",
			pins:          &derivedPins{Generated: map[string]string{"APP_SIGNING_KEY": "12", "APP_COOKIE_KEY": "3"}},
			wantArgs:      []string{"-var", `generated_secret_versions={APP_COOKIE_KEY="3", APP_SIGNING_KEY="12"}`},
			wantGenerated: "APP_COOKIE_KEY=3,APP_SIGNING_KEY=12",
		},
		{
			name:          "a versioned secret's every enabled version, oldest first, beside a generated secret's one",
			pins:          &derivedPins{Generated: map[string]string{"APP_COOKIE_KEY": "3"}, Versioned: map[string][]string{"APP_SECRET_KEY": {"1", "2", "11"}}},
			wantArgs:      []string{"-var", `generated_secret_versions={APP_COOKIE_KEY="3"}`, "-var", `versioned_secret_versions={APP_SECRET_KEY=["1", "2", "11"]}`},
			wantGenerated: "APP_COOKIE_KEY=3",
			wantVersioned: "APP_SECRET_KEY=1 2 11",
		},
		{
			name:          "several versioned, sorted, one of them on one version",
			pins:          &derivedPins{Versioned: map[string][]string{"APP_SECRET_KEY": {"2"}, "APP_LEDGER_KEY": {"1", "4"}}},
			wantArgs:      []string{"-var", `versioned_secret_versions={APP_LEDGER_KEY=["1", "4"], APP_SECRET_KEY=["2"]}`},
			wantVersioned: "APP_LEDGER_KEY=1 4,APP_SECRET_KEY=2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := generatedArgs(tt.pins); !slices.Equal(got, tt.wantArgs) {
				t.Errorf("generatedArgs() = %q, want %q", got, tt.wantArgs)
			}
			facts := tt.pins.facts()
			if facts[generatedFact] != tt.wantGenerated || facts[versionedFact] != tt.wantVersioned {
				t.Errorf("facts() = %q, want %s=%q and %s=%q", facts, generatedFact, tt.wantGenerated, versionedFact, tt.wantVersioned)
			}
			if len(facts) != min(len(tt.wantGenerated), 1)+min(len(tt.wantVersioned), 1) {
				t.Errorf("facts() = %q leaves a fact for a kind that derived none", facts)
			}
			var wantGenerated map[string]string
			var wantVersioned map[string][]string
			if tt.pins != nil && len(tt.pins.Generated) > 0 {
				wantGenerated = tt.pins.Generated
			}
			if tt.pins != nil && len(tt.pins.Versioned) > 0 {
				wantVersioned = tt.pins.Versioned
			}
			if got := generatedOf(facts); !maps.Equal(got, wantGenerated) || (wantGenerated == nil) != (got == nil) {
				t.Errorf("generatedOf() = %v, want %v", got, wantGenerated)
			}
			if got := versionedOf(facts); !maps.EqualFunc(got, wantVersioned, slices.Equal[[]string]) || (wantVersioned == nil) != (got == nil) {
				t.Errorf("versionedOf() = %v, want %v", got, wantVersioned)
			}
		})
	}
}

// TestIdentityProject: the project a service account's email belongs to.
func TestIdentityProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		email string
		want  string
	}{
		{email: "quill-plan@p-stg.iam.gserviceaccount.com", want: "p-stg"},
		{email: "imp-tst-gbl-harbor-apply@imp-tst-gbl-core-b241.iam.gserviceaccount.com", want: "imp-tst-gbl-core-b241"},
		{email: "nobody", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			t.Parallel()

			if got := identityProject(tt.email); got != tt.want {
				t.Errorf("identityProject(%q) = %q, want %q", tt.email, got, tt.want)
			}
		})
	}
}
