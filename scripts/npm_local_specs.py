#!/usr/bin/env python3
"""Refuse an npm tarball whose package.json names a dependency by a local path.

  python3 scripts/npm_local_specs.py release-assets/npm/*.tgz

A spec such as file:../../versiongraph/typescript installs inside this
checkout, where the path exists, and nowhere else: an install from the
published tarball leaves the dependency unresolved. release.yml packs each
package, rewrites the engine's file: spec of @superschematic/versiongraph
to the release's version first, and runs this on every tarball it packed,
so a local spec that reaches a tarball fails the release before anything
is published. Development dependencies are not installed from a tarball,
so their specs are not read.

Python 3.9+, standard library only.
"""
import json
import re
import sys
import tarfile

# The specs that name a path or a workspace rather than a registry version.
LOCAL = re.compile(r"^(file|link|workspace|portal):")

# The members a consumer's install reads.
SECTIONS = ("dependencies", "peerDependencies", "optionalDependencies")


def local_specs(manifest):
    """The section.name=spec of each local spec manifest's installed sections hold, sorted."""
    out = []
    for section in SECTIONS:
        for name, spec in sorted((manifest.get(section) or {}).items()):
            if isinstance(spec, str) and LOCAL.match(spec):
                out.append(f"{section}.{name}={spec}")
    return out


def tarball_manifest(path):
    """The package.json an npm pack tarball holds."""
    with tarfile.open(path, "r:gz") as tar:
        member = tar.extractfile("package/package.json")
        if member is None:
            raise ValueError(f"{path}: package/package.json is not a file")
        return json.load(member)


def main(argv):
    if not argv:
        print("usage: npm_local_specs.py <tarball>...", file=sys.stderr)
        return 2
    status = 0
    for path in argv:
        specs = local_specs(tarball_manifest(path))
        for spec in specs:
            print(f"{path}: {spec} would not install from the tarball", file=sys.stderr)
        if specs:
            status = 1
    return status


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
