//go:build race

package sqlite

// raceDetector: the SQLite engine dbscope links is Go code, so the race
// detector instruments it and timings say nothing about real speed.
const raceDetector = true
