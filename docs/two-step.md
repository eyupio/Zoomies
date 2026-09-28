---
icon: material/shield-key-outline
description: >-
  Add a code from an authenticator app to your Zoomies sign-in, keep the
  recovery codes somewhere safe, and what an administrator does when
  somebody loses their phone.
---

# Two-step verification

Two-step verification adds a second step to signing in to Zoomies: after your
password, a six-digit code from an authenticator app on your phone. Somebody
who has learned your password — from another site that leaked it, or from a
phishing page — still cannot sign in as you without the phone.

It is for accounts that sign in with a **password**. It is off until you turn
it on, and an administrator can make it compulsory for every password account
on the instance.

Any app that implements the standard (TOTP, RFC 6238) works: 1Password,
Bitwarden, Google Authenticator, Microsoft Authenticator, Aegis, Ente Auth,
and most password managers.

## Turn it on

1. Open **Settings → Account** and choose **Turn on two-step verification**.
2. Scan the QR code with your authenticator app. If you cannot scan — on the
   phone you are reading this on, say — choose **Enter the key instead** and
   type the key into the app. The entry is named *Zoomies* and shows your
   username and this instance's address, so a staging and a production
   instance do not end up as two identical entries.
3. Type the six-digit code the app shows, and choose **Turn on**.
4. Zoomies shows ten **recovery codes**. This is the only time they are shown.
   Copy or download them and keep them somewhere that is not your phone — a
   password manager, or printed and put away.

Turning it on signs you out everywhere else: those sessions were signed in with
the password alone.

## Sign in

Sign in with your username and password as usual. Zoomies then asks for the
code from your app. Type the six digits shown now; spaces are fine. You have
five minutes and five tries before you are sent back to the password.

Each code works **once**. If a code is refused straight after you used it,
wait for the app to show the next one.

## Recovery codes

Each recovery code works once, in place of a code from the app — at sign-in,
or when turning two-step off. Type it in the same box; the hyphen and capitals
do not matter.

**Settings → Account** says how many you have left. When you are running low,
or think somebody else has seen them, choose **New recovery codes**: you give
your password and a current code, and every old code stops working.

## Turn it off, or move to a new phone

Choose **Turn off** on **Settings → Account** and give your password and a
current code (a recovery code works too). To move to a new phone, turn it off
and on again from the new one — the old phone's entry stops working the moment
you do.

## Lost your phone

Sign in with a recovery code, then turn two-step off and on again with the new
phone.

If you have no recovery codes left either, ask an administrator. They can reset
it for you:

* from **Settings → Users**, the account's **Reset two-step** action, or
* from a terminal, `zoomies users reset-two-step <user-id>`.

A reset removes your authenticator and recovery codes and signs you out
everywhere. You then sign in with your password alone and can set it up again —
or, where the instance requires two-step, you are asked to at that sign-in.
Every reset is recorded in the audit log as `user.two_step_reset`, with the
name of the administrator who made it.

!!! warning "Administrators: check who is asking"
    A reset is exactly what somebody holding a stolen password would ask for.
    Confirm the request through a channel other than the one it arrived on.

## Require it for everybody

Set `security.require_two_step: true` (or `ZOOMIES_REQUIRE_TWO_STEP=true`) and
restart. From then on, every password account that has no authenticator sets
one up at its next sign-in before it gets any further; accounts that already
have one simply keep using it. The problems panel shows `two_step.required` as
a reminder of what the setting does not cover.

## What it does not cover

* **Single sign-on accounts.** An account that signs in through your identity
  provider is never asked for a Zoomies code, even with two-step required. Its
  second factor belongs to the identity provider — require multi-factor
  authentication there, where your organisation's policy already lives.
* **API tokens.** A token is never asked for a code. It is already a long
  random secret, and the scripts and pipelines it is made for have nobody to
  type one. Keep tokens scoped to what they need, give them an expiry, and
  revoke one the moment it may have leaked.
* **Sessions already signed in** when an administrator turns the requirement
  on. They carry on until they expire; the requirement applies at the next
  sign-in.

[Security](security.md#two-step-verification) explains how the key and the
recovery codes are stored, and the limits a code attempt counts against.
