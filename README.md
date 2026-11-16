# expose-quota

A quota-only plugin for CLIProxyAPI v8.0.23. It exposes redacted Antigravity, Claude and Codex OAuth quota snapshots; it does not route requests.

## Install

Download the release built for your CPA host's OS and architecture. The Linux AMD64 release targets glibc and CPA v8.0.23; other platforms must build from source.

```sh
VERSION=0.1.0
curl -fLO "https://github.com/mauriciozaffari/expose-quota/releases/download/v${VERSION}/expose-quota-linux-amd64.tar.gz"
curl -fLO "https://github.com/mauriciozaffari/expose-quota/releases/download/v${VERSION}/SHA256SUMS"
sha256sum --ignore-missing -c SHA256SUMS
tar -xzf expose-quota-linux-amd64.tar.gz
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

`GET /v0/resource/plugins/expose-quota/quota.json` requires the bearer key and performs a fresh read on each request. Responses are `no-store`; failed, missing, expired or unsupported credentials are unknown, never exhausted. Accounts are anonymous and have no stable identifiers. The plugin reads Antigravity, Claude and Codex OAuth credentials through host callbacks and sends requests directly to allowlisted provider hosts, without redirects. It does not inherit CPA's outbound proxy. Ollama Cloud config keys are not available through CPA's auth-file callbacks and are not supported. No snapshot cache or background refresh is implemented.

Installing this plugin alone does not enable quota-aware routing in Race Control; the standalone gateway still needs to consume its snapshots.
