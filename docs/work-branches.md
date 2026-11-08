# Work branches

In runner **Settings → Work branches**, set a branch template for your account.
It applies to new sessions, including coding children. Existing sessions keep the
branch recorded at launch, even if you change the template, title, or linked card.
Leave the template blank for a readable default such as `wip/ticket-data-7d67a123`: the launch title made safe for Git, followed by the first eight characters of the session ID. Empty or punctuation-only titles use `session`. Collisions receive an additional session-ID suffix. Names are recorded once and stay fixed after a rename.

An explicitly saved `wip/{session}` template keeps the full session ID, as do existing branches.

For example, `dev/{username}/{slug}` produces a name such as
`dev/example-person/fix-the-button`.

| Placeholder | Value |
| --- | --- |
| `{username}` | The account's sign-in username or provider subject, made safe for a branch |
| `{session}` | The first eight characters of the session ID in custom templates |
| `{slug}` | The launch title in lowercase, with punctuation and spaces replaced by hyphens; the short session ID when empty |
| `{date}` | The launch date in UTC, as `YYYY-MM-DD` |
| `{card}` | The first linked card's reference at launch, made safe for a branch |

When there is no card, its segment is omitted and leftover separators collapse.
An empty or invalid result after omitting a card falls back to the session ID. Variable fragments and the final
name are bounded. Unknown placeholders and templates that cannot produce a legal
Git branch are refused when saved. If another session already reserved the name,
a session-ID suffix makes it unique. Allocated names remain reserved after
Discard so a later session cannot adopt an old branch. Only the name, session ID and template-choice flag are kept in that reservation.

Custom names require a current workstation daemon or cluster agent image. Update the
workstation daemon or agent image when a launch reports unsupported custom names.
Older workstation daemons receive the full-session-ID default instead. Host launches do not require custom-branch capability.

A fresh session refuses an existing custom branch it does not own. Template-selected cluster branches use strict provider checks and pushes use a lease so an external update is preserved until it is integrated.

The saved branch is used for clone and worktree creation, delivery of commits,
recovery, merged-child checks, Discard, and agent guidance. A top-level host session
that works directly in an existing checkout keeps that checkout's current branch.

The account preference is also available through `GET /api/me/preferences` and
`PUT /api/me/preferences` as `branch_template`, a string. An empty string restores
the default. Administrator defaults and per-repository overrides are not included.

The cluster job passes `BLERG_RUNNER_WORK_BRANCH_CUSTOM=true` only when a
branch template was explicitly selected. The choice is saved with the branch,
independent of its spelling. Defaults keep ordinary resume and force-push behavior,
including when an agent amends or rebases its work. Historical records without
saved template intent retain the default policy; their names are not reinterpreted.

To find a child's branch, run `blerg-runner session result <child-id>` and use
`branch` (or each workspace member's branch). Never construct it from the ID.
