#!/bin/sh
set -eu
cd "$(dirname "$0")"
work=$(mktemp -d "${TMPDIR:-/tmp}/expose-quota-host.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
source_dir=$(go list -m -f '{{.Dir}}' github.com/router-for-me/CLIProxyAPI/v8)
mkdir -p "$work/host" "$work/plugins"
cp -R "$source_dir/." "$work/host/"
chmod -R u+w "$work/host"
go build -buildvcs=false -buildmode=c-shared -o "$work/plugins/expose-quota.so" .
cp testdata/host_test.go "$work/host/internal/pluginhost/expose_quota_external_test.go"
(cd "$work/host" && CPA_API_KEY=test-key EXPOSE_QUOTA_TEST_DIR="$work/plugins" go test -count=1 -run '^TestExternalExposeQuotaLifecycle$' ./internal/pluginhost)
