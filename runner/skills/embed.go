// Package skills carries trusted ticket guidance into every session runtime.
package skills

import _ "embed"

// ManagingTickets is also installed as an engine-discoverable skill on workstations.
// Native, sandbox and cluster sessions receive the same content in their shared guide.
//
//go:embed managing-tickets/SKILL.md
var ManagingTickets string

// Coordinating is distributed through the same host and shared-runtime paths.
//
//go:embed coordinating/SKILL.md
var Coordinating string

// CoordinatingExamples is coordinating's linked worked-examples file. It ships
// beside SKILL.md in every runtime but is read only when needed, so it never
// counts against the always-on or initial-load budgets.
//
//go:embed coordinating/examples.md
var CoordinatingExamples string

// TestDrivenDevelopment is distributed to every engine through the shared guide
// and workstation skill discovery directories.
//
//go:embed test-driven-development/SKILL.md
var TestDrivenDevelopment string

// UXDesign is distributed through the shared guide and workstation discovery.
//
//go:embed ux-design/SKILL.md
var UXDesign string

// Planning maintains the durable blackboard in every engine.
//
//go:embed planning/SKILL.md
var Planning string

// Visualizing is distributed through the shared guide and workstation discovery.
//
//go:embed visualizing/SKILL.md
var Visualizing string

// VisualizingExamples is visualizing's linked worked-templates file, shipped beside
// SKILL.md but read only on demand, same as coordinating's.
//
//go:embed visualizing/examples.md
var VisualizingExamples string

// TestTiming is also installed as an engine-discoverable skill on workstations,
// like ManagingTickets: it ships to every workspace with no off switch.
//
//go:embed test-timing/SKILL.md
var TestTiming string
