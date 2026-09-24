#!/usr/bin/env python3
"""One version for the whole repository: set it, check it, read it.

The npm packages, the PyPI distribution, the crate and the Go modules share one
SemVer version (CONTRIBUTING.md, "Releases"). versions.env holds it and this
script is the only thing that writes it or the sites below. It edits the files
in place with anchored regular expressions so comments and formatting survive,
and it refuses to continue if any site matched a different number of times
than expected.

  bump_version.py current                 print the version (versions.env)
  bump_version.py set <version>           write <version> everywhere and cut the
                                          CHANGELOG "Unreleased" section into a
                                          dated "[<version>]" section
  bump_version.py check [--expect <v>]    every site agrees (and equals <v>);
                                          with --expect, the changelog must
                                          also carry a "[<v>]" section
  bump_version.py pep440 <version>        the PEP 440 form of a SemVer version
  bump_version.py notes <version>         print the CHANGELOG section for <version>
  bump_version.py go-modules              print the Go module tag prefixes, one
                                          per line ("" for the root module)

Version sites (relative to the repository root):
  versions.env                        VERSION=
  packages/{api,db,schema,schema-config}/package.json
                                      version
  packages/bun.lock                   the four workspace entries
  runtime/schema/typescript/package.json
                                      version
  runtime/http/typescript/package.json
                                      version
  runtime/schema/python/pyproject.toml
                                      [project] version, in PEP 440 form
  runtime/http/rust/Cargo.toml        [package] version
  runtime/http/rust/Cargo.lock        the superschematic-http-runtime package
  go.mod                              require .../superschematic/ir vX.Y.Z
  runtime/schema/go/go.mod            require .../superschematic/ir vX.Y.Z
  runtime/http/go/go.mod              require .../runtime/schema/go vX.Y.Z and
                                      .../superschematic/ir vX.Y.Z
  CHANGELOG.md                        the released sections and their links

The Go requires carry the release version so a consumer at a tag resolves
the sibling modules from their own tags (ir/vX.Y.Z and so on, all cut on the
same commit by go-module-tag.yml); the `replace` lines next to them keep
local builds on the checkout. 0.0.0 is the unreleased version.

Pre-releases: SemVer `-alpha.N`, `-beta.N` and `-rc.N` map to PEP 440 `aN`,
`bN` and `rcN`. Other pre-release identifiers and build metadata are rejected
because PyPI could not carry them.

Python 3.9+, standard library only.
"""
import argparse
import datetime
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
REPO_URL = "https://github.com/parable-work/superschematic"
GO_MODULE = "github.com/parable-work/superschematic"

NPM_WORKSPACE_PACKAGES = ["api", "db", "schema", "schema-config"]

# Directory of every Go module, in dependency order. The tag for a module in a
# subdirectory is the directory followed by /vX.Y.Z; the root module's is
# vX.Y.Z.
GO_MODULES = ["ir", "runtime/schema/go", "runtime/http/go", ""]

SEMVER = re.compile(
    r"^(?P<core>0|[1-9]\d*)\.(?P<minor>0|[1-9]\d*)\.(?P<patch>0|[1-9]\d*)"
    r"(?:-(?P<pre>(?:alpha|beta|rc)\.(?:0|[1-9]\d*)))?$"
)


class VersionError(Exception):
    pass


def validate(version: str) -> str:
    if version.startswith("v"):
        raise VersionError(f"give the version without the leading v: {version}")
    if not SEMVER.match(version):
        raise VersionError(
            f"not a supported SemVer version: {version} "
            "(X.Y.Z, optionally -alpha.N, -beta.N or -rc.N)"
        )
    return version


def is_prerelease(version: str) -> bool:
    return "-" in validate(version)


def pep440(version: str) -> str:
    m = SEMVER.match(validate(version))
    base = "{}.{}.{}".format(m.group("core"), m.group("minor"), m.group("patch"))
    pre = m.group("pre")
    if pre is None:
        return base
    label, number = pre.split(".")
    return base + {"alpha": "a", "beta": "b", "rc": "rc"}[label] + number


# --- sites -------------------------------------------------------------------
#
# Each site is (path, list of (pattern, expected_count), form). Patterns
# capture the text around the version in groups 1 and 3 (quotes included) and
# the bare version itself in group 2. `format_version` picks the SemVer or
# PEP 440 spelling per file; go.mod carries the SemVer after a literal v that
# is part of group 1.

V = r"([^\"\s]+)"


def go_require_pattern(module: str) -> str:
    return r"(\n\t" + re.escape(module) + r" v)" + V + r"((?: // indirect)?\n)"


def sites():
    out = []
    out.append((ROOT / "versions.env", [(r"(^VERSION=)([^\n]+)(\n)", 1)], "semver"))
    for pkg in NPM_WORKSPACE_PACKAGES:
        out.append(
            (
                ROOT / "packages" / pkg / "package.json",
                [(r'(\n  "version": ")' + V + r'(",)', 1)],
                "semver",
            )
        )
    out.append(
        (
            ROOT / "packages" / "bun.lock",
            [
                (
                    r'(\n      "name": "@superschematic/' + re.escape(pkg) + r'",\n      "version": ")' + V + r'(",)',
                    1,
                )
                for pkg in NPM_WORKSPACE_PACKAGES
            ],
            "semver",
        )
    )
    out.append(
        (
            ROOT / "runtime" / "schema" / "typescript" / "package.json",
            [(r'(\n  "version": ")' + V + r'(",)', 1)],
            "semver",
        )
    )
    out.append(
        (
            ROOT / "runtime" / "http" / "typescript" / "package.json",
            [(r'(\n  "version": ")' + V + r'(",)', 1)],
            "semver",
        )
    )
    out.append(
        (
            ROOT / "runtime" / "schema" / "python" / "pyproject.toml",
            [(r'(\[project\]\nname = "superschematic-schema-runtime"\nversion = ")' + V + r'(")', 1)],
            "pep440",
        )
    )
    out.append(
        (
            ROOT / "runtime" / "http" / "rust" / "Cargo.toml",
            [(r'(\[package\]\nname = "superschematic-http-runtime"\nversion = ")' + V + r'(")', 1)],
            "semver",
        )
    )
    out.append(
        (
            ROOT / "runtime" / "http" / "rust" / "Cargo.lock",
            [(r'(\[\[package\]\]\nname = "superschematic-http-runtime"\nversion = ")' + V + r'(")', 1)],
            "semver",
        )
    )
    out.append((ROOT / "go.mod", [(go_require_pattern(GO_MODULE + "/ir"), 1)], "gomod"))
    out.append(
        (
            ROOT / "runtime" / "schema" / "go" / "go.mod",
            [(go_require_pattern(GO_MODULE + "/ir"), 1)],
            "gomod",
        )
    )
    out.append(
        (
            ROOT / "runtime" / "http" / "go" / "go.mod",
            [
                (go_require_pattern(GO_MODULE + "/runtime/schema/go"), 1),
                (go_require_pattern(GO_MODULE + "/ir"), 1),
            ],
            "gomod",
        )
    )
    return out


def format_version(version: str, form: str) -> str:
    if form == "pep440":
        return pep440(version)
    return version


def read_sites():
    """Yield (path, form, found_version) for every version occurrence."""
    for path, patterns, form in sites():
        text = path.read_text(encoding="utf-8")
        for pattern, expected in patterns:
            found = re.findall(pattern, text, flags=re.M)
            if len(found) != expected:
                raise VersionError(
                    f"{path.relative_to(ROOT)}: expected {expected} match(es) for "
                    f"{pattern!r}, found {len(found)}"
                )
            for _, version, _ in found:
                yield path, form, version


def current_version() -> str:
    text = (ROOT / "versions.env").read_text(encoding="utf-8")
    m = re.search(r"^VERSION=([^\n]+)$", text, flags=re.M)
    if m is None:
        raise VersionError("versions.env has no VERSION= line")
    return validate(m.group(1))


def check(expect=None) -> str:
    version = current_version()
    if expect is not None and version != validate(expect):
        raise VersionError(f"versions.env is {version}, expected {expect}")
    problems = []
    for path, form, found in read_sites():
        want = format_version(version, form)
        if found != want:
            problems.append(f"{path.relative_to(ROOT)}: {found} (want {want})")
    if expect is not None and changelog_section(version) is None:
        problems.append(f"CHANGELOG.md: no '## [{version}]' section")
    if problems:
        raise VersionError("version sites disagree:\n  " + "\n  ".join(problems))
    return version


def write_sites(version: str) -> None:
    for path, patterns, form in sites():
        text = path.read_text(encoding="utf-8")
        want = format_version(version, form)
        for pattern, expected in patterns:
            text, n = re.subn(pattern, lambda m: m.group(1) + want + m.group(3), text, flags=re.M)
            if n != expected:
                raise VersionError(
                    f"{path.relative_to(ROOT)}: expected {expected} match(es) for "
                    f"{pattern!r}, found {n}"
                )
        path.write_text(text, encoding="utf-8")


# --- changelog ---------------------------------------------------------------

CHANGELOG = ROOT / "CHANGELOG.md"
SECTION = re.compile(r"^## \[(?P<name>[^\]]+)\](?: - (?P<date>\d{4}-\d{2}-\d{2}))?$", re.M)


def changelog_sections():
    """Return [(name, date, body)] in file order. body excludes the heading."""
    text = CHANGELOG.read_text(encoding="utf-8")
    heads = list(SECTION.finditer(text))
    out = []
    for i, h in enumerate(heads):
        end = heads[i + 1].start() if i + 1 < len(heads) else len(text)
        body = text[h.end() : end]
        # The link reference block at the end of the file is not part of a section.
        body = re.sub(r"(?:^\[[^\]]+\]: \S+\n?)+\Z", "", body, flags=re.M)
        out.append((h.group("name"), h.group("date"), body.strip("\n")))
    return out


def changelog_section(version: str):
    for name, _, body in changelog_sections():
        if name == version:
            return body
    return None


def cut_changelog(version: str, today: str) -> None:
    text = CHANGELOG.read_text(encoding="utf-8")
    sections = changelog_sections()
    if not sections or sections[0][0] != "Unreleased":
        raise VersionError("CHANGELOG.md must start its sections with '## [Unreleased]'")
    if changelog_section(version) is not None:
        raise VersionError(f"CHANGELOG.md already has a '## [{version}]' section")
    if not sections[0][2].strip():
        raise VersionError("CHANGELOG.md: the Unreleased section is empty; nothing to release")
    previous = sections[1][0] if len(sections) > 1 else None

    heading = f"## [{version}] - {today}"
    text, n = re.subn(r"^## \[Unreleased\]\n", "## [Unreleased]\n\n" + heading + "\n", text, count=1, flags=re.M)
    if n != 1:
        raise VersionError("CHANGELOG.md: could not find the Unreleased heading")

    unreleased_link = f"[Unreleased]: {REPO_URL}/compare/v{version}...HEAD"
    if previous is None:
        version_link = f"[{version}]: {REPO_URL}/releases/tag/v{version}"
    else:
        version_link = f"[{version}]: {REPO_URL}/compare/v{previous}...v{version}"
    text, n = re.subn(
        r"^\[Unreleased\]: \S+$",
        unreleased_link + "\n" + version_link,
        text,
        count=1,
        flags=re.M,
    )
    if n != 1:
        raise VersionError("CHANGELOG.md: could not find the [Unreleased] link reference")
    CHANGELOG.write_text(text, encoding="utf-8")


# --- commands ----------------------------------------------------------------


def cmd_current(_args) -> int:
    print(current_version())
    return 0


def cmd_set(args) -> int:
    version = validate(args.version)
    before = current_version()
    check()
    today = args.date or datetime.date.today().isoformat()
    cut_changelog(version, today)
    write_sites(version)
    check(expect=version)
    print(f"version {before} -> {version} (PyPI {pep440(version)}); CHANGELOG cut at {today}")
    return 0


def cmd_check(args) -> int:
    version = check(expect=args.expect)
    print(f"version sites agree: {version} (PyPI {pep440(version)})")
    return 0


def cmd_pep440(args) -> int:
    print(pep440(args.version))
    return 0


def cmd_notes(args) -> int:
    body = changelog_section(validate(args.version))
    if body is None:
        raise VersionError(f"CHANGELOG.md has no '## [{args.version}]' section")
    print(body)
    return 0


def cmd_go_modules(_args) -> int:
    for module in GO_MODULES:
        print(module)
    return 0


def main(argv) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("current").set_defaults(func=cmd_current)
    p = sub.add_parser("set")
    p.add_argument("version")
    p.add_argument("--date", help="changelog date (YYYY-MM-DD); default today")
    p.set_defaults(func=cmd_set)
    p = sub.add_parser("check")
    p.add_argument("--expect")
    p.set_defaults(func=cmd_check)
    p = sub.add_parser("pep440")
    p.add_argument("version")
    p.set_defaults(func=cmd_pep440)
    p = sub.add_parser("notes")
    p.add_argument("version")
    p.set_defaults(func=cmd_notes)
    sub.add_parser("go-modules").set_defaults(func=cmd_go_modules)
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except VersionError as err:
        print(f"error: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
