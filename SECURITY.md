# Security policy

## Reporting a vulnerability

Open a [private security advisory][advisory] on this repository rather than a
public issue. GitHub keeps the report between you and the maintainers until
there is a fix.

Please include:

* the version, from `zoomies version`;
* the configuration with secrets removed, which `zoomies config print` produces
  already blanked;
* what an attacker gains, and what they need in order to get it.

Reports are acknowledged within a few days. If a fix is warranted it ships in a
release, and the advisory is published with credit unless you would rather it
were not.

## What counts

Zoomies runs other people's workflow code on your machines, so the interesting
boundary is between a job and everything else: escaping a runner container,
reading another job's secrets, reaching the controller's API without a
credential, or making the controller act as an administrator.

Some things are dangerous **by configuration** rather than by defect, and each
one is documented, warned about at startup and shown in the UI's problems
drawer. `pool.docker_mode: host-socket` hands the host's Docker socket to
every job in the pool, which is root on the host by design; `security.disable_auth`
treats every request as an administrator. Those are not vulnerabilities, they
are settings with a cost, and [docs/security.md](docs/security.md) says what
each one costs. A report that they can be abused once enabled tells us what the
warning already says.

What *is* worth reporting: a way to reach one of those states without setting
it, a warning that does not fire when it should, or a default that is less safe
than it is documented to be.

## Supported versions

Fixes go to the latest release; there are no maintained release branches. Upgrading is stop, replace the binary, start,
see [Upgrading](docs/upgrading.md).

[advisory]: https://github.com/eyupio/zoomies/security/advisories/new
