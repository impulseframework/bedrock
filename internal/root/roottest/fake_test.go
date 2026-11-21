package roottest_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/impulseframework/bedrock/internal/root"
	"github.com/impulseframework/bedrock/internal/root/roottest"
)

// TestDeleteZoneRefusesAZoneThatHoldsRecords: the stand-in deletes a zone only once its
// apex NS and SOA are all it holds, as Cloud DNS does, so a destroy that passes on it
// deleted the zone's record sets first.
func TestDeleteZoneRefusesAZoneThatHoldsRecords(t *testing.T) {
	t.Parallel()

	const (
		project = "imp-net-gbl-core-9c0d"
		zone    = "imp-net-gbl-dns-apps"
		apex    = "apps.imp.example."
	)
	ctx := context.Background()
	fake := roottest.New()
	if err := fake.CreateZone(ctx, project, root.Zone{Name: zone, DNSName: apex}); err != nil {
		t.Fatal(err)
	}
	// One record set under the apex and one at it, neither the zone's own NS or SOA.
	under := root.RecordSet{Name: "quill." + apex, Type: "A", TTL: 300, Data: []string{"203.0.113.10"}}
	atApex := root.RecordSet{Name: apex, Type: "TXT", TTL: 300, Data: []string{`"v=spf1 -all"`}}
	if err := fake.ChangeRecordSets(ctx, project, zone, []root.RecordSet{under, atApex}, nil); err != nil {
		t.Fatal(err)
	}
	for _, left := range []root.RecordSet{under, atApex} {
		err := fake.DeleteZone(ctx, project, zone)
		if err == nil || !strings.Contains(err.Error(), "cannot be deleted because it is not empty") {
			t.Fatalf("DeleteZone() with %s %s still in the zone = %v, want Cloud DNS's refusal of a zone that is not empty", left.Type, left.Name, err)
		}
		if _, stands := fake.ZoneMap[project][zone]; !stands || len(fake.Deletions) != 0 {
			t.Fatalf("the refused zone is gone, or a deletion is recorded: %v, %v", fake.ZoneMap[project], fake.Deletions)
		}
		if err := fake.ChangeRecordSets(ctx, project, zone, nil, []root.RecordSet{left}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fake.DeleteZone(ctx, project, zone); err != nil {
		t.Fatalf("DeleteZone() of a zone holding its apex NS and SOA alone = %v, want it deleted", err)
	}
	if _, stands := fake.ZoneMap[project][zone]; stands || !slices.Equal(fake.Deletions, []string{"zone " + project + "/" + zone}) {
		t.Errorf("after the deletion the project holds %v and the deletions are %v, want no zone and the one deletion", fake.ZoneMap[project], fake.Deletions)
	}
}
