package shapes

type Rect struct {
	W, H float64
}

func (r Rect) Area() float64 {
	return r.W * r.H
}

func (r Rect) Perimeter() float64 {
	return 2 * (r.W + r.H)
}
