#!/bin/sh
# install.sh: build kiln from source and install it.
#
#   curl -fsSL https://raw.githubusercontent.com/6cclab/kiln/main/scripts/install.sh | sh
#   sh scripts/install.sh --source .          # from a local checkout
#
# Options (or the environment variable in brackets):
#   --ref REF        branch, tag or commit to build [KILN_REF, default main]
#   --dir DIR        where to install the kiln binary [KILN_INSTALL_DIR,
#                    default ~/.local/bin]
#   --source DIR     build this local checkout instead of cloning [KILN_SOURCE]
#   --repo URL       repository to clone [KILN_REPO,
#                    default https://github.com/6cclab/kiln.git]
#
# Needs git and Go (the version go.mod names, or newer). Never uses sudo.
# The binary is copied next to the target and renamed into place, so a
# kiln that is already running keeps its old binary.
set -eu

REF=${KILN_REF:-main}
INSTALL_DIR=${KILN_INSTALL_DIR:-$HOME/.local/bin}
SOURCE=${KILN_SOURCE:-}
REPO=${KILN_REPO:-https://github.com/6cclab/kiln.git}
MODULE=github.com/andrepato/harness

say() { printf 'kiln install: %s\n' "$*"; }
die() { printf 'kiln install: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case $1 in
	--ref) [ $# -ge 2 ] || die "--ref needs a value"; REF=$2; shift 2 ;;
	--dir) [ $# -ge 2 ] || die "--dir needs a value"; INSTALL_DIR=$2; shift 2 ;;
	--source) [ $# -ge 2 ] || die "--source needs a value"; SOURCE=$2; shift 2 ;;
	--repo) [ $# -ge 2 ] || die "--repo needs a value"; REPO=$2; shift 2 ;;
	-h | --help)
		echo "usage: install.sh [--ref REF] [--dir DIR] [--source DIR] [--repo URL]"
		echo "Builds kiln from source and installs it (default ~/.local/bin). See the script header."
		exit 0 ;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
done

command -v go >/dev/null 2>&1 || die "Go is not installed: get it from https://go.dev/dl/"
[ -n "$SOURCE" ] || command -v git >/dev/null 2>&1 || die "git is not installed"

# version_ge A B: true when Go version A (1.26.3) is at least B.
version_ge() {
	printf '%s\n%s\n' "$2" "$1" | sort -t. -k1,1n -k2,2n -k3,3n -C
}

WORK=$(mktemp -d 2>/dev/null || mktemp -d -t kiln-install)
trap 'rm -rf "$WORK"' EXIT INT TERM

if [ -n "$SOURCE" ]; then
	SRC=$(cd "$SOURCE" && pwd) || die "no such directory: $SOURCE"
	say "building from $SRC"
else
	SRC=$WORK/src
	say "cloning $REPO ($REF)"
	if ! git clone --quiet --depth 1 --branch "$REF" "$REPO" "$SRC" 2>/dev/null; then
		# Not a branch or tag: a commit. A full SHA can be fetched alone;
		# a short one needs the history to resolve it.
		rm -rf "$SRC"
		git init --quiet "$SRC"
		git -C "$SRC" remote add origin "$REPO"
		if git -C "$SRC" fetch --quiet --depth 1 origin "$REF" 2>/dev/null; then
			git -C "$SRC" checkout --quiet FETCH_HEAD
		else
			git -C "$SRC" fetch --quiet origin || die "cannot fetch from $REPO"
			git -C "$SRC" checkout --quiet "$REF" 2>/dev/null || die "no branch, tag or commit $REF in $REPO"
		fi
	fi
fi

if ! grep -q "^module $MODULE\$" "$SRC/go.mod" 2>/dev/null; then
	die "$SRC is not a kiln checkout (no go.mod for $MODULE)"
fi

need=$(sed -n 's/^go \([0-9][0-9.]*\).*/\1/p' "$SRC/go.mod" | head -n 1)
have=$(go env GOVERSION | sed 's/^go//; s/[^0-9.].*//')
if [ -n "$need" ] && ! version_ge "$have" "$need"; then
	die "kiln needs Go $need or newer; this is Go $have (https://go.dev/dl/)"
fi

VERSION=$(git -C "$SRC" describe --tags --always --dirty 2>/dev/null || echo dev)
say "building kiln $VERSION"
(cd "$SRC" && go build -trimpath -ldflags "-X $MODULE/internal/cli.Version=$VERSION" -o "$WORK/bin/kiln" ./cmd/kiln) ||
	die "build failed"

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
cp "$WORK/bin/kiln" "$INSTALL_DIR/.kiln.tmp.$$"
chmod 755 "$INSTALL_DIR/.kiln.tmp.$$"
mv -f "$INSTALL_DIR/.kiln.tmp.$$" "$INSTALL_DIR/kiln"
say "installed $INSTALL_DIR/kiln ($VERSION)"

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
