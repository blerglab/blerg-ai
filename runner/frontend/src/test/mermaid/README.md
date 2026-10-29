# Mermaid browser corpus

Card #169. The corpus pins **Mermaid 12.2.0** and covers all **37 detector IDs** in
its installed `dist/mermaid.core.mjs`, including the four railroad grammars, ELK,
agent flow, swimlanes, event modeling, and the informational diagram.
`coverage.test.ts` fails if an installed detector lacks a case or the version changes.

`cases.json` contains generic sources, expected labels, provenance, and explicit outcomes.
The seed examples are the Mermaid fences in the installed package's `README.md`
(flowchart, sequence, gantt, class, state, pie, git graph, and journey). C4 uses a
smaller generic example. The remaining cases were derived from the installed
bundle's source-map grammars and renderers and `@mermaid-js/parser`'s generated
grammars. No network documentation or real user diagrams were used. Mermaid's
examples are MIT licensed; the installed package's notice is preserved in
`LICENSE.mermaid` beside the fixtures.

The sources exercise directions, nested graphs, shapes, edge types and IDs,
animation, classes, node/edge colors, notes, fragments, concurrency, relationships,
calendar tasks, branches/tags/merges/cherry picks, CSV values, nested blocks,
architecture's built-in icons and junctions, task metadata, grammar operators,
comments, title frontmatter, host light/dark themes, Unicode/emoji, Markdown,
line breaks/entities, long labels, 120 nodes, and 60 sequence messages. Event-modeling
regressions require exact text for encoded tags, script-like strings, quotes, named
and double-encoded entities, breaks, and long wrapped labels. State CSS refusal
cases cover false class boundaries and styles after real class assignments. Flowchart
class-assignment cases cover Unicode IDs/classes, comma-separated node and class lists,
punctuation and numeric operands, and refusal of later unsafe style declarations.
`MermaidTextLabels.browser.test.tsx` supplies controlled renderer SVG to the actual
sandbox converter and parent pipeline. It checks text-node construction, HTML
recovery, permitted elements/attributes, and parent refusal of hostile siblings
and attributes after conversion; it does not replace the real-bundle corpus.

Source HTML, external icons/images, links/clicks, configuration directives,
configuration frontmatter, math, excessive source length, and excessive edge
counts have named **policy refusal** cases. These assert a specific reason and
empty output; they do not count as rendered diagrams. Built-in architecture icons
are exercised by successful cases. Source themes are intentionally refused;
host themes are the supported theme mechanism.

## Run and review

From the repository root:

```sh
npm test -w runner/frontend -- --project browser MermaidCorpus
npm test -w runner/frontend -- --project unit src/test/mermaid/coverage.test.ts
```

The normal workspace `npm test` already runs these tests. Unit tests run first,
then browser files use at most four simultaneous pages. Four tiny browser test
files shard the corpus over isolated pages in one Chromium. Each page calls
`renderMermaid`, the same validator → serialized opaque iframe → sanitizer →
decoded inert image path used by chat and viewers. There is no mocked renderer.
The route tests also exercise Markdown fences and `.mmd` viewers at **390** and
**3840** pixels. Existing Mermaid real-browser and network tests remain in place.

Successful cases require a decoded image, finite positive bounds, no parser/error
node, required labels, explicit colors where specified, and a stored structural
snapshot. Snapshots retain all SVG element counts, CSS class counts, and ordered visible labels,
while excluding generated element IDs and layout coordinates. Mermaid's random default
git commit hashes and measured-width class suffixes are normalized. Checks accept SVGs with explicit
intrinsic dimensions instead of a viewBox (the `info` renderer uses this form).
The sandbox attribute and CSP are checked on successful cases as well.

Review intentional changes to both fixtures and snapshots, then update explicitly:

```sh
npm test -w runner/frontend -- --project browser MermaidCorpus --update
```

Snapshots live beside the four browser files in `components/__snapshots__`.
Never regenerate them just to make a failure disappear. Required-label/color
assertions still run during updates, so empty or incomplete output cannot become
a successful baseline. Vitest names each failing case and prints its reason.
Caches are local to each checkout, avoiding writes into a symlinked shared install.

## Explicit upstream limitations

These are active tests, not skips. Each has a reason in `cases.json` and checks
raw output from the bundled Mermaid through the same sandbox, bypassing Blerg's
validator and sanitizer to establish ownership. A future upstream fix makes the
expectation fail and requires review.

| Case | Mermaid 12.2.0 limitation | Workaround |
| --- | --- | --- |
| `upstream-sankey-cycle` | d3-sankey throws `circular link` for a cycle. | Use an acyclic Sankey graph. |
| `upstream-flow-hex-semicolon` | The entity preprocessor mistakes `#hex;` inside a classDef for an entity, producing a parse error. | Put a space before the semicolon, use named colors, or put styles on separate lines. |
| `upstream-eventmodeling-quoted-data` | The grammar accepts quoted inline data, but the renderer slices it using brace offsets and loses the content. | Use brace-delimited inline data. |
| `eventmodeling-notes-upstream` | The parser accepts note entities, but the renderer draws only boxes and relations. | Put essential text in a model box or adjacent Markdown. |

Event modeling's hardcoded XHTML is handled by Blerg: inside the sandbox, its label
boxes become measured SVG text. HTML-only breaks/entities are normalized before
XML serialization. The final image still passes the SVG allowlist; HTML, foreign
objects, active attributes, and external references remain disallowed there.
