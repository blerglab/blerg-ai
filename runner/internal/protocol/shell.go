package protocol

import "github.com/blerglab/blerg-ai/contracts/shellpolicy"

// Aliases keep daemon/server protocol and core policy on one shared wire schema.
type ShellPolicy = shellpolicy.ShellPolicy
type ShellGrant = shellpolicy.ShellGrant
type HostShellConsent = shellpolicy.HostShellConsent

const ShellGrantSeconds = shellpolicy.ShellGrantSeconds
const ShellRenewSeconds = shellpolicy.ShellRenewSeconds
