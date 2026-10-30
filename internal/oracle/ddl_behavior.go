package oracle

// behavior is what a DDL does when another transaction has uncommitted
// changes on its table, as TestDDLPredictions observes on 23.26.
type behavior struct {
	// nowait: it needs a table lock at once, so it fails with ORA-00054
	// when ddl_lock_timeout is 0, or after waiting that many seconds.
	nowait bool
	// waitsTx: it waits for those transactions to end, whatever
	// ddl_lock_timeout is, while inserts, updates and deletes continue.
	waitsTx bool
}

func (b behavior) rank() int {
	switch {
	case b.nowait:
		return 2
	case b.waitsTx:
		return 1
	}
	return 0
}

var (
	nowait  = behavior{nowait: true}
	waitsTx = behavior{waitsTx: true}
)

// behaviors is keyed by plan.op. An op missing here is assumed to need its
// lock at once, as DDL does by the Database Reference's DDL_LOCK_TIMEOUT.
var behaviors = map[string]behavior{
	"add column":                     waitsTx,
	"add virtual":                    {},
	"add identity":                   nowait,
	"add column constraint":          nowait,
	"add column index":               nowait,
	"modify column":                  nowait,
	"modify narrow":                  nowait,
	"modify not null":                nowait,
	"modify not null novalidate":     waitsTx,
	"add constraint validate":        nowait,
	"add constraint novalidate":      waitsTx,
	"add constraint unique":          nowait,
	"add constraint unique existing": nowait,
	"validate constraint":            {},
	"enable validate":                nowait,
	"create index":                   nowait,
	"create index online":            waitsTx,
	"rebuild index":                  nowait,
	"rebuild index online":           waitsTx,
	"drop column":                    nowait,
	"drop column online":             nowait,
	"set unused":                     nowait,
	"set unused online":              waitsTx,
	"drop constraint":                nowait,
	"drop constraint online":         nowait,
	"drop index":                     nowait,
	"drop index online":              waitsTx,
	"rename column":                  nowait,
	"rename table":                   nowait,
	"move":                           nowait,
	"move online":                    waitsTx,
	"truncate":                       nowait,
	"drop table":                     nowait,
	"other":                          nowait,
}

func behaviorOf(op string) behavior {
	if b, ok := behaviors[op]; ok {
		return b
	}
	return nowait
}
