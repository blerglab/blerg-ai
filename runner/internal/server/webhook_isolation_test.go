package server

import "time"

// Test isolation for webhook delivery.
//
// A session that ends with a callback_url gets its webhook from a background
// goroutine (notifyCompletion) that outlives the handler, and so outlives the
// test that started it. On the production schedule it would retry a failed
// delivery 5, 30 and 120 seconds later, long after that test's receiver has
// closed and its loopback port has been handed to a LATER test's receiver:
// the stray retry then lands there as a request the later test never asked
// for ("receiver got 2 requests, want 1", or two deliveries where one was
// expected). It is rare, it moves with the order and number of tests, and it
// has nothing to do with the test it fails.
//
// So in this package's tests a delivery that nobody configured makes its one
// attempt and stops. A test that is about retries sets api.webhookBackoff
// itself (webhookSession does), which this does not touch.
func init() {
	webhookBackoffDefault = []time.Duration{}
}
