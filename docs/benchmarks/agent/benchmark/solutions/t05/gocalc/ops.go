package gocalc

// Sum returns the sum of nums.
func Sum(nums []int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total
}

// Max returns the largest value in nums, or 0 for an empty slice.
func Max(nums []int) int {
	if len(nums) == 0 {
		return 0
	}
	best := nums[0]
	for _, n := range nums[1:] {
		if n > best {
			best = n
		}
	}
	return best
}
