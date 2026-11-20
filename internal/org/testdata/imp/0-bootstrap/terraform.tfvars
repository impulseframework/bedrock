# Committed on purpose: nothing here is a secret. The creation writes the
# three IDs below (README.md, "Created by the root organization", steps 1 and
# 2): the boot project's ID, suffix included, and the bare numeric IDs of the
# organization and terraform folders its seed made. A REPLACEME is an ID a
# render outside a creation did not have.

billing_account_id = "012345-6789AB-CDEF01"
# boot_project_id is the ID the seed step chose, suffix included;
# organization_folder_id and terraform_folder_id are the bare numeric folder
# IDs from the seed step, under organization 123456789012.
boot_project_id        = "imp-boot-gbl-core-a1b2"
organization_folder_id = "REPLACEME"
terraform_folder_id    = "REPLACEME"

# The two roles/billing.user grants need Billing Account Administrator. The
# apply trigger applies this layer as the boot layer identity, a Billing
# Account User, so this is false and the creation makes the grants, as the
# root organization's creating identity (README.md, "Created by the root
# organization", step 2); set it true once the identity applying this layer
# can manage billing IAM.
manage_billing_iam = false
