# Explorer operations

Collapse folders is in the workspace header and the Ctrl+K command palette. It
closes all folders for this session, cancels pending expansion, returns from a
focused subtree to the workspace, and retains the selected path. Reopening an
ancestor makes a hidden selection available again. Copy relative path copies
the selected workspace-relative path, including a repository prefix in a
multi-repository workspace. Both operations change only the view or clipboard.

## Adoption evaluation

| Operation | Decision | Permission boundary |
| --- | --- | --- |
| Collapse folders | Adopt now, header and palette | View only |
| Copy relative path | Adopt now, header and palette | Clipboard only |
| Filter / type to navigate | Keep loaded-name filter and keyboard tree navigation; bounded Search all files searches unloaded names | Existing workspace read access |
| New file / folder, rename | Defer until workspace write operations have runtime fencing and conflict handling | Repository write; agent calls must use the session's write permissions |
| Delete to trash | Defer until each runtime has recoverable trash semantics | Destructive repository write; explicit confirmation |
| Cut / copy / paste / duplicate | Defer with writes; clipboard paths must not grant filesystem access | Paste, cut and duplicate write to the repository |
| Drag move | Defer with rename; avoid accidental phone moves | Repository write |
| Copy absolute path | Defer: sandbox and host paths differ; relative paths are portable | Clipboard, but do not expose host paths to unauthorized readers |
| Reveal in terminal / shell | Defer until a terminal can safely accept a directory without injecting commands | Terminal access and execution permissions |
| Open to the side | Retain existing file preview and pinned viewer tabs; defer a dedicated tree action | Workspace read access |
| Find in folder | Defer content search; current search matches file names | Bounded workspace read access |
| Compare selected | Defer a two-file comparison workflow | Workspace read access |
| Sort order | Keep reader ordering (folders first, name order); defer persisted alternatives | View only |
| Auto-reveal active file toggle | Keep explicit Reveal in explorer; defer automatic reveals | View only after workspace access |
| Nested file patterns | Defer; retain compact single-folder chains | View only |
| Exclude settings | Retain reader's protected-path exclusions; defer custom hiding | Hiding is view only; cannot override protected-path rules |

Write operations should share one audited workspace-write contract rather than
implementing filesystem writes in the browser. A UI action and an agent action
must both validate the runtime generation, repository boundary and current
permissions. View settings never authorize a read or write by themselves.
