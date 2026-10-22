package api

// SetNewBoardBeforeRecheck installs the test seam between the board-new exchange's mint and its
// second check, and returns a function that removes it.
func SetNewBoardBeforeRecheck(f func()) func() {
	newBoardBeforeRecheck = f
	return func() { newBoardBeforeRecheck = nil }
}
