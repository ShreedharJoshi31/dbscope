package estimate

import "testing"

func TestBytes(t *testing.T) {
	r := Bytes(200e6, Rate{LowMBps: 8, HighMBps: 190, FixedSeconds: 0.5}, "building the index")
	// The fast end is doubled (fasterHeadroom).
	if r.LowSeconds != 200.0/380 || r.HighSeconds != 200.0/8+0.5 {
		t.Errorf("range %v-%v", r.LowSeconds, r.HighSeconds)
	}
	want := "building the index: from 200.0 MB at 8.0-380 MB/s measured on a laptop-class machine; your server may be faster or slower"
	if r.Basis != want {
		t.Errorf("basis %q, want %q", r.Basis, want)
	}
	for n, want := range map[int64]string{512: "512 bytes", 1500: "1.5 kB", 3e9: "3.0 GB", 2e12: "2.0 TB"} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}
