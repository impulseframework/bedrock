package derive

import (
	"strings"
	"testing"

	"github.com/impulseframework/impulse/app"
)

// TestDriverHandEdit holds each refusal over a driver's settings missing from the
// configuration to the hand edit by the driver's alias: a job process without the job
// driver's settings, no database identity, and a Firestore database without the live
// driver's project variable.
func TestDriverHandEdit(t *testing.T) {
	t.Parallel()

	site := Process{Name: siteProcess, Dir: ".", Main: mainFile, Levels: []string{LevelCore, LevelData, LevelSite}}
	jobs := &Process{Name: jobsProcess, Dir: jobsDir, Main: jobsDir, Levels: []string{LevelCore, LevelData}}
	database := Variable{Name: varFirestoreDatabase, Role: RoleFirestoreDatabase, Level: LevelData, Struct: "dataConfig", Field: "FirestoreDatabase"}
	tests := []struct {
		name string
		// refuse runs the refusal over a model missing the driver's settings.
		refuse func(t *testing.T) error
		// handEdit is the remedy the refusal ends in.
		handEdit string
	}{
		{
			name: "a job process without the job driver's settings",
			refuse: func(_ *testing.T) error {
				m := Model{Sites: []Site{{Name: "harbor", Dir: ".", Process: site}}, Jobs: jobs, Variables: []Variable{{Name: "APP_OTHER", Level: LevelSite}}}

				return m.jobsTemplate()
			},
			handEdit: "embed the job driver's settings (jobstarter.Settings, resource/jobs/cloudrun) in the served site's configuration",
		},
		{
			name: "no database identity",
			refuse: func(_ *testing.T) error {
				m := Model{Variables: []Variable{{Name: "APP_OTHER", Level: LevelData}}}

				return m.classify(&config{})
			},
			handEdit: "embed the database driver's settings (database.Settings, resource/database/spanner) in the data level's configuration",
		},
		{
			name: "a Firestore database without the live driver's project variable",
			refuse: func(t *testing.T) error {
				t.Helper()

				m := Model{Variables: []Variable{database}}

				return m.firestore(&app.App{Root: t.TempDir()})
			},
			handEdit: "embed the live driver's settings (liveservice.Settings, resource/live/firestore) in the data level's configuration, in place of the variables declared by hand",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.refuse(t)
			if err == nil || !strings.HasSuffix(err.Error(), "; "+tt.handEdit) {
				t.Errorf("error = %v, want it to end in %q", err, tt.handEdit)
			}
		})
	}
}
