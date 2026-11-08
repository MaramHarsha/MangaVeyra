//go:build !race

package editor_test

// raceSlowdown scales timing bounds under the race detector.
const raceSlowdown = 1
