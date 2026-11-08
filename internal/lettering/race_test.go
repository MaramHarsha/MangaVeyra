//go:build race

package lettering

// raceSlowdown scales timing bounds under the race detector.
const raceSlowdown = 10
