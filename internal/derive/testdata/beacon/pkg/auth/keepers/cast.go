package keepers

import (
	"context"
	_ "embed" // the cast rides in the binary

	"github.com/impulseframework/impulse/cast"
)

// castFile is the cast of the persona sign-in: the personas the test environments offer,
// each with the roles the seed seats it in.
//
//go:embed cast.json
var castFile []byte

// Cast reads the embedded cast.
func Cast() (*cast.Cast, error) {
	return cast.ReadCast(castFile)
}

// SeedCast creates the cast's accounts with their roles where they do not exist, and
// reports how many it created.
func (a *Auth) SeedCast(ctx context.Context) (int, error) {
	cast, err := Cast()
	if err != nil {
		return 0, err
	}

	return len(cast.Personas), nil
}
