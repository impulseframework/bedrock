package staff

import (
	"context"

	"github.com/go-playground/errors/v5"
	"github.com/impulseframework/impulse/accesstypes"
	"github.com/impulseframework/impulse/session"
)

// AdministratorVariable is the setting the placement names the auth's first
// administrator in, for each environment: the person's email address.
const AdministratorVariable = "APP_STAFF_ADMINISTRATOR"

// AdministratorRoles are the roles the first administrator holds, by where they are held:
// what the development identities hold (cmd/bootstrap/users.json), a domain role in every
// tenant domain, so the foothold can do what the development logins can.
func AdministratorRoles() (global, everyDomain []accesstypes.Role) {
	return []accesstypes.Role{"Administrator", "FeatureAdministrator"}, []accesstypes.Role{"TenantAdministrator"}
}

// InviteAdministrator invites the auth's first administrator, the way into a deployed
// environment: while nobody of the auth holds the Super Administrator role, the migrate
// command has an invitation mailed to email, the address the placement names
// (AdministratorVariable), which gives the Super Administrator role and the
// application's own roles for the first administrator (AdministratorRoles).
func (a *Auth) InviteAdministrator(ctx context.Context, email string) (*session.FirstAdministrator, error) {
	invited, err := a.handlers.API().InviteFirstAdministrator(ctx, a.access.UserManager(), &session.FirstAdministratorRequest{Email: email, RoleNames: administratorRoleNames()})
	if err != nil {
		return nil, errors.Wrapf(err, "the %s auth's first administrator (%s)", Name, AdministratorVariable)
	}

	return invited, nil
}

// ReinviteAdministrator withdraws the first administrator's standing invitation and
// mails a fresh one to email, for a message that was lost: the earlier link no longer
// opens.
func (a *Auth) ReinviteAdministrator(ctx context.Context, email string) (*session.FirstAdministrator, error) {
	invited, err := a.handlers.API().ReinviteFirstAdministrator(ctx, a.access.UserManager(), &session.FirstAdministratorRequest{Email: email, RoleNames: administratorRoleNames()})
	if err != nil {
		return nil, errors.Wrapf(err, "the %s auth's first administrator (%s)", Name, AdministratorVariable)
	}

	return invited, nil
}

// administratorRoleNames are the roles the first administrator's invitation carries: the
// role the framework defines in every auth (accesstypes.SuperAdministrator), held for the
// whole application, whose holder alone makes another, and the application's own
// (AdministratorRoles).
func administratorRoleNames() []string {
	global, everyDomain := AdministratorRoles()
	names := []string{string(accesstypes.SuperAdministrator)}
	for _, role := range global {
		names = append(names, string(role))
	}
	for _, role := range everyDomain {
		names = append(names, string(role))
	}

	return names
}
