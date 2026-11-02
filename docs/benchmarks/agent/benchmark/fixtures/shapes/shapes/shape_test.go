package shapes

import (
	"math"
	"testing"
)

func TestPerimeter(t *testing.T) {
	c := Circle{R: 2}
	if got, want := c.Perimeter(), 4*math.Pi; math.Abs(got-want) > 1e-9 {
		t.Fatalf("Circle.Perimeter() = %v, want %v", got, want)
	}
	r := Rect{W: 3, H: 4}
	if got, want := r.Perimeter(), 14.0; got != want {
		t.Fatalf("Rect.Perimeter() = %v, want %v", got, want)
	}
}

func TestTotalPerimeter(t *testing.T) {
	got := TotalPerimeter([]Shape{Circle{R: 1}, Rect{W: 3, H: 4}})
	want := 2*math.Pi + 14
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("TotalPerimeter() = %v, want %v", got, want)
	}
}
