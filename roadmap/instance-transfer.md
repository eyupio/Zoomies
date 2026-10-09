# Complete instance transfer

ZF-237 moves retained instance data between independently operated controllers.
It does not copy a running job, a browser session, host files or a service unit.

## Plan

1. Offer one-click preparation. Pause new runner and machine demand without
   changing saved pool settings. Busy jobs finish naturally; idle withdrawal
   and cleanup continue. Persist the preparation across restarts, show counts,
   and raise the recovery fence only when the fleet is quiescent.
2. Copy the database consistently through the existing backup machinery.
   Materialise effective fleet settings, including secrets supplied by a file
   or environment, into the copy. Omit source process and operator secrets
   before the archive leaves the source. Re-seal all retained encrypted stores under a fresh
   transfer key. Include that key only inside a mandatory passphrase-encrypted
   archive; never include the source key.
3. Verify and decrypt the whole archive in a private staging directory.
   Reject unsupported formats, damaged data, unreadable secrets and occupied
   destinations. Re-seal the staging database under the destination key.
4. Replace process settings and sign-in configuration with the destination's.
   Disable source operator accounts, revoke their tokens, end sessions and
   unused enrolment capabilities, and preserve destination operator access.
   Retain historical identities rather than deleting audit attribution.
5. Apply through the existing staged restore or the stopped-controller CLI.
   Keep reconciliation fenced and rented-machine ownership unverified. The
   operator checks credentials, agents and webhooks before lifting the fence.
6. Prove round trips, secret coverage, privilege handover, failure atomicity,
   bounded extraction and compatibility through regression tests.

## Cutover

Drain live runners, stop the source, and take the final snapshot. A rehearsal
copy never authorises both controllers to operate the same fleet. Import does
not delete the source. Before resuming either controller, stop the other.
Agents reconnect when the address changes; point GitHub App webhooks at the
new address. Verify rented-machine ownership before permitting deletion.

The destination chooses its own local operator or retains its existing operator
access for a staged import. Incoming accounts cannot carry process authority.
External sign-in subjects need destination mapping; local passwords and
team API tokens retain their values. Old OAuth capabilities are bound to
the old resource and are revoked, with new consent required.

## Failure handling

Export modifies a private copy, never the source database. Import prepares a
private copy and changes the live database only after every check succeeds.
Passphrases are never staged or logged. An interrupted upload is discarded;
a staged, verified copy survives without needing the passphrase at restart.
The ordinary restore records the outcome and keeps a recovery fence.

Missing secret coverage is a refusal, not a successful transfer with a broken
credential. A schema test inventories encrypted columns so a future store
cannot silently escape re-sealing. Unknown secret storage needs an explicit
addition before a release may claim portable transfer.
