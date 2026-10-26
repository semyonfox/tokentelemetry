#!/usr/bin/env bash
# Cross-compiles release archives and checksums into dist/release, and with
# --publish uploads them as a GitHub release. Needs only Go, tar, zip and gh,
# so a release never depends on CI minutes.
#
#   engine/scripts/release.sh v0.1.0            build archives only
#   engine/scripts/release.sh v0.1.0 --publish  build, tag and publish
set -euo pipefail

tag=${1:?usage: release.sh vX.Y.Z [--publish]}
publish=${2:-}
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "tag must look like v1.2.3" >&2; exit 1; }
version=${tag#v}

root=$(cd "$(dirname "$0")/../.." && pwd)
out=$root/dist/release
module=github.com/semyonfox/tokentelemetry/engine
targets="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

rm -rf "$out"
mkdir -p "$out"

for target in $targets; do
	goos=${target%/*}
	goarch=${target#*/}
	name=tokentelemetry_${goos}_${goarch}
	stage=$out/$name
	exe=
	[[ $goos == windows ]] && exe=.exe
	mkdir -p "$stage"
	echo "building $goos/$goarch"
	# pure Go sqlite means no cgo, so one toolchain builds every target
	(cd "$root/engine" && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
		-ldflags "-s -w -X $module/internal/cli.Version=$version" \
		-o "$stage/" ./cmd/tokentelemetry ./cmd/tt)
	cp "$root/LICENSE" "$root/README.md" "$stage/"
	if [[ $goos == windows ]]; then
		(cd "$out" && zip -qrX "$name.zip" "$name")
	else
		tar -C "$out" --sort=name --owner=0 --group=0 --numeric-owner -czf "$out/$name.tar.gz" "$name"
	fi
	rm -rf "$stage"
done

(cd "$out" && sha256sum tokentelemetry_* > checksums.txt)
ls -l "$out"

[[ $publish == --publish ]] || exit 0

if gh release view "$tag" >/dev/null 2>&1; then
	echo "release $tag already exists; not publishing" >&2
	exit 0
fi
# release notes come from the matching CHANGELOG section
notes=$(awk -v h="## $version" '$0 == h || index($0, h " ") == 1 {on=1; next} /^## / && on {exit} on' "$root/CHANGELOG.md")
[[ -n $notes ]] || { echo "CHANGELOG.md has no '## $version' section" >&2; exit 1; }
gh release create "$tag" "$out"/tokentelemetry_* "$out/checksums.txt" \
	--title "TokenTelemetry $version" --notes "$notes" --verify-tag
