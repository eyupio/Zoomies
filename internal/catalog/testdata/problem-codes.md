# Problem codes

| Severity | What it means |
| --- | --- |
| **Error** | Startup stops. |

## Configuration: the listener

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `bind.empty` | error | `server.bind` | Give it a `host:port`. Nothing can start without one. |
| `security.disable_auth` | **warning on loopback, error elsewhere** | `security.disable_auth` | Every request is an administrator's. Remove it before anyone else can reach the listener. |

## Runtime: hosts and installations

| Code | Severity | What it means |
| --- | --- | --- |
| `host.unhealthy` | **error with runners on it, warning without** | The host has stopped heartbeating. |

## Runtime: pools, jobs and runners

| Code | Severity | What it means | How to see it worked |
| --- | --- | --- | --- |
| `jobs.unmatched` | warning | Jobs are queued that no pool will run \| or a pool is missing a label. | `zoomies jobs list --unmatched` is empty. |
| `jobs.label_advice` | info | Jobs whose runs call for another size. | The Jobs page lists no advice. |

## Runtime: infrastructure providers

| Code | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `provider.unreachable` | error | The provider's API did not answer. | Check the address. |

## What the status page says

| Code | What the status page says |
| --- | --- |
| `host.unhealthy` | A runner host has stopped answering. |
