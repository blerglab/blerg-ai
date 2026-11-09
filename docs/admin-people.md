# Invite a person and set up their desktop

With local sign-in enabled, an administrator opens **Settings → People** and enters a
name and email address. New people are members, without administrator access. Copy the
invite link and send it privately. Blerg does not send email. The link is shown once,
works once, and expires after seven days; only its hash is stored.

The person opens the link, chooses a username and password using the existing first-login
rules, and lands on **Set up your desktop**. Under **Your desktops**, **Add a desktop**
creates a short-lived, one-time install command enrolling the desktop to that person.
The invite does not grant an administrator access to the person's password.

Administrators can see everyone's desktop status in **People**, and select another enabled
person and **Reassign**. Reassignment disconnects the old owner and requires the new owner
to enroll the desktop. Existing work is kept. The current ownership revision prevents a
stale page from overwriting another assignment.

**Disable** requires confirmation. It disables sign-in and revokes human sessions, agent
tokens, desktop credentials, and unused desktop enrollment codes in one transaction.
The runner invalidates standing desktop connections at a new revision. If it is unavailable,
the account stays disabled; **Disconnect desktops** safely retries that remaining step.
Administrative disable survives organization membership reconciliation. Desktop credentials
are also refused whenever their account is disabled. It keeps the
person and their desktops so work can be recovered or reassigned. Administrators cannot
disable themselves. This page deliberately offers no automatic re-enable action.

If an invite is lost or expires, disable that pending person and invite them again. A
self-service resend or re-enable policy remains an owner decision. Inviting, accepting,
and disabling are recorded in core's `desktop_audit` alongside existing enrollment and
desktop actions; secrets never appear in that audit.

Expired invitation links no longer count as pending invitations. If disabling a
person cannot acquire the session lock in time, the API returns a retryable busy
response (503 with Retry-After) rather than an authorization denial.
