package shapes

// TotalArea returns the sum of the shapes' areas.
func TotalArea(shapes []Shape) float64 {
	total := 0.0
	for _, s := range shapes {
		total += s.Area()
	}
	return total
}

// TotalPerimeter returns the sum of the shapes' perimeters.
func TotalPerimeter(shapes []Shape) float64 {
	total := 0.0
	for _, s := range shapes {
		total += s.Perimeter()
	}
	return total
}
