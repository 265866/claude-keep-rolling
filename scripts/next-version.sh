#!/usr/bin/env bash
# Prints the next release version from Conventional Commit subjects since the
# last v* tag, as GitHub Actions outputs (version=, release=):
#   feat -> minor; fix or perf -> patch; "!" or BREAKING CHANGE -> minor while
#   below 1.0.0, major after; anything else -> no release.
# Without a previous tag the first release is 0.1.0.
set -euo pipefail

emit() {
	if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
		printf 'version=%s\nrelease=%s\n' "$1" "$2" >>"$GITHUB_OUTPUT"
	fi
	printf 'version=%s\nrelease=%s\n' "$1" "$2"
}

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true)
if [[ -z "$last" ]]; then
	emit 0.1.0 true
	exit 0
fi

IFS=. read -r major minor patch <<<"${last#v}"
subjects=$(git log --format=%s "$last..HEAD")
bodies=$(git log --format=%b "$last..HEAD")
type='[a-z]+(\([^)]*\))?'

if grep -qE "^$type!:" <<<"$subjects" || grep -q '^BREAKING CHANGE:' <<<"$bodies"; then
	if ((major == 0)); then
		minor=$((minor + 1)) patch=0
	else
		major=$((major + 1)) minor=0 patch=0
	fi
elif grep -qE '^feat(\([^)]*\))?:' <<<"$subjects"; then
	minor=$((minor + 1)) patch=0
elif grep -qE '^(fix|perf)(\([^)]*\))?:' <<<"$subjects"; then
	patch=$((patch + 1))
else
	emit "${last#v}" false
	exit 0
fi
emit "$major.$minor.$patch" true
