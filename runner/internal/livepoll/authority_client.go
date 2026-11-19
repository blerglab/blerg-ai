package livepoll

import ld "github.com/blerglab/blerg-ai/contracts/livedata"

// The transport is an in-process authority dependency, never a gateway route.
var _ Authority = (*ld.ServiceClient)(nil)
