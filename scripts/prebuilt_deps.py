#!/usr/bin/env python3
"""The prebuilt dependencies a checkout without Rust stages (D82).

Two families of build products come from Rust, and a checkout builds them
from source with the Rust toolchain tools.env pins:

  superscalar   superscalar's Go binding with its static archive, which the
                CLI and every Go build link (scripts/superscalar-dep.sh),
                and its TypeScript binding, with the napi addon Bun and
                Node load and the browser and workerd builds over its
                WebAssembly core, which the generated TypeScript packages
                and a site's bundle take. Keyed by the commit in
                superscalar.pin, RUST_VERSION and WASM_PACK_VERSION.
  versiongraph  the version-graph core's static archive, which a Go binary
                over the version graph links (scripts/versiongraph-archive.sh),
                and its wasm32 module, which @superschematic/versiongraph
                ships (runtime/versiongraph/typescript). Keyed by the
                digest of the core's sources and RUST_VERSION.

The prebuilt-deps workflow (.github/workflows/prebuilt-deps.yml) builds
each family for linux-x64, linux-arm64, darwin-arm64 and darwin-x64 and
attaches its tarballs and their manifest.json to a GitHub prerelease
tagged prebuilt-<family>-<key>. prebuilt.pin records each family's key
and the SHA-256 of its manifest. `fetch` downloads a family's manifest,
refuses one whose digest is not the pinned one or whose key is not the
checkout's, downloads the tarballs this host needs, refuses one whose
digest or size the manifest does not list, and stages their files where
the build expects them, as a build from source leaves them.

Usage:
  scripts/prebuilt_deps.py key <family>              # the checkout's key
  scripts/prebuilt_deps.py tag <family>              # the release's tag
  scripts/prebuilt_deps.py pack <platform> <out> [--wasm]
  scripts/prebuilt_deps.py manifest <out> <family> [--platforms p,...]
  scripts/prebuilt_deps.py fetch <family> [--only <artifact>,...]
  scripts/prebuilt_deps.py pin <family>...

pack packs this host's built products under <out>/<tag>/, after a build
from source (scripts/superscalar-dep.sh, scripts/versiongraph-archive.sh,
and with --wasm the core's wasm32 build). manifest writes
<out>/<tag>/manifest.json over the tarballs of every platform there. pin
writes prebuilt.pin from the published manifests.

Environment:
  SUPERSCHEMATIC_PREBUILT_URL      where the releases are: a URL (https://
                                   or file://) or a directory, holding
                                   <tag>/<file>; unset is this
                                   repository's GitHub releases
  SUPERSCHEMATIC_PREBUILT_REPO     the GitHub repository of the releases,
                                   parable-work/superschematic unless set
  SUPERSCHEMATIC_PREBUILT_<FAMILY>_MANIFEST_SHA256
                                   the manifest digest to trust in place of
                                   prebuilt.pin's, as for a release just
                                   published or a local one
  SUPERSCHEMATIC_PREBUILT_PLATFORM the platform to fetch for; unset is
                                   this host's, from go env
  GITHUB_TOKEN                     sent to github.com, for a private
                                   repository

Needs python3 alone, or the GitHub CLI (gh) for a private repository's
releases when GITHUB_TOKEN is unset.
"""

import hashlib
import json
import os
import platform as host_platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path, PurePosixPath
from typing import Callable, List, Optional

ROOT = Path(__file__).resolve().parent.parent
PIN = ROOT / "prebuilt.pin"
DEFAULT_REPO = "parable-work/superschematic"

# The release's platforms, by their names in release-archives.sh, with Go's
# GOOS_GOARCH and the napi addon's suffix for each.
PLATFORMS = {
    "linux-x64": ("linux_amd64", "linux-x64-gnu"),
    "linux-arm64": ("linux_arm64", "linux-arm64-gnu"),
    "darwin-arm64": ("darwin_arm64", "darwin-arm64"),
    "darwin-x64": ("darwin_amd64", "darwin-x64"),
}


class PrebuiltError(Exception):
    """A refusal, said in one line."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def tools_env(name: str) -> str:
    for line in (ROOT / "tools.env").read_text().splitlines():
        if line.startswith(name + "="):
            return line.split("=", 1)[1].strip()
    raise PrebuiltError(f"tools.env has no {name}= line")


def superscalar_commit() -> str:
    for line in (ROOT / "superscalar.pin").read_text().splitlines():
        if line.startswith("commit="):
            return line.split("=", 1)[1].strip()
    raise PrebuiltError("superscalar.pin has no commit= line")


def tree_digest(base: Path, members: List[str]) -> str:
    """The SHA-256 of the files under base that members name, files or
    directories, each path and content in order, so any edit, addition or
    removal changes it. In a git checkout the files are the ones git does
    not ignore, so a build's output or an editor's file changes nothing."""
    names: List[str] = []
    listed = subprocess.run(
        ["git", "-C", str(base), "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", *members],
        capture_output=True,
    )
    if listed.returncode == 0:
        names = [n for n in listed.stdout.decode().split("\0") if n and (base / n).is_file()]
    else:
        for member in members:
            path = base / member
            if path.is_file():
                names.append(member)
            elif path.is_dir():
                names.extend(p.relative_to(base).as_posix() for p in path.rglob("*") if p.is_file())
    digest = hashlib.sha256()
    for name in sorted(set(names)):
        digest.update(name.encode() + b"\0")
        digest.update(sha256_file(base / name).encode() + b"\n")
    return digest.hexdigest()


# --- the families ---


@dataclass
class Artifact:
    """One tarball of a family. base is the directory, relative to the
    repository, its files are relative to once staged. pack lists the files
    and directories `pack` puts in it from a built tree; allowed, the paths
    its files must be under, which `fetch` refuses any other file outside;
    replace, the build outputs `fetch` removes before it stages the new
    ones, so no file of an earlier build stays beside them; required, the
    files whose presence, with the stamp, means it is staged; stamp, where
    `fetch` records the digest of the tarball it staged. Each takes the
    platform."""

    name: str
    base: str
    per_platform: bool
    pack: Callable[[str], List[str]]
    allowed: Callable[[str], List[str]]
    replace: Callable[[str], List[str]]
    required: Callable[[str], List[str]]
    stamp: str

    def file(self, platform: Optional[str]) -> str:
        return f"{self.name}_{platform}.tar.gz" if self.per_platform else f"{self.name}.tar.gz"


def goos_goarch(platform: str) -> str:
    return PLATFORMS[platform][0]


def superscalar_go_pack(platform: str) -> List[str]:
    go = ROOT / "third_party/superscalar/go"
    sources = sorted(
        "go/" + p.name for p in go.glob("*.go") if not p.name.endswith("_test.go") and not p.name.startswith("archive_stamp_")
    )
    return ["LICENSE", "go/go.mod", "go/go.sum", "go/include", *sources, f"go/lib/{goos_goarch(platform)}/libsuperscalar_ffi.a"]


TS = "bindings/typescript"


def superscalar_typescript_pack(platform: str) -> List[str]:
    napi = PLATFORMS[platform][1]
    return ["LICENSE", f"{TS}/package.json", f"{TS}/src", f"{TS}/dist", f"{TS}/native/index.js", f"{TS}/native/index.d.ts", f"{TS}/native/superscalar-napi.{napi}.node"]


@dataclass
class Family:
    name: str
    inputs: Callable[[], dict]
    artifacts: List[Artifact] = field(default_factory=list)

    def key(self) -> str:
        canonical = json.dumps({"family": self.name, **self.inputs()}, sort_keys=True, separators=(",", ":"))
        return hashlib.sha256(canonical.encode()).hexdigest()[:16]

    def tag(self) -> str:
        return f"prebuilt-{self.name}-{self.key()}"

    def artifact(self, name: str) -> Artifact:
        for a in self.artifacts:
            if a.name == name:
                return a
        raise PrebuiltError(f"{self.name} has no artifact {name}; it has {', '.join(a.name for a in self.artifacts)}")


VERSIONGRAPH_SOURCES = ["Cargo.toml", "Cargo.lock", "src"]
WASM = "superschematic_versiongraph.wasm"

FAMILIES = {
    "superscalar": Family(
        "superscalar",
        lambda: {
            "superscalar": superscalar_commit(),
            "rust": tools_env("RUST_VERSION"),
            "wasmPack": tools_env("WASM_PACK_VERSION"),
        },
        [
            # The Go binding's sources at the pinned commit and its archive,
            # where the binding's cgo flags and scripts/superscalar-dep.sh
            # --print look for it.
            Artifact(
                "superscalar-go", "third_party/superscalar", True, superscalar_go_pack,
                allowed=lambda p: ["LICENSE", "go"],
                replace=lambda p: [f"go/lib/{goos_goarch(p)}"],
                required=lambda p: ["go/go.mod", f"go/lib/{goos_goarch(p)}/libsuperscalar_ffi.a"],
                stamp=".prebuilt-superscalar-go",
            ),
            # The TypeScript binding: its sources, which a schema's
            # tsconfig resolves superscalar to, and its builds, the napi addon
            # for the platform among them.
            Artifact(
                "superscalar-typescript", "third_party/superscalar", True, superscalar_typescript_pack,
                allowed=lambda p: ["LICENSE", TS],
                replace=lambda p: [f"{TS}/dist", f"{TS}/native"],
                required=lambda p: [f"{TS}/package.json", f"{TS}/src/index.ts", f"{TS}/dist/esm/validation.js", f"{TS}/dist/browser/backend.js", f"{TS}/dist/workerd/backend.js", f"{TS}/native/index.js"],
                stamp=".prebuilt-superscalar-typescript",
            ),
        ],
    ),
    "versiongraph": Family(
        "versiongraph",
        lambda: {
            "sources": tree_digest(ROOT / "runtime/versiongraph/rust", VERSIONGRAPH_SOURCES),
            "rust": tools_env("RUST_VERSION"),
        },
        [
            Artifact(
                "versiongraph-archive", "runtime/versiongraph/go", True,
                lambda p: [f"lib/{goos_goarch(p)}/libsuperschematic_versiongraph.a"],
                allowed=lambda p: [f"lib/{goos_goarch(p)}"],
                replace=lambda p: [f"lib/{goos_goarch(p)}"],
                required=lambda p: [f"lib/{goos_goarch(p)}/libsuperschematic_versiongraph.a"],
                stamp="lib/.prebuilt-versiongraph-archive",
            ),
            # Built where cargo writes it; staged where build-wasm.mjs reads a
            # prebuilt module.
            Artifact(
                "versiongraph-wasm", "runtime/versiongraph/typescript/prebuilt", False,
                lambda p: [WASM], allowed=lambda p: [WASM], replace=lambda p: [], required=lambda p: [WASM],
                stamp=".prebuilt-versiongraph-wasm",
            ),
        ],
    ),
}

# Where `pack` reads an artifact's files when the build leaves them
# somewhere other than where `fetch` stages them.
PACK_SOURCES = {"versiongraph-wasm": "runtime/versiongraph/rust/target/wasm32-unknown-unknown/release"}


def family(name: str) -> Family:
    if name not in FAMILIES:
        raise PrebuiltError(f"no family {name}; the families are {', '.join(FAMILIES)}")
    return FAMILIES[name]


def host() -> str:
    """This host's platform: SUPERSCHEMATIC_PREBUILT_PLATFORM, or the one go
    env names, or Python's reading of the machine."""
    if os.environ.get("SUPERSCHEMATIC_PREBUILT_PLATFORM"):
        name = os.environ["SUPERSCHEMATIC_PREBUILT_PLATFORM"]
        if name not in PLATFORMS:
            raise PrebuiltError(f"SUPERSCHEMATIC_PREBUILT_PLATFORM is {name}; the platforms are {', '.join(PLATFORMS)}")
        return name
    try:
        out = subprocess.run(["go", "env", "GOOS", "GOARCH"], capture_output=True, text=True, check=True).stdout.split()
        goos_goarch = "_".join(out)
    except (OSError, subprocess.CalledProcessError):
        machine = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(host_platform.machine().lower(), "")
        goos_goarch = f"{host_platform.system().lower()}_{machine}"
    for name, (go, _) in PLATFORMS.items():
        if go == goos_goarch:
            return name
    raise PrebuiltError(f"no prebuilt dependencies are built for {goos_goarch}; the platforms are {', '.join(PLATFORMS)}")


# --- pack and manifest ---


def pack(platform: str, out: Path, wasm: bool) -> List[Path]:
    """Packs this host's built products for platform under out/<tag>/."""
    if platform not in PLATFORMS:
        raise PrebuiltError(f"no platform {platform}; the platforms are {', '.join(PLATFORMS)}")
    written = []
    for fam in FAMILIES.values():
        dest = out / fam.tag()
        dest.mkdir(parents=True, exist_ok=True)
        for art in fam.artifacts:
            if not art.per_platform and not wasm:
                continue
            source = ROOT / PACK_SOURCES.get(art.name, art.base)
            tarball = dest / art.file(platform if art.per_platform else None)
            write_tarball(tarball, source, art.pack(platform))
            written.append(tarball)
    return written


def write_tarball(tarball: Path, base: Path, members: List[str]) -> None:
    """A gzipped tar of members of base, regular files and directories only,
    their owners and times zeroed, so the tarball holds the files' bytes and
    modes alone."""

    def clean(info: tarfile.TarInfo) -> tarfile.TarInfo:
        info.uid = info.gid = 0
        info.uname = info.gname = ""
        info.mtime = 0
        return info

    with tarfile.open(tarball, "w:gz") as tar:
        for member in members:
            path = base / member
            if not path.exists():
                raise PrebuiltError(f"{path} is missing; build it from source before packing {tarball.name}")
            files = [path] if path.is_file() else sorted(p for p in path.rglob("*") if p.is_file())
            for f in files:
                if f.is_symlink():
                    raise PrebuiltError(f"{f} is a symbolic link; a tarball holds regular files alone")
                tar.add(f, arcname=f.relative_to(base).as_posix(), recursive=False, filter=clean)


def write_manifest(out: Path, fam: Family, platforms: List[str]) -> Path:
    """Writes out/<tag>/manifest.json over the family's tarballs there, one per
    platform for a per-platform artifact, and refuses a missing one."""
    dest = out / fam.tag()
    entries = []
    for art in fam.artifacts:
        for platform in platforms if art.per_platform else [None]:
            tarball = dest / art.file(platform)
            if not tarball.is_file():
                raise PrebuiltError(f"{tarball} is missing; pack it on a {platform or 'linux-x64'} runner first")
            entries.append({"file": tarball.name, "sha256": sha256_file(tarball), "size": tarball.stat().st_size})
    manifest = {
        "family": fam.name,
        "key": fam.key(),
        "inputs": fam.inputs(),
        "commit": os.environ.get("GITHUB_SHA") or git_head(),
        "platforms": platforms,
        "artifacts": entries,
    }
    path = dest / "manifest.json"
    path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    return path


def git_head() -> str:
    try:
        return subprocess.run(["git", "-C", str(ROOT), "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


# --- download ---


def release_url(tag: str, file: str) -> str:
    base = os.environ.get("SUPERSCHEMATIC_PREBUILT_URL", "")
    if not base:
        repo = os.environ.get("SUPERSCHEMATIC_PREBUILT_REPO", DEFAULT_REPO)
        return f"https://github.com/{repo}/releases/download/{tag}/{file}"
    if "://" not in base:
        base = Path(base).resolve().as_uri()
    return base.rstrip("/") + f"/{tag}/{file}"


def download(tag: str, file: str, dest: Path) -> None:
    url = release_url(tag, file)
    gh = shutil.which("gh")
    if url.startswith("https://github.com/") and gh and not os.environ.get("GITHUB_TOKEN"):
        repo = os.environ.get("SUPERSCHEMATIC_PREBUILT_REPO", DEFAULT_REPO)
        result = subprocess.run([gh, "release", "download", tag, "--repo", repo, "--pattern", file, "--dir", str(dest.parent), "--clobber"], capture_output=True, text=True)
        if result.returncode != 0:
            raise PrebuiltError(f"gh release download {tag} {file}: {result.stderr.strip()}")
        return
    request = urllib.request.Request(url)
    if url.startswith("https://github.com/") and os.environ.get("GITHUB_TOKEN"):
        request.add_header("Authorization", "Bearer " + os.environ["GITHUB_TOKEN"])
    try:
        with urllib.request.urlopen(request, timeout=300) as response, open(dest, "wb") as f:
            shutil.copyfileobj(response, f)
    except (OSError, ValueError) as error:
        raise PrebuiltError(f"download {url}: {error}") from error


# --- pin and fetch ---


def read_pin() -> dict:
    values = {}
    if PIN.is_file():
        for line in PIN.read_text().splitlines():
            if "=" in line and not line.lstrip().startswith("#"):
                key, value = line.split("=", 1)
                values[key.strip()] = value.strip()
    return values


def pinned_manifest_digest(fam: Family) -> str:
    override = os.environ.get(f"SUPERSCHEMATIC_PREBUILT_{fam.name.upper()}_MANIFEST_SHA256", "")
    if override:
        return override
    pin = read_pin()
    key, digest = pin.get(f"{fam.name}_key", ""), pin.get(f"{fam.name}_manifest_sha256", "")
    if not key or not digest:
        raise PrebuiltError(
            f"prebuilt.pin pins no prebuilt {fam.name} yet; publish them with the prebuilt-deps workflow and "
            f"write prebuilt.pin with scripts/prebuilt_deps.py pin {fam.name}, or build from source with Rust (make setup)"
        )
    if key != fam.key():
        raise PrebuiltError(
            f"prebuilt.pin pins prebuilt {fam.name} for key {key}, but this checkout's inputs give {fam.key()} "
            f"({json.dumps(fam.inputs(), sort_keys=True)}); run the prebuilt-deps workflow for this commit and "
            f"update prebuilt.pin (scripts/prebuilt_deps.py pin {fam.name}), or build from source with Rust (make setup)"
        )
    return digest


def read_manifest(fam: Family, work: Path) -> dict:
    """Downloads the family's manifest for this checkout's key and returns it,
    refusing one whose digest is not the pinned one or that names another
    family, key or set of inputs."""
    want = pinned_manifest_digest(fam)
    path = work / "manifest.json"
    download(fam.tag(), "manifest.json", path)
    got = sha256_file(path)
    if got != want:
        raise PrebuiltError(f"{fam.tag()}/manifest.json has SHA-256 {got}, not the pinned {want}; refusing it")
    manifest = json.loads(path.read_text())
    if manifest.get("family") != fam.name or manifest.get("key") != fam.key() or manifest.get("inputs") != fam.inputs():
        raise PrebuiltError(f"{fam.tag()}/manifest.json is for {manifest.get('family')} key {manifest.get('key')}, not {fam.name} key {fam.key()}; refusing it")
    return manifest


def safe_extract(tarball: Path, dest: Path, allowed: List[str]) -> List[str]:
    """Extracts the regular files and directories of tarball into dest, each
    file under one of the allowed paths, and refuses anything else: a link,
    a device, an absolute path, one that leaves dest, or a file elsewhere.
    It returns the files' paths."""
    names = []
    with tarfile.open(tarball, "r:gz") as tar:
        for info in tar.getmembers():
            path = PurePosixPath(info.name)
            if path.is_absolute() or ".." in path.parts or not (info.isfile() or info.isdir()):
                raise PrebuiltError(f"{tarball.name} holds {info.name!r}, which is no plain file inside it; refusing it")
            if info.isfile() and not any(str(path) == a or str(path).startswith(a + "/") for a in allowed):
                raise PrebuiltError(f"{tarball.name} holds {info.name}, which is outside {', '.join(allowed)}; refusing it")
            target = dest / path
            if info.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            with tar.extractfile(info) as source, open(target, "wb") as f:
                shutil.copyfileobj(source, f)
            target.chmod(0o755 if info.mode & 0o111 else 0o644)
            names.append(str(path))
    return names


def stage(extracted: Path, files: List[str], base: Path, replace: List[str]) -> None:
    """Stages the extracted files under base: each directory replace names is
    removed first, a build's output that the tarball holds whole, and then
    each file is copied over whatever is there."""
    for member in replace:
        target = base / member
        if target.is_dir() and not target.is_symlink():
            shutil.rmtree(target)
        elif target.exists() or target.is_symlink():
            target.unlink()
    for name in files:
        target = base / name
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.is_symlink() or target.is_dir():
            raise PrebuiltError(f"{target} is a link or a directory; remove it to stage the prebuilt file")
        shutil.copy2(extracted / name, target)


def stamp_versiongraph(platform: str) -> None:
    """Writes the archive's digest where scripts/versiongraph-archive.sh does,
    so Go's build cache relinks against a new archive."""
    pair = goos_goarch(platform)
    goos, goarch = pair.split("_")
    go = ROOT / "runtime/versiongraph/go"
    digest = sha256_file(go / f"lib/{pair}/libsuperschematic_versiongraph.a")
    stamp = go / f"archive_stamp_{pair}.go"
    text = (
        f"//go:build {goos} && {goarch}\n\npackage versiongraph\n\n"
        "// Code generated by scripts/versiongraph-archive.sh. DO NOT EDIT.\n//\n"
        "// The SHA-256 of the static archive this package links. It changes when the\n"
        "// archive changes, so the Go build cache cannot reuse a link against a stale\n// one.\n"
        f'const archiveSHA256 = "{digest}"\n'
    )
    if not stamp.is_file() or stamp.read_text() != text:
        stamp.write_text(text)


def fetch(fam: Family, only: Optional[List[str]]) -> List[str]:
    """Stages the family's artifacts for this host, or those only names, and
    returns what it staged. The stamp beside each records the digest of the
    tarball it came from, so a second fetch of the same one downloads
    nothing but the manifest."""
    platform = host()
    artifacts = [fam.artifact(name) for name in only] if only else fam.artifacts
    staged = []
    with tempfile.TemporaryDirectory(prefix="prebuilt-") as tmp:
        work = Path(tmp)
        manifest = read_manifest(fam, work)
        listed = {entry["file"]: entry for entry in manifest.get("artifacts", [])}
        for art in artifacts:
            file = art.file(platform if art.per_platform else None)
            base = ROOT / art.base
            stamp = base / art.stamp
            entry = listed.get(file)
            if entry is None:
                raise PrebuiltError(f"{fam.tag()}/manifest.json lists no {file}")
            if stamp.is_file() and stamp.read_text().strip() == entry["sha256"] and all((base / m).is_file() for m in art.required(platform)):
                staged.append(f"{file} (staged already)")
                continue
            tarball = work / file
            download(fam.tag(), file, tarball)
            got, size = sha256_file(tarball), tarball.stat().st_size
            if got != entry["sha256"] or size != entry["size"]:
                raise PrebuiltError(f"{fam.tag()}/{file} has SHA-256 {got} and {size} bytes, not the manifest's {entry['sha256']} and {entry['size']}; refusing it")
            extracted = work / (file + ".d")
            files = safe_extract(tarball, extracted, art.allowed(platform))
            missing = [m for m in art.required(platform) if m not in files]
            if missing:
                raise PrebuiltError(f"{fam.tag()}/{file} holds no {', '.join(missing)}; refusing it")
            stage(extracted, files, base, art.replace(platform))
            stamp.parent.mkdir(parents=True, exist_ok=True)
            stamp.write_text(entry["sha256"] + "\n")
            if art.name == "versiongraph-archive":
                stamp_versiongraph(platform)
            staged.append(f"{file} into {base}")
    return staged


def write_pin(names: List[str]) -> None:
    """Writes prebuilt.pin with each named family's key and the digest of the
    manifest published for it, keeping the other families' lines."""
    pin = read_pin()
    with tempfile.TemporaryDirectory(prefix="prebuilt-") as tmp:
        for name in names:
            fam = family(name)
            path = Path(tmp) / f"{name}-manifest.json"
            download(fam.tag(), "manifest.json", path)
            manifest = json.loads(path.read_text())
            if manifest.get("key") != fam.key() or manifest.get("inputs") != fam.inputs():
                raise PrebuiltError(f"{fam.tag()}/manifest.json is for key {manifest.get('key')}, not this checkout's {fam.key()}")
            pin[f"{name}_key"] = fam.key()
            pin[f"{name}_manifest_sha256"] = sha256_file(path)
    lines = [
        "# The prebuilt dependencies scripts/prebuilt_deps.py fetch stages in a",
        "# checkout without Rust (D82): for each family, the key of the inputs its",
        "# artifacts were built from and the SHA-256 of the manifest.json of the",
        "# prerelease prebuilt-<family>-<key>, which lists each tarball's digest.",
        "# The prebuilt-deps workflow publishes the prerelease;",
        "# `scripts/prebuilt_deps.py pin superscalar versiongraph` writes this file.",
    ]
    for name in FAMILIES:
        lines.append(f"{name}_key={pin.get(f'{name}_key', '')}")
        lines.append(f"{name}_manifest_sha256={pin.get(f'{name}_manifest_sha256', '')}")
    PIN.write_text("\n".join(lines) + "\n")


def main(argv: List[str]) -> int:
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__)
        return 0 if argv else 2
    command, args = argv[0], argv[1:]
    try:
        if command == "key" and len(args) == 1:
            print(family(args[0]).key())
        elif command == "tag" and len(args) == 1:
            print(family(args[0]).tag())
        elif command == "pack" and len(args) in (2, 3) and args[2:] in ([], ["--wasm"]):
            for path in pack(args[0], Path(args[1]), args[2:] == ["--wasm"]):
                print(path)
        elif command == "manifest" and len(args) in (2, 4):
            platforms = list(PLATFORMS)
            if len(args) == 4:
                if args[2] != "--platforms":
                    raise PrebuiltError("manifest takes --platforms <platform>,...")
                platforms = args[3].split(",")
            print(write_manifest(Path(args[0]), family(args[1]), platforms))
        elif command == "fetch" and len(args) in (1, 3):
            only = None
            if len(args) == 3:
                if args[1] != "--only":
                    raise PrebuiltError("fetch takes --only <artifact>,...")
                only = args[2].split(",")
            fam = family(args[0])
            for line in fetch(fam, only):
                print(f"prebuilt {fam.name}: staged {line}", file=sys.stderr)
        elif command == "pin" and args:
            write_pin(args)
            print(f"wrote {PIN}", file=sys.stderr)
        else:
            print(__doc__, file=sys.stderr)
            return 2
    except PrebuiltError as error:
        print(f"prebuilt_deps: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
