package uuidint

import (
	"testing"
)

func TestClampStep(t *testing.T) {
	cases := map[int]int{
		-5:          1,
		0:           1,
		1:           1,
		10:          10,
		MaxIncr:     MaxIncr,
		MaxIncr + 1: MaxIncr,
	}
	for in, want := range cases {
		if got := clampStep(in); got != want {
			t.Fatalf("clampStep(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestExpandRange(t *testing.T) {
	ids := expandRange(1000, 3)
	want := []int64{998, 999, 1000}
	if len(ids) != len(want) {
		t.Fatalf("expandRange len = %d, want %d", len(ids), len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("expandRange = %v, want %v", ids, want)
		}
	}

	if ids := expandRange(2, 3); ids != nil {
		t.Fatalf("expandRange(2, 3) = %v, want nil", ids)
	}
	if got := expandRange(2_000_000, MaxIncr+10); len(got) != MaxIncr {
		t.Fatalf("expandRange above MaxIncr = %d ids, want %d", len(got), MaxIncr)
	}
}
