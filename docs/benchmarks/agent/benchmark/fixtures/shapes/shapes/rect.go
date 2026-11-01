package shapes

type Rect struct {
	W, H float64
}

func (r Rect) Area() float64 {
	return r.W * r.H
}
