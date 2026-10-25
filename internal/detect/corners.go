package detect

// RejectCorners drops artefacts whose centre lies inside a size×size square at
// any image corner. Registration and optics leave the corners poorly
// constrained, so a feature there is usually an edge effect rather than a
// ghost.
//
// The test uses the candidate's centre only. A matched filter centred just
// outside the square can still respond to a corner feature's wings, but that
// response is weaker and further from the edge, so it is kept. A size of zero
// or less disables the rejection.
func RejectCorners(arts []Artefact, w, h, size int) ([]Artefact, int) {
	if len(arts) == 0 || size <= 0 || w <= 0 || h <= 0 {
		return arts, 0
	}

	kept := make([]Artefact, 0, len(arts))
	rejected := 0
	for _, a := range arts {
		left := a.X < size
		right := a.X >= w-size
		top := a.Y < size
		bottom := a.Y >= h-size
		if (left || right) && (top || bottom) {
			rejected++
			continue
		}
		kept = append(kept, a)
	}
	return kept, rejected
}
