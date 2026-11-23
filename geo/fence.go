package geo

// Fence is a geo-fence stored either as a circle (Center plus RadiusM) or as a polygon
// (Ring, GeoJSON [lng,lat] order, with optional Holes). When Ring has three or more
// points it wins over the circle.
type Fence struct {
	Center  Point         `json:"center"`
	RadiusM float64       `json:"radius_m"`
	Ring    [][]float64   `json:"ring,omitempty"`
	Holes   [][][]float64 `json:"holes,omitempty"`
}

// IsPolygon reports whether the fence is described by its ring.
func (f Fence) IsPolygon() bool { return len(f.Ring) >= 3 }

// Usable reports whether the fence can be checked at all.
func (f Fence) Usable() bool {
	if f.IsPolygon() {
		return true
	}
	return Valid(f.Center) && f.RadiusM > 0
}

// FenceCheck is the outcome of testing a point against a fence.
type FenceCheck struct {
	Inside bool `json:"inside"`
	// DistanceM is 0 when inside, otherwise how far outside the fence the point is.
	DistanceM float64 `json:"distance_m"`
}

// Check tests p against the fence. toleranceM widens the fence, which absorbs GPS
// accuracy (pass the device's reported accuracy, capped by the caller). An unusable
// fence or an invalid point reports outside with distance 0.
func (f Fence) Check(p Point, toleranceM float64) FenceCheck {
	if !Valid(p) || !f.Usable() {
		return FenceCheck{}
	}
	if toleranceM < 0 {
		toleranceM = 0
	}
	var outsideM float64
	if f.IsPolygon() {
		if InPolygon(p, f.Ring, f.Holes...) {
			return FenceCheck{Inside: true}
		}
		outsideM = DistanceToRingKm(p, f.Ring) * 1000
		if outsideM == 0 { // inside the outer ring, so p sits in a hole
			for _, h := range f.Holes {
				if InRing(p, h) {
					outsideM = DistanceToRingEdgeKm(p, h) * 1000
					break
				}
			}
		}
	} else {
		d := Distance(p, f.Center)*1000 - f.RadiusM
		if d <= 0 {
			return FenceCheck{Inside: true}
		}
		outsideM = d
	}
	if outsideM <= toleranceM {
		return FenceCheck{Inside: true}
	}
	return FenceCheck{DistanceM: outsideM - toleranceM}
}

// Nearest returns the index of the fence that contains p, or else the one p is closest
// to, with its check. ok is false when no fence is usable.
func Nearest(p Point, fences []Fence, toleranceM float64) (idx int, check FenceCheck, ok bool) {
	idx = -1
	for i, f := range fences {
		if !f.Usable() {
			continue
		}
		c := f.Check(p, toleranceM)
		if c.Inside {
			return i, c, true
		}
		if idx < 0 || c.DistanceM < check.DistanceM {
			idx, check = i, c
		}
	}
	return idx, check, idx >= 0
}
