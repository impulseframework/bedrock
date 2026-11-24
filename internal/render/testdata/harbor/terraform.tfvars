# Committed on purpose: nothing here is a secret. state_bucket is the
# organization's state bucket, from this application's placement.json
# (stateBucket), where bedrock org register recorded it: the same name every
# render writes into the backend block.
# environment is never set here: every run passes -var environment=<env>.

state_bucket = "imp-boot-gbl-state-REPLACEME"

# Placement per environment, filled as each environment is set up. Every map
# is keyed by environment so one file serves every environment's apply.

# The members auth's directory registration with Microsoft (README, "Hand
# steps"): the client id of each environment's registration, and the tenant whose
# accounts it admits, by its tenant id (the directory (tenant) ID on the app
# registration's overview page), once for the organization.
members_directory_client_id = {
  tst = ""
  stg = ""
  prd = ""
}
members_directory_tenant_id = ""
# The tenants the deployment administers among those the registration admits, by
# tenant id, comma-separated, once for the organization: a sign-in from one carries
# the tenant's own second factor (a Conditional Access policy requiring multifactor
# authentication of everyone) and is asked for no sign-in code; empty, every sign-in
# is asked for the code.
members_directory_administered_tenants = ""

# The members auth's okta registration with the identity provider (README, "Hand
# steps"): the client id of each environment's registration, and the provider's
# issuer URL, the one its configuration is read under, once for the
# organization.
members_okta_client_id = {
  tst = ""
  stg = ""
  prd = ""
}
members_okta_issuer = ""

# The staff auth's directory registration with Google (README, "Hand
# steps"): the client id of each environment's registration.
staff_directory_client_id = {
  tst = ""
  stg = ""
  prd = ""
}
# The Workspaces the deployment administers among those the registration admits, by
# domain, comma-separated, once for the organization: a sign-in from one carries the
# Workspace's own second factor (2-Step Verification enforced for everyone) and is
# asked for no sign-in code; empty, every sign-in is asked for the code.
staff_directory_administered_workspaces = ""

# The members auth's first administrator, per environment (README, "Hand steps"):
# the person's email address, which the first release mails the invitation to.
members_administrator = {
  tst = ""
  stg = ""
  prd = ""
}

# The Google Workspace mailbox the auths' mail is sent as, once for the
# organization (README, "Hand steps"); the application does not start without
# it and its app password.
mail_mailbox = ""

# The version of each secret an environment runs. No entry means "not yet": the
# container exists, nothing is mounted; tst alone runs that way,
# and a release to any other environment is refused until every secret the
# code declares is pinned there. Bump here after someone adds a version
# (bedrock secret pin). "latest" only for a secret the placement marks as
# tracking it. "generated" for a secret the code declares secret:"generated":
# the pipeline makes its value and derives its version at each build, so the
# word stays, and bedrock secret rotate adds the next version. "versioned" for
# one it declares secret:"versioned": made the same way, and a process
# receives every enabled version (bedrock secret retire takes one away).
secret_versions = {
  tst = {}
  stg = {}
  prd = {}
}

# The secrets and file stores a release retires, by variable: anything the
# running release uses is retired in one release and removed in the next.
# Naming one here lifts the protection that refuses its destroy at a plan;
# the next release removes its declaration and takes the name out again.
retired = []

# Extra trigger substitutions for the application's hooks and its image build,
# per environment (_NAME = value); none by default (README, "Customizing the
# pipeline").
substitutions = {
  tst = {}
  stg = {}
  prd = {}
}

# Secrets the image build reads, per environment (NAME = pinned version of the
# container imp-<env>-gbl-harbor-<kebab name>, which the stack creates and only
# the deploy identity may read); none by default (README, "Build secrets").
build_secrets = {
  tst = {}
  stg = {}
  prd = {}
}

# Cloud Armor per environment (README, "Cloud Armor"): "preview" evaluates the
# policy and logs what each rule would have done, "enforce" applies it. Off for
# an environment left out. Preview first, read the logs, then enforce. The
# policy throttles the sign-in routes by client address, at the
# cloud_armor_sign_in_count and cloud_armor_sign_in_interval restated below;
# write either here to change it.
cloud_armor = {
}

# Defaults that are decisions, restated so they are visible here:
#   hostnames                     = harbor-tst. / harbor-stg. / harbor.impulseframework.dev
#   placeholder_image             = ghcr.io/impulseframework/welcome@sha256:106eb632ba7ae2857a044cec68c6efd8d647f67b2355f3b37e206b1686fee6aa
#   staff_directory_group_lookup  = "direct"
#   staff_directory_group_prefix  = "staff-"
#   staff_directory_hosted_domain = "impulseframework.com"
#   mail_mailbox                  = ""
#   jobs_timeout                  = "1800s"
#   jobs_retries                  = 0
#   jobs_resources                = cpu 1, memory 512Mi
#   tasks_max_concurrent          = 10
#   tasks_max_attempts            = 5
#   cloud_armor_sign_in_count     = 30
#   cloud_armor_sign_in_interval  = 60
