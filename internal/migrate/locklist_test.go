package migrate

import "testing"

func TestLockList(t *testing.T) {
	pair := func(mode, rel string) string { return mode + "\x00" + rel }
	parts := []string{pair("SHARE", "public.payment")}
	for _, p := range []string{"p1", "p2", "p3", "p4"} {
		parts = append(parts, pair("SHARE", "public.payment_"+p))
	}
	parts = append(parts, pair("ACCESS EXCLUSIVE", "public.a"), pair("ACCESS EXCLUSIVE", "public.b"))
	got := lockList(parts)
	want := "SHARE on public.payment, public.payment_p1 and 3 more; ACCESS EXCLUSIVE on public.a, public.b"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
