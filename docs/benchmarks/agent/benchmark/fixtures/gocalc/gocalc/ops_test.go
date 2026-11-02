package gocalc

import "testing"

func TestSum(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{[]int{1, 2, 3}, 6},
		{nil, 0},
		{[]int{-2, 5}, 3},
	}
	for _, c := range cases {
		if got := Sum(c.in); got != c.want {
			t.Fatalf("Sum(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMax(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{[]int{1, 9, 3}, 9},
		{nil, 0},
		{[]int{-5, -2}, -2},
	}
	for _, c := range cases {
		if got := Max(c.in); got != c.want {
			t.Fatalf("Max(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
