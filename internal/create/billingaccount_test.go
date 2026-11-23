package create

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/cloudbilling/v1"

	"github.com/impulseframework/bedrock/internal/org"
)

// The billing account's members in these tests: a and b are one organization's two
// identities, x another organization's.
const (
	billingRole  = "roles/billing.user"
	billingAdmin = "roles/billing.admin"
	memberA      = "serviceAccount:imp-boot-gbl-tofu@imp-boot-gbl-core-a1b2.iam.gserviceaccount.com"
	memberB      = "serviceAccount:imp-org-gbl-tofu@imp-boot-gbl-core-a1b2.iam.gserviceaccount.com"
	memberX      = "serviceAccount:imq-boot-gbl-tofu@imq-boot-gbl-core-e5f6.iam.gserviceaccount.com"
)

// TestWithoutMembers: the members go from each binding of the role that has no condition,
// a binding left with no member goes, and every other binding and member is as read;
// nothing taken out is no change.
func TestWithoutMembers(t *testing.T) {
	t.Parallel()

	condition := &cloudbilling.Expr{Title: "until the end of the year", Expression: `request.time < timestamp("2027-01-01T00:00:00Z")`}
	tests := []struct {
		name        string
		policy      []*cloudbilling.Binding
		take        []string
		want        []*cloudbilling.Binding
		wantChanged bool
	}{
		{
			name:        "the two go from among another organization's",
			policy:      []*cloudbilling.Binding{{Role: billingRole, Members: []string{memberA, memberX, memberB}}},
			take:        []string{memberA, memberB},
			want:        []*cloudbilling.Binding{{Role: billingRole, Members: []string{memberX}}},
			wantChanged: true,
		},
		{
			name:        "a binding left with no member goes",
			policy:      []*cloudbilling.Binding{{Role: billingRole, Members: []string{memberA, memberB}}},
			take:        []string{memberA, memberB},
			want:        []*cloudbilling.Binding{},
			wantChanged: true,
		},
		{
			name:   "none of them in the role is no change",
			policy: []*cloudbilling.Binding{{Role: billingRole, Members: []string{memberX}}},
			take:   []string{memberA, memberB},
			want:   []*cloudbilling.Binding{{Role: billingRole, Members: []string{memberX}}},
		},
		{
			name:   "the role under a condition is left as read",
			policy: []*cloudbilling.Binding{{Role: billingRole, Members: []string{memberA}, Condition: condition}, {Role: billingRole, Members: []string{memberX}}},
			take:   []string{memberA},
			want:   []*cloudbilling.Binding{{Role: billingRole, Members: []string{memberA}, Condition: condition}, {Role: billingRole, Members: []string{memberX}}},
		},
		{
			name:        "another role is left as read",
			policy:      []*cloudbilling.Binding{{Role: billingAdmin, Members: []string{memberA, memberB}}, {Role: billingRole, Members: []string{memberA}}},
			take:        []string{memberA},
			want:        []*cloudbilling.Binding{{Role: billingAdmin, Members: []string{memberA, memberB}}},
			wantChanged: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, changed := withoutMembers(tt.policy, billingRole, tt.take)
			if changed != tt.wantChanged {
				t.Errorf("withoutMembers() changed = %v, want %v", changed, tt.wantChanged)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("withoutMembers() bindings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRoleMembers: the role's members across its bindings with no condition, in the
// policy's order; a conditional binding's and another role's members are not the role's.
func TestRoleMembers(t *testing.T) {
	t.Parallel()

	policy := []*cloudbilling.Binding{
		{Role: billingRole, Members: []string{memberB}},
		{Role: billingAdmin, Members: []string{memberX}},
		{Role: billingRole, Members: []string{memberX}, Condition: &cloudbilling.Expr{Expression: `request.time < timestamp("2027-01-01T00:00:00Z")`}},
		{Role: billingRole, Members: []string{memberA}},
	}
	if got, want := roleMembers(policy, billingRole), []string{memberB, memberA}; !slices.Equal(got, want) {
		t.Errorf("roleMembers() = %v, want %v", got, want)
	}
	if got := roleMembers(policy, "roles/billing.viewer"); len(got) != 0 {
		t.Errorf("roleMembers() of a role with no binding = %v, want none", got)
	}
}

// TestBillingUsers: the creation's two grants on the billing account, which the destroy
// takes off, are the boot and org identities of the boot project, in that order.
func TestBillingUsers(t *testing.T) {
	t.Parallel()

	h := &org.Placement{Prefix: "imp", Projects: map[string]string{"boot": "imp-boot-gbl-core-a1b2"}}
	want := []string{
		"serviceAccount:imp-boot-gbl-tofu@imp-boot-gbl-core-a1b2.iam.gserviceaccount.com",
		"serviceAccount:imp-org-gbl-tofu@imp-boot-gbl-core-a1b2.iam.gserviceaccount.com",
	}
	if got := BillingUsers(h); !slices.Equal(got, want) {
		t.Errorf("BillingUsers() = %v, want %v", got, want)
	}
}
