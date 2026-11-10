// drivers.go names the provider drivers whose settings a configuration level embeds, each
// under the alias the skeleton binds it under, and the remedy a refusal over a driver's
// settings missing from the configuration ends in: the hand edit that embeds them where
// the stack reads them, named by that alias.

package derive

import "fmt"

// driver is one provider driver whose settings a configuration level embeds, as a remedy
// names it: the kind of driver, the import alias the skeleton binds it under, and the
// framework package of the driver.
type driver struct {
	kind  string
	alias string
	path  string
}

// The provider drivers (resource/database/spanner, resource/live/firestore,
// resource/jobs/cloudrun), which every release of the framework module carries. Each
// declares the variables it reads on a Settings struct the configuration embeds, which the
// stack reads for the variables it derives. Each is listed under the neutral alias the
// skeleton binds it under (database, liveservice, jobstarter), so a remedy names the
// import line as the configuration spells it and not the vendor's package name.
var (
	databaseDriver = driver{kind: "database", alias: "database", path: "resource/database/spanner"}
	liveDriver     = driver{kind: "live", alias: "liveservice", path: "resource/live/firestore"}
	jobDriver      = driver{kind: "job", alias: "jobstarter", path: "resource/jobs/cloudrun"}
)

// handEdit is the edit that embeds the driver's settings where the stack reads them, the
// remedy a refusal over the settings missing from the configuration ends in (embed the
// job driver's settings (jobstarter.Settings, resource/jobs/cloudrun) in the served site's
// configuration).
func (d driver) handEdit(where string) string {
	return fmt.Sprintf("embed the %s driver's settings (%s.Settings, %s) in %s", d.kind, d.alias, d.path, where)
}

// levelConfiguration names a level's configuration as a remedy places an edit in it.
func levelConfiguration(level string) string {
	return fmt.Sprintf("the %s level's configuration", level)
}
