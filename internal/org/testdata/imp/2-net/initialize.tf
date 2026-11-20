terraform {
  # Same bucket as the foundation layers, prefix = this directory. A backend
  # block cannot read a variable, so every render writes the bucket's name
  # here, from placement.json (stateBucket): the bucket the creation's seed
  # made (0-bootstrap/README.md, "Created by the root organization", step 1).
  # The remote state block below carries the same name, written by the same
  # render.
  backend "gcs" {
    bucket = "imp-boot-gbl-state-a1b2"
    prefix = "2-net"
  }
  required_version = ">= 1.11.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 7.0"
    }
  }
}

# Everything this layer needs to know about the organization comes from 1-org:
# the net project and its number, the net layer identity, the boot project,
# the prefix. Reading them here rather than restating them in terraform.tfvars
# means no ID is typed twice.
data "terraform_remote_state" "org" {
  backend = "gcs"
  config = {
    bucket = "imp-boot-gbl-state-a1b2"
    prefix = "1-org"
  }
}

# Every API call is billed and quota-counted against the boot project, the
# same arrangement as 1-org; the layer identity holds serviceUsageConsumer
# there for exactly this. The default project is the one this layer manages,
# and every resource names it explicitly anyway.
provider "google" {
  project               = local.project_id
  user_project_override = true
  billing_project       = local.org.boot_project_id
}

provider "google-beta" {
  project               = local.project_id
  user_project_override = true
  billing_project       = local.org.boot_project_id
}
