#!/usr/bin/env bash
# Build an image of the migration runner, superschematic-migrate, from this
# checkout's runtime/migrate/go, on Cloud Build, and print it by digest.
#
#   export SUPERSCHEMATIC_MIGRATE_IMAGE="$(scripts/migrate-dev-image.sh <project> <region> <stack>)"
#
# A deploy to gcp runs the runner as the stack's migration job. A binary
# built from a checkout names no release whose image the gcp target could
# build, so it takes the image SUPERSCHEMATIC_MIGRATE_IMAGE names
# (docs/stack-model.md, section 8.4). This builds one as a deploy builds a
# server's image, after `stack bootstrap`:
#
#   - the source archive goes to the state bucket, under
#     superschematic/builds/, the one prefix the builder account reads;
#   - the build runs in the environment's region as the stack's builder
#     account, `<stack>-builder`, with its logs in Cloud Logging only;
#   - the image goes to the stack's Artifact Registry repository, named
#     after the stack, as superschematic-migrate, tagged dev-<hash> with a
#     hash of the runner's source, so the same source is built once.
#
# The image is the release's (extensions/gcp, migrateDockerfile), with
# `go build` from the checkout in place of `go install` at a tag. It needs
# gcloud, signed in with an account that can run builds as the builder
# account and write to the state bucket. Progress goes to stderr, the image
# to stdout.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <project> <region> <stack>" >&2
  exit 2
fi
project="$1"
region="$2"
stack="$3"

cd "$(dirname "${BASH_SOURCE[0]}")/.."
source_dir=runtime/migrate/go
GO_VERSION="$(sed -n 's/^GO_VERSION=//p' tools.env)"

# The tag hashes every file the build reads, so an uncommitted change to
# the runner gets an image of its own.
hash="$( (cd "$source_dir" && find . -type f -not -path './testdata/*' -not -name '*_test.go' -print0 |
  LC_ALL=C sort -z | xargs -0 shasum -a 256; echo "GO_VERSION=$GO_VERSION") | shasum -a 256 | cut -c1-16)"
repository="$region-docker.pkg.dev/$project/$stack/superschematic-migrate"
image="$repository:dev-$hash"

digest_of() {
  gcloud artifacts docker images describe "$1" --project "$project" --format='value(image_summary.digest)' 2>/dev/null
}

if digest="$(digest_of "$image")" && [[ -n "$digest" ]]; then
  echo "$image exists" >&2
  echo "$repository@$digest"
  exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir "$work/src"
tar -C "$source_dir" --exclude ./testdata --exclude '*_test.go' -cf - . | tar -C "$work/src" -xf -
cat >"$work/src/Dockerfile" <<EOF
# The migration runner, superschematic-migrate, built from a checkout
# (scripts/migrate-dev-image.sh).
ARG GO_VERSION=$GO_VERSION

FROM golang:\${GO_VERSION}-trixie AS build
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
WORKDIR /src
COPY . .
RUN go build -o /out/superschematic-migrate ./cmd/superschematic-migrate

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/superschematic-migrate /superschematic-migrate
ENTRYPOINT ["/superschematic-migrate"]
EOF
cat >"$work/cloudbuild.json" <<EOF
{
  "steps": [{"name": "gcr.io/cloud-builders/docker", "env": ["DOCKER_BUILDKIT=1"], "args": ["build", "-t", "$image", "."]}],
  "images": ["$image"],
  "serviceAccount": "projects/$project/serviceAccounts/$stack-builder@$project.iam.gserviceaccount.com",
  "options": {"logging": "CLOUD_LOGGING_ONLY"}
}
EOF

echo "build $image on Cloud Build in $region" >&2
gcloud builds submit "$work/src" \
  --project "$project" \
  --region "$region" \
  --config "$work/cloudbuild.json" \
  --gcs-source-staging-dir "gs://$project-superschematic-state/superschematic/builds/$stack/superschematic-migrate" \
  >&2

digest="$(digest_of "$image")"
if [[ -z "$digest" ]]; then
  echo "the build pushed no $image" >&2
  exit 1
fi
echo "$repository@$digest"
