// Package geo holds the planar and spherical helpers the fleet uses for distance and
// containment: logistics delivery zones, quotes and dispatch, and ERP work-site
// geo-fences for staff clock-in. It is the one place this math lives; services import
// it instead of keeping their own copy.
//
// Coordinates follow GeoJSON order: a point in a ring is [lng, lat].
package geo

import "math"

const earthRadiusKm = 6371.0

// Point is a latitude/longitude pair in decimal degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// BBox is an axis-aligned bounding box in degrees.
type BBox struct {
	MinLat, MinLng, MaxLat, MaxLng float64
}

// Contains reports whether p falls inside (or on the edge of) the box.
func (b BBox) Contains(p Point) bool {
	return p.Lat >= b.MinLat && p.Lat <= b.MaxLat && p.Lng >= b.MinLng && p.Lng <= b.MaxLng
}

// Extend grows b to include o.
func (b BBox) Extend(o BBox) BBox {
	return BBox{
		MinLat: math.Min(b.MinLat, o.MinLat), MinLng: math.Min(b.MinLng, o.MinLng),
		MaxLat: math.Max(b.MaxLat, o.MaxLat), MaxLng: math.Max(b.MaxLng, o.MaxLng),
	}
}

// Center returns the midpoint of the box.
func (b BBox) Center() Point {
	return Point{Lat: (b.MinLat + b.MaxLat) / 2, Lng: (b.MinLng + b.MaxLng) / 2}
}

// HaversineKm is the great-circle distance between two points in kilometres.
func HaversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// Distance is HaversineKm for two Points.
func Distance(a, b Point) float64 { return HaversineKm(a.Lat, a.Lng, b.Lat, b.Lng) }

// RingBBox returns the bounding box of a [lng,lat] ring. ok is false for an empty ring.
func RingBBox(ring [][]float64) (BBox, bool) {
	first := true
	var b BBox
	for _, c := range ring {
		if len(c) < 2 {
			continue
		}
		lng, lat := c[0], c[1]
		if first {
			b = BBox{MinLat: lat, MaxLat: lat, MinLng: lng, MaxLng: lng}
			first = false
			continue
		}
		b.MinLat, b.MaxLat = math.Min(b.MinLat, lat), math.Max(b.MaxLat, lat)
		b.MinLng, b.MaxLng = math.Min(b.MinLng, lng), math.Max(b.MaxLng, lng)
	}
	return b, !first
}

// InRing runs the even-odd ray cast against one [lng,lat] ring. The ring may be open
// or closed; the closing segment is implied.
func InRing(p Point, ring [][]float64) bool {
	n := len(ring)
	if n < 3 {
		return false
	}
	inside := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		if len(ring[i]) < 2 || len(ring[j]) < 2 {
			continue
		}
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > p.Lat) != (yj > p.Lat) && p.Lng < (xj-xi)*(p.Lat-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}

// InPolygon reports whether p is inside the outer ring and outside every hole.
func InPolygon(p Point, outer [][]float64, holes ...[][]float64) bool {
	if !InRing(p, outer) {
		return false
	}
	for _, h := range holes {
		if InRing(p, h) {
			return false
		}
	}
	return true
}

// CirclePolygon approximates a circle as a closed [lng,lat] ring with `segments` sides.
// Stored zones always carry a polygon so every consumer can treat them the same way.
func CirclePolygon(center Point, radiusM float64, segments int) [][]float64 {
	if segments < 8 {
		segments = 8
	}
	ring := make([][]float64, 0, segments+1)
	dist := radiusM / 1000 / earthRadiusKm // angular distance
	lat1, lng1 := rad(center.Lat), rad(center.Lng)
	for i := 0; i < segments; i++ {
		brng := 2 * math.Pi * float64(i) / float64(segments)
		lat2 := math.Asin(math.Sin(lat1)*math.Cos(dist) + math.Cos(lat1)*math.Sin(dist)*math.Cos(brng))
		lng2 := lng1 + math.Atan2(math.Sin(brng)*math.Sin(dist)*math.Cos(lat1), math.Cos(dist)-math.Sin(lat1)*math.Sin(lat2))
		ring = append(ring, []float64{round6(deg(lng2)), round6(deg(lat2))})
	}
	ring = append(ring, []float64{ring[0][0], ring[0][1]})
	return ring
}

// RingAreaKm2 is the approximate area of a ring in square kilometres, using an
// equirectangular projection around the ring's centre. Good enough to rank overlapping
// zones by size; not a survey-grade measurement.
func RingAreaKm2(ring [][]float64) float64 {
	b, ok := RingBBox(ring)
	if !ok || len(ring) < 3 {
		return 0
	}
	kx, ky := kmPerDegree(b.Center().Lat)
	sum := 0.0
	n := len(ring)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		if len(ring[i]) < 2 || len(ring[j]) < 2 {
			continue
		}
		sum += ring[i][0]*kx*ring[j][1]*ky - ring[j][0]*kx*ring[i][1]*ky
	}
	return math.Abs(sum) / 2
}

// DistanceToRingKm returns 0 when p is inside the ring, otherwise the distance in
// kilometres from p to the nearest edge.
func DistanceToRingKm(p Point, ring [][]float64) float64 {
	if InRing(p, ring) {
		return 0
	}
	return DistanceToRingEdgeKm(p, ring)
}

// DistanceToRingEdgeKm is the distance in kilometres from p to the nearest edge of the
// ring, whether p is inside or outside it (use it to measure how deep a point sits in a hole).
func DistanceToRingEdgeKm(p Point, ring [][]float64) float64 {
	kx, ky := kmPerDegree(p.Lat)
	best := math.Inf(1)
	n := len(ring)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		if len(ring[i]) < 2 || len(ring[j]) < 2 {
			continue
		}
		ax, ay := (ring[i][0]-p.Lng)*kx, (ring[i][1]-p.Lat)*ky
		bx, by := (ring[j][0]-p.Lng)*kx, (ring[j][1]-p.Lat)*ky
		if d := segmentDistance(ax, ay, bx, by); d < best {
			best = d
		}
	}
	return best
}

// segmentDistance is the distance from the origin to segment AB in a local km plane.
func segmentDistance(ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	lenSq := dx*dx + dy*dy
	t := 0.0
	if lenSq > 0 {
		t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/lenSq))
	}
	x, y := ax+t*dx, ay+t*dy
	return math.Hypot(x, y)
}

func kmPerDegree(lat float64) (kx, ky float64) {
	ky = math.Pi * earthRadiusKm / 180
	kx = ky * math.Cos(rad(lat))
	return
}

// Valid reports whether p is a usable coordinate (not the 0,0 placeholder, within range).
func Valid(p Point) bool {
	if p.Lat == 0 && p.Lng == 0 {
		return false
	}
	return p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}

func rad(d float64) float64    { return d * math.Pi / 180 }
func deg(r float64) float64    { return r * 180 / math.Pi }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// DistanceToSegmentKm is the shortest distance in kilometres from p to the straight
// segment a to b, in a local plane around p (accurate for the tens of km a delivery
// route spans). A point beyond either end measures to that end.
func DistanceToSegmentKm(p, a, b Point) float64 {
	kx, ky := kmPerDegree(p.Lat)
	return segmentDistance((a.Lng-p.Lng)*kx, (a.Lat-p.Lat)*ky, (b.Lng-p.Lng)*kx, (b.Lat-p.Lat)*ky)
}

// Bearing is the initial compass bearing in degrees (0 north, 90 east) from a to b.
func Bearing(a, b Point) float64 {
	y := math.Sin(rad(b.Lng-a.Lng)) * math.Cos(rad(b.Lat))
	x := math.Cos(rad(a.Lat))*math.Sin(rad(b.Lat)) - math.Sin(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Cos(rad(b.Lng-a.Lng))
	return math.Mod(deg(math.Atan2(y, x))+360, 360)
}

// Compass names a bearing as one of eight points: north, north-east, east and so on.
func Compass(bearing float64) string {
	names := [...]string{"north", "north-east", "east", "south-east", "south", "south-west", "west", "north-west"}
	return names[int(math.Mod(math.Round(bearing/45), 8))]
}
