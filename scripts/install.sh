#!/bin/sh
# install.sh: install a prebuilt kiln release, or build it from source.
#
#   curl -fsSL https://raw.githubusercontent.com/6cclab/kiln/main/scripts/install.sh | sh
#   sh scripts/install.sh --source .          # from a local checkout
#
# By default this installs the latest GitHub release's binary for your
# platform, verified against its published checksum. It builds from source
# instead when: --source or --from-source is given, --ref names a branch
# or commit rather than a version tag (vX.Y.Z), or there is no release, or
# no release asset for your OS/arch — building from source then needs Go
# (the version go.mod names, or newer) and git.
#
# Options (or the environment variable in brackets):
#   --ref REF        version tag to install, or branch/commit to build
#                    [KILN_REF, default: the latest release]
#   --dir DIR        where to install the kiln binary [KILN_INSTALL_DIR,
#                    default ~/.local/bin]
#   --source DIR     build this local checkout instead of installing a
#                    release [KILN_SOURCE]
#   --from-source    build from source even though a release could be
#                    installed [KILN_FROM_SOURCE=1]
#   --repo URL       repository to clone when building from source
#                    [KILN_REPO, default https://github.com/6cclab/kiln.git]
#
# Never uses sudo. The binary is copied next to the target and renamed
# into place, so a kiln that is already running keeps its old binary. A
# release's checksum is always verified before it is installed; a mismatch
# aborts rather than falling back to building from source.
set -eu

REF=${KILN_REF:-}
INSTALL_DIR=${KILN_INSTALL_DIR:-$HOME/.local/bin}
SOURCE=${KILN_SOURCE:-}
FROM_SOURCE=${KILN_FROM_SOURCE:-}
REPO=${KILN_REPO:-https://github.com/6cclab/kiln.git}
MODULE=github.com/andrepato/harness
# KILN_RELEASE_BASE overrides where release assets come from: normally
# https://github.com/6cclab/kiln/releases/download/<tag>, computed once a
# tag is known. Pointed instead at a plain directory (a file:// URL or a
# test HTTP server) holding "<archive>" and "checksums.txt" directly, with
# no per-version subpath, no GitHub API call, and no "latest" resolution:
# tests use it with an explicit --ref. Not meant for everyday use.
RELEASE_BASE=${KILN_RELEASE_BASE:-}
RELEASE_API=${KILN_RELEASE_API:-https://api.github.com/repos/6cclab/kiln/releases/latest}

say() { printf 'kiln install: %s\n' "$*"; }
die() { printf 'kiln install: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case $1 in
	--ref) [ $# -ge 2 ] || die "--ref needs a value"; REF=$2; shift 2 ;;
	--dir) [ $# -ge 2 ] || die "--dir needs a value"; INSTALL_DIR=$2; shift 2 ;;
	--source) [ $# -ge 2 ] || die "--source needs a value"; SOURCE=$2; shift 2 ;;
	--from-source) FROM_SOURCE=1; shift ;;
	--repo) [ $# -ge 2 ] || die "--repo needs a value"; REPO=$2; shift 2 ;;
	-h | --help)
		echo "usage: install.sh [--ref REF] [--dir DIR] [--source DIR] [--from-source] [--repo URL]"
		echo "Installs a release of kiln, or builds it from source. See the script header."
		exit 0 ;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
done

WORK=$(mktemp -d 2>/dev/null || mktemp -d -t kiln-install)
trap 'rm -rf "$WORK"' EXIT INT TERM

# atomic_install BIN VERSION: copies BIN next to $INSTALL_DIR/kiln and
# renames it into place, so a kiln that is already running (reading its
# old inode) keeps working, then reports where it landed.
atomic_install() {
	mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
	cp "$1" "$INSTALL_DIR/.kiln.tmp.$$"
	chmod 755 "$INSTALL_DIR/.kiln.tmp.$$"
	mv -f "$INSTALL_DIR/.kiln.tmp.$$" "$INSTALL_DIR/kiln"
	say "installed $INSTALL_DIR/kiln ($2)"
}

# looks_like_version_tag REF: true for vX.Y(.Z...), the shape a release
# tag has. Anything else (a branch name, a short or full commit) is built
# from source instead of looked up as a release.
looks_like_version_tag() {
	case $1 in
	v[0-9]*.[0-9]*) return 0 ;;
	*) return 1 ;;
	esac
}

# download URL DEST: fetches URL to DEST with curl, falling back to wget.
# Fails (non-zero) on a 404 or other HTTP error, which release lookups
# below rely on to detect "no such asset" without parsing output.
download() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -q "$1" -O "$2"
	else
		die "need curl or wget to install a release"
	fi
}

# sha256_of FILE: prints FILE's sha256 using whichever tool is on PATH.
sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "need sha256sum or shasum to verify a release"
	fi
}

# release_platform: prints "GOOS GOARCH" for this machine in the spelling
# release.yml names archives with, or nothing if either axis is
# unrecognized (the caller then falls back to building from source).
release_platform() {
	goos=
	case $(uname -s) in
	Darwin) goos=darwin ;;
	Linux) goos=linux ;;
	MINGW* | MSYS* | CYGWIN*) goos=windows ;;
	esac
	goarch=
	case $(uname -m) in
	x86_64 | amd64) goarch=amd64 ;;
	arm64 | aarch64) goarch=arm64 ;;
	esac
	[ -n "$goos" ] && [ -n "$goarch" ] && printf '%s %s\n' "$goos" "$goarch"
}

# fatal_release MSG: prints like die, but returns instead of exiting.
# try_release uses this (then `return 2`) for anything that must abort the
# whole install rather than fall back to building from source — a
# checksum mismatch above all. die's plain `exit` would also work today,
# since try_release is always called directly rather than through
# `$(...)` (which would turn exit into "just end the subshell" and the
# caller would wrongly treat it as "no release, fall back"), but a
# dedicated return code means that stays true even if a future change
# wraps the call differently.
fatal_release() {
	printf 'kiln install: %s\n' "$*" >&2
}

# try_release: attempts the prebuilt-release path. On success, sets
# rel_bin and rel_version and returns 0. Returns 1 when there is no
# release (or none for this platform): the caller then builds from
# source. Returns 2 on a checksum mismatch or other corrupt/incomplete
# release data; the caller must treat that as fatal and exit, never fall
# back to building from source, since this found a real release asset
# that failed to verify.
try_release() {
	rel_bin=
	rel_version=
	platform=$(release_platform) || true
	[ -n "$platform" ] || return 1
	goos=${platform% *}
	goarch=${platform#* }

	version=$REF
	if [ -z "$version" ]; then
		if [ -n "$RELEASE_BASE" ]; then
			return 1 # no API to ask "latest" against a plain asset directory
		fi
		say "looking up the latest release"
		api_json=$WORK/release.json
		download "$RELEASE_API" "$api_json" 2>/dev/null || return 1
		version=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$api_json" | head -n 1)
		[ -n "$version" ] || return 1
	fi

	ext=.tar.gz
	[ "$goos" = windows ] && ext=.zip
	archive="kiln_${version}_${goos}_${goarch}${ext}"
	base=${RELEASE_BASE:-"https://github.com/6cclab/kiln/releases/download/$version"}

	say "fetching $archive ($version)"
	archive_path=$WORK/$archive
	download "$base/$archive" "$archive_path" 2>/dev/null || return 1

	checksums_path=$WORK/checksums.txt
	if ! download "$base/checksums.txt" "$checksums_path" 2>/dev/null; then
		fatal_release "fetched $archive but not its checksums.txt; refusing to install it unverified"
		return 2
	fi

	want=$(awk -v f="$archive" '$2 == f || $2 == "*"f {print $1}' "$checksums_path" | head -n 1)
	if [ -z "$want" ]; then
		fatal_release "checksums.txt has no entry for $archive"
		return 2
	fi
	got=$(sha256_of "$archive_path")
	if [ "$got" != "$want" ]; then
		fatal_release "checksum mismatch for $archive: got $got, want $want"
		return 2
	fi

	extract_dir=$WORK/extracted
	mkdir -p "$extract_dir"
	case $ext in
	.zip)
		if ! command -v unzip >/dev/null 2>&1; then
			fatal_release "need unzip to install a Windows release"
			return 2
		fi
		if ! unzip -q "$archive_path" -d "$extract_dir"; then
			fatal_release "could not extract $archive"
			return 2
		fi
		bin=$(find "$extract_dir" -name 'kiln.exe' | head -n 1)
		;;
	*)
		if ! tar -xzf "$archive_path" -C "$extract_dir"; then
			fatal_release "could not extract $archive"
			return 2
		fi
		bin=$(find "$extract_dir" -name 'kiln' -type f | head -n 1)
		;;
	esac
	if [ -z "$bin" ] || [ ! -f "$bin" ]; then
		fatal_release "$archive did not contain a kiln binary"
		return 2
	fi

	rel_bin=$bin
	rel_version=$version
}

# build_from_source REF: clones (or reuses --source) REF, builds it, and
# sets src_bin/src_version — not printed to stdout, since `say` below
# writes progress there too and a caller capturing this through `$(...)`
# would catch that as well as the result.
build_from_source() {
	ref=$1
	command -v go >/dev/null 2>&1 || die "Go is not installed: get it from https://go.dev/dl/"
	[ -n "$SOURCE" ] || command -v git >/dev/null 2>&1 || die "git is not installed"

	if [ -n "$SOURCE" ]; then
		src=$(cd "$SOURCE" && pwd) || die "no such directory: $SOURCE"
		say "building from $src"
	else
		src=$WORK/src
		say "cloning $REPO ($ref)"
		if ! git clone --quiet --depth 1 --branch "$ref" "$REPO" "$src" 2>/dev/null; then
			# Not a branch or tag: a commit. A full SHA can be fetched alone;
			# a short one needs the history to resolve it.
			rm -rf "$src"
			git init --quiet "$src"
			git -C "$src" remote add origin "$REPO"
			if git -C "$src" fetch --quiet --depth 1 origin "$ref" 2>/dev/null; then
				git -C "$src" checkout --quiet FETCH_HEAD
			else
				git -C "$src" fetch --quiet origin || die "cannot fetch from $REPO"
				git -C "$src" checkout --quiet "$ref" 2>/dev/null || die "no branch, tag or commit $ref in $REPO"
			fi
		fi
	fi

	if ! grep -q "^module $MODULE\$" "$src/go.mod" 2>/dev/null; then
		die "$src is not a kiln checkout (no go.mod for $MODULE)"
	fi

	need=$(sed -n 's/^go \([0-9][0-9.]*\).*/\1/p' "$src/go.mod" | head -n 1)
	have=$(go env GOVERSION | sed 's/^go//; s/[^0-9.].*//')
	if [ -n "$need" ] && ! version_ge "$have" "$need"; then
		die "kiln needs Go $need or newer; this is Go $have (https://go.dev/dl/)"
	fi

	version=$(git -C "$src" describe --tags --always --dirty 2>/dev/null || echo dev)
	say "building kiln $version"
	(cd "$src" && go build -trimpath -ldflags "-X $MODULE/internal/cli.Version=$version" -o "$WORK/bin/kiln" ./cmd/kiln) ||
		die "build failed"

	src_bin=$WORK/bin/kiln
	src_version=$version
}

# version_ge A B: true when Go version A (1.26.3) is at least B.
version_ge() {
	printf '%s\n%s\n' "$2" "$1" | sort -t. -k1,1n -k2,2n -k3,3n -C
}

rel_bin=
rel_version=
if [ -z "$SOURCE" ] && [ -z "$FROM_SOURCE" ] && { [ -z "$REF" ] || looks_like_version_tag "$REF"; }; then
	# Called directly, not through `$(...)`: try_release sets rel_bin/
	# rel_version itself rather than printing them, and a fatal error
	# inside it (rc 2) must reach this `case`, not be swallowed by a
	# subshell. The `|| rc=$?` is only to stop `set -e` from exiting on
	# try_release's own non-zero "no release here" return (1).
	rc=0
	try_release || rc=$?
	case $rc in
	0) ;;
	2) exit 1 ;; # try_release already printed why
	*) ;;        # no release for this platform/ref: fall back below
	esac
fi
if [ -n "$rel_bin" ]; then
	bin=$rel_bin
	version=$rel_version
else
	src_bin=
	src_version=
	build_from_source "${REF:-main}"
	bin=$src_bin
	version=$src_version
fi

atomic_install "$bin" "$version"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) say "$INSTALL_DIR is not on your PATH; add this to your shell profile:"
	# shellcheck disable=SC2016 # $PATH is meant literally here
	printf '    export PATH="%s:$PATH"\n' "$INSTALL_DIR" ;;
esac
other=$(command -v kiln 2>/dev/null || true)
if [ -n "$other" ] && [ "$other" != "$INSTALL_DIR/kiln" ]; then
	say "note: \`kiln\` on your PATH is currently $other, not the copy just installed"
fi
