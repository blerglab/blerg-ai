# Product-phase examples

## Small PoC web app

Declare: `Product phase: PoC / pre-1.0; release discipline: one review batch before deployment.`

Prove that a few invited users can record and retrieve their data. Keep a short
acceptance list. For a sign-in batch, specify credential handling, session expiry,
ownership and privacy boundaries first; have a different model attack that spec.
Test each guard and its negative path while building. Run focused checks after
each fix, then the whole touched region and supported browsers once before review.
If review changes a guard, rerun its regression and affected integration proofs;
repeat broad gates when that change invalidates their evidence. Keep release gates.

Keep migration recovery and a short setup guide. Defer speculative analytics,
large-scale infrastructure and SOC 2 certification. A PoC still protects real data.

## Pre-1.0 product with release discipline

Declare: `Product phase: PoC / pre-1.0; release discipline: candidate qualification, supported upgrades and owner acceptance.`

A tool that orchestrates coding sessions can be pre-1.0 while promising retained
history, supported installs and upgrades. Keep those release promises: run focused
skill or daemon tests during a task, the whole affected region at batch review,
and candidate-wide end-to-end, platform and upgrade qualification once for the
release candidate. Do not run every browser and backend suite after a text edit.

Specify authority and private-data boundaries before implementation and obtain
independent adversarial review. Test old populated records and recovery. Keep
operator upgrade notes and release evidence. Defer unmeasured scale architecture;
do not defer security or required candidate gates merely because the version is 0.x.

## Beta to GA

Propose moving to GA when support promises and audience justify it. Record the
person's phase decision next to the vision or in AGENTS.md. Add explicit support
windows, compatibility policy, recovery documentation and release qualification.
Do not interpret a version-label edit as authorization to add enterprise scope.
