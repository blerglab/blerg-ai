---
name: ux-design
description: Design and review any UI or frontend change in Blerg. Use whenever adding, changing, or fixing interface content, layout, styling, controls, or interaction.
---

# UX design

## Earn your place

- Ask whether this is needed here. Choose the quietest version that works.
- Put settings in the existing settings panel.
- Give a new top-level element a reason to exist.

## Controls

- Keep controls at natural width; never stretch them across a window.
- Use one look for one action. Prefer icons over labeled buttons; use no
  outline unless the control is the real call to action.
- Navigation tabs keep their text beside the glyph; truncate the label first and drop to the
  glyph only when no readable text fits. Glyph-only tabs are hard to hit and to tell apart.
- Give each icon a clear tooltip and an accessible label.
- Keep one object’s actions in one utility bar. Use icons consistently, rather than mixing words and icons.
- Put an action on what it acts on: a + belongs to its list's heading,
  not a tab strip, where it reads as add a tab.

## Density

- Spend space on content, not chrome. Start with a compact row and hovercard;
  click for detail in a modal or tab.
- Show state as one glyph; put words in tooltips. A dot means one thing.

## Hierarchy

- Make what the person made stand out; keep agent-run things subdued.
- Repeated marks draw the eye. Show a shared fact once on the heading.

## One system

- Reuse existing tabs, dialogs, toasts and notices. Leave no unstyled text.
- Take every color from the central theme tokens, charts, diagrams and exports included: no
  hex literals or theme IDs in components, and a repaint on theme switch. Check one
  color-blind theme as well as the default.
- Make messages truthful, with no duplicate or contradictory status.
- Keep people inside the app.

## Character

- Use real names, signs of life, and agent-picked emoji where they fit.
- Leave nothing that reads as an empty form.

## Check

- Check a visual change against every state of the component, including combinations.
  For session cards: idle, running, selected, hover, focus, waiting-on-you question,
  failed/error, unread, paused, starred, ended, collapsed tiles, and selected plus waiting.
  Identity bars and state borders, tints and glyphs must all remain visible together.
- Take real screenshots at phone (390 px) and very wide (3840 px) widths;
  verify the encoded image widths, then look at them before merging.
- Check narrow panes inside wide windows too; toolbar density follows the pane’s width.
- Check filtered chart states with events: changing visible series must not make totals overlap the event markers or labels.
- Use the space without stretching small controls.
- Ask: needed here, matches neighbors, quietest version?

## Feedback

- Say a thing once: no heading repeating its tab, no label repeating its value, no text shown twice.
- Leading with a trend does not authorize removing existing information. Keep established detail reachable below it in one labeled section, using the same filters and data.

- When the same remark comes twice, add it here.

## Blerg

- Full-width Verbosity dropdown across the window: use natural width in the existing settings panel.
- Follow checkbox pinned atop every workspace: move it into the existing settings panel.
- Boxed + in a tab strip: use a plain + on the heading of the list it adds to.
- Boxed refresh icon beside a plain one: use the same plain refresh control for both.
- Raw Paused text under the composer: use the existing styled status pattern with one glyph and a tooltip.
- False Waiting for your answer pill: show it only while a real question needs the person's answer.
- White dot that meant running: use the existing running glyph and tooltip; reserve each dot meaning consistently.
- A file tab followed by its filename again: keep the name on the tab, its full path in the tooltip, and a visible path only when directories add useful context.
- Parent/child labels in History and conversation authors: name Main or the actual worker or channel; keep relationship identifiers inside the protocol.

- A popover's Escape must close that popover and consume the key, so the workspace tab behind it stays open. Test this through the app's navigation and keyboard handlers.

- Dialog shells must bring their shared styles through their own entry path; never rely on a neighboring launch sheet or chat having opened first.
