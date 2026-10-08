# Support

Zoomies is free and open-source software, published under AGPL-3.0 by people
who are not on call for it. There is no support contract, no guaranteed
response time, and no paid tier that would buy one. That is the whole boundary,
and it is stated plainly so that nobody discovers it during an incident.

What there is: documentation written to be acted on, error messages that name
the setting to change, and maintainers who read what comes in.

## Where a question goes

| You want to | Go to |
| --- | --- |
| Work out why something is not doing what you expected | [Troubleshooting](https://zoomies.sh/troubleshooting/), and the problems panel in your own UI; it names the setting rather than the symptom |
| Ask how to do something | [Discussions](https://github.com/eyupio/zoomies/discussions) |
| Report a bug | [Issues](https://github.com/eyupio/zoomies/issues) |
| Report a vulnerability | A [private advisory](https://github.com/eyupio/zoomies/security/advisories/new). Not an issue, not a discussion, see [SECURITY.md](SECURITY.md) |
| Propose a change | [CONTRIBUTING.md](CONTRIBUTING.md) |

## What makes a report answerable

Most of what a maintainer needs, Zoomies will print for you:

* `zoomies version`: the build, on both the controller and the agent if they
  differ;
* `zoomies config print`: the configuration, already blanked of secrets;
* the problem code from the UI or the log, such as `bind.public_no_tls`. Every
  code has [a row explaining it](https://zoomies.sh/problem-codes/), and naming
  it skips a round of questions;
* what you expected, and what happened instead.

Logs help. Please read them before pasting: a job's output is yours, and a
controller's log can name repositories.

## If you deployed from a provider's marketplace

Your provider supports their platform; the instance, its network, its volume,
its billing. They do not maintain Zoomies and cannot fix it.

So: anything about the machine goes to them, and anything about the software
comes here, on the same terms as every other installation. Nothing about a
one-click install creates a support relationship with this project, and no
provider can offer one on its behalf.

## What is qualified

Not everything that builds has been run. The [support
matrix](https://zoomies.sh/#what-is-qualified) separates the two, and it is
worth reading before putting anything precious on this; a report about a
combination nobody has qualified is still welcome, but it is a different
conversation from one about a combination that is supposed to work.
