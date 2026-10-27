// Package score turns a classified statement into a number and a rating
// (docs/MIGRATIONS.md, "Scoring"):
//
//	score = L·W·Z + Q + 3T + 2H + 2K
//
// L is the lock class of the relation (0-3), W the work (0 catalog only,
// 1 scan or index build, 2 rewrite), Z the size tier of the data the
// statement processes, Q = 1 when L >= 2 (lock-queue exposure), T = 1 when
// L >= 2 and no lock_timeout is in effect, H = 1 when ACCESS EXCLUSIVE is
// already held earlier in the same transaction, K = 1 when L >= 2 on a hot
// table (one serving a large share of the database's reads, so a lock queue
// on it stalls much of the application). The statement's score is
// the maximum over the relations it locks. Bands: Low 0-2, Medium 3-6,
// High 7-11, Critical 12+.
//
// Z uses the size of the relation whose data the statement processes (the
// table it scans, rewrites or indexes), because that decides how long every
// one of its locks is held: ADD FOREIGN KEY holds SHARE ROW EXCLUSIVE on
// the referenced table for as long as it scans the referencing one.
package score

// Rating is the API's risk level.
type Rating string

const (
	Low      Rating = "low"
	Medium   Rating = "medium"
	High     Rating = "high"
	Critical Rating = "critical"
)

var rank = map[Rating]int{Low: 0, Medium: 1, High: 2, Critical: 3}

// Rank orders ratings (Low = 0).
func (r Rating) Rank() int { return rank[r] }

// Max returns the higher rating.
func Max(a, b Rating) Rating {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// ParseRating validates a --fail-on value.
func ParseRating(s string) (Rating, bool) {
	r := Rating(s)
	_, ok := rank[r]
	return r, ok
}

// Band maps a score to its rating.
func Band(score int) Rating {
	switch {
	case score >= 12:
		return Critical
	case score >= 7:
		return High
	case score >= 3:
		return Medium
	}
	return Low
}

const (
	mb = int64(1) << 20
	gb = int64(1) << 30
)

// Tier is Z: 0 under 10 MB, 1 under 1 GB, 2 under 10 GB, 3 under 50 GB,
// 4 from 50 GB (GitLab's table size classes for the top tiers). Unknown
// sizes are tier 2.
func Tier(bytes int64, known bool) int {
	switch {
	case !known:
		return 2
	case bytes < 10*mb:
		return 0
	case bytes < gb:
		return 1
	case bytes < 10*gb:
		return 2
	case bytes < 50*gb:
		return 3
	}
	return 4
}

// Inputs are the terms for one locked relation.
type Inputs struct {
	L, W, Z     int
	LockTimeout bool
	Held        bool
	Hot         bool
}

// Score computes L·W·Z + Q + 3T + 2H + 2K.
func Score(in Inputs) int {
	s := in.L * in.W * in.Z
	if in.L >= 2 {
		s++ // Q
		if !in.LockTimeout {
			s += 3 // T
		}
		if in.Hot {
			s += 2 // K
		}
	}
	if in.Held {
		s += 2
	}
	return s
}
