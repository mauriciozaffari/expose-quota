# expose-quota

A quota-only plugin for CLIProxyAPI v8.0.23. It exposes redacted Antigravity, Claude and Codex OAuth quota snapshots; it does not route requests.

## Install

Once the [store submission](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store/pull/380) is merged, install `expose-quota` through CPA's plugin store. Until then, install the release manually.

The v0.1.0 store archive supports Linux AMD64 with glibc 2.17 or later and CPA v8.0.23. Other platforms must build from source.

```sh
VERSION=0.1.0
ASSET="expose-quota_${VERSION}_linux_amd64.zip"
curl -fLO "https://github.com/mauriciozaffari/expose-quota/releases/download/v${VERSION}/${ASSET}"
curl -fLO "https://github.com/mauriciozaffari/expose-quota/releases/download/v${VERSION}/checksums.txt"
sha256sum --check checksums.txt
unzip "$ASSET"
install -m 755 expose-quota.so /path/to/cpa/plugins/expose-quota.so
```

Replace the destination with CPA's configured plugin directory. Enable it in CPA's config:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    expose-quota:
      enabled: true
```

Give the CPA process `CPA_API_KEY` in its environment, with the same value already configured as a CPA client key. An absent key denies every request; restart CPA after changing its environment. Never publish the key. Use TLS or a trusted private network.

```sh
curl --fail --silent \
  -H "Authorization: Bearer ${CPA_API_KEY}" \
  https://cpa.example.com/v0/resource/plugins/expose-quota/quota.json
```

For Docker, mount the host plugin directory read-only into CPA's container and pass `CPA_API_KEY` through the service environment. The container's plugin directory must match `plugins.dir` above.

## Build from source

Build on the same OS, architecture and libc family as CPA. Go 1.27.4 and a C compiler are required.

```sh
go test -race ./...
./test-host.sh  # pinned CPA host loader: registration and reload
go build -buildmode=c-shared -o expose-quota.so .
```

## Contract and limitations

The quota endpoint emits schema version 1. Upgrade consumers to accept version 1 before deploying this plugin. When rolling back, roll back the plugin before consumers.

`GET /v0/resource/plugins/expose-quota/quota.json` requires the bearer key and performs a fresh read on each request. Responses are `no-store`; failed, missing or expired credentials are reported as unknown, never exhausted. Accounts are anonymous and have no stable identifiers. The plugin reads Antigravity, Claude and Codex OAuth credentials through host callbacks and sends requests directly to allowlisted provider hosts, without redirects.
