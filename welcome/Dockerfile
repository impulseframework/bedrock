# The welcome image: the one binary and nothing under it. The binary is built outside
# the image, static, for linux/amd64, which is what Cloud Run runs, by the workflow that
# publishes the image (.github/workflows/welcome-image.yml) and by README.md's
# "The welcome image" for a build by hand; the build context is the directory that
# holds that binary alone.
#
# An ENTRYPOINT and no CMD, as Cloud Run's public sample has, which the image takes the
# place of: a multi-site application's stack creates each service with the site's binary
# as its container's arguments, and the entrypoint keeps the image starting on its own
# program, which does not read them. There is no /jobs here, as there is none in the
# sample: the stack creates its job with that command as the template of the jobs each
# build makes from the application's image, and nothing runs the template itself.
FROM scratch
COPY welcome /welcome
USER 65532:65532
ENTRYPOINT ["/welcome"]
