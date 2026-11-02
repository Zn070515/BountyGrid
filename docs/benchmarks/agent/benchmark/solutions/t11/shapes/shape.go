package shapes

// Shape is a closed two-dimensional figure.
type Shape interface {
	Area() float64
	Perimeter() float64
}
