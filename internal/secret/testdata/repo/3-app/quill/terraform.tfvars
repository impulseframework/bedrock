# Committed on purpose: nothing here is a secret.

state_bucket = "lab-boot-state-1234"

# The version of each secret an environment runs. Empty means "not yet": the
# container exists, nothing is mounted.
secret_versions = {
  tst = {
    APP_COOKIE_KEY = "2"
  }
  stg = {}
  prd = {}
}

# Defaults that are decisions, restated so they are visible here:
#   placeholder_image = ghcr.io/impulseframework/welcome@sha256:106eb632ba7ae2857a044cec68c6efd8d647f67b2355f3b37e206b1686fee6aa
