package geo

import (
	"math"
	"testing"
)

var busia = Point{Lat: 0.4545662, Lng: 34.1272854}

func square(minLng, minLat, maxLng, maxLat float64) [][]float64 {
	return [][]float64{{minLng, minLat}, {maxLng, minLat}, {maxLng, maxLat}, {minLng, maxLat}, {minLng, minLat}}
}

func TestHaversineKnownDistance(t *testing.T) {
	// Busia outlet to Alupe market is roughly 4.7 km in a straight line.
	d := HaversineKm(busia.Lat, busia.Lng, 0.497094, 34.13416)
	if d < 4.4 || d > 5.0 {
		t.Fatalf("busia to alupe = %.2f km, want about 4.7", d)
	}
	if HaversineKm(1, 1, 1, 1) != 0 {
		t.Fatal("same point must be 0")
	}
}

func TestInPolygonWithHole(t *testing.T) {
	outer := square(0, 0, 10, 10)
	hole := square(4, 4, 6, 6)
	cases := []struct {
		p    Point
		want bool
	}{
		{Point{Lat: 1, Lng: 1}, true},
		{Point{Lat: 5, Lng: 5}, false}, // inside the hole
		{Point{Lat: 11, Lng: 5}, false},
		{Point{Lat: 5, Lng: -0.1}, false},
	}
	for _, c := range cases {
		if got := InPolygon(c.p, outer, hole); got != c.want {
			t.Errorf("InPolygon(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestOpenRingWorks(t *testing.T) {
	open := [][]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	if !InRing(Point{Lat: 5, Lng: 5}, open) {
		t.Fatal("open ring should imply its closing edge")
	}
	if InRing(Point{Lat: 5, Lng: 5}, open[:2]) {
		t.Fatal("degenerate ring must never contain a point")
	}
}

func TestCirclePolygonRadius(t *testing.T) {
	ring := CirclePolygon(busia, 3000, 48)
	if len(ring) != 49 {
		t.Fatalf("ring len = %d, want 49 (closed)", len(ring))
	}
	if ring[0][0] != ring[48][0] || ring[0][1] != ring[48][1] {
		t.Fatal("ring must be closed")
	}
	for _, c := range ring {
		d := HaversineKm(busia.Lat, busia.Lng, c[1], c[0])
		if math.Abs(d-3) > 0.01 {
			t.Fatalf("vertex at %.4f km, want 3", d)
		}
	}
	if !InRing(busia, ring) {
		t.Fatal("centre must be inside its circle")
	}
	// 2.9 km north is inside, 3.1 km north is outside.
	if !InRing(Point{Lat: busia.Lat + 2.9/111.195, Lng: busia.Lng}, ring) {
		t.Fatal("2.9 km point should be inside")
	}
	if InRing(Point{Lat: busia.Lat + 3.1/111.195, Lng: busia.Lng}, ring) {
		t.Fatal("3.1 km point should be outside")
	}
	area := RingAreaKm2(ring)
	if want := math.Pi * 9; math.Abs(area-want)/want > 0.01 {
		t.Fatalf("area = %.3f, want about %.3f", area, want)
	}
}

func TestDistanceToRing(t *testing.T) {
	ring := CirclePolygon(busia, 1500, 48)
	if DistanceToRingKm(busia, ring) != 0 {
		t.Fatal("inside point must be 0")
	}
	p := Point{Lat: busia.Lat + 3.5/111.195, Lng: busia.Lng} // 3.5 km north, 2 km past the edge
	if d := DistanceToRingKm(p, ring); math.Abs(d-2) > 0.05 {
		t.Fatalf("edge distance = %.3f, want about 2", d)
	}
}

func TestBBox(t *testing.T) {
	b, ok := RingBBox(square(1, 2, 3, 4))
	if !ok || b.MinLng != 1 || b.MinLat != 2 || b.MaxLng != 3 || b.MaxLat != 4 {
		t.Fatalf("bbox = %+v", b)
	}
	if !b.Contains(Point{Lat: 3, Lng: 2}) || b.Contains(Point{Lat: 5, Lng: 2}) {
		t.Fatal("contains wrong")
	}
	if _, ok := RingBBox(nil); ok {
		t.Fatal("empty ring has no bbox")
	}
	if Valid(Point{}) || !Valid(busia) || Valid(Point{Lat: 91, Lng: 1}) {
		t.Fatal("Valid wrong")
	}
}

func TestSegmentDistanceBearingAndCompass(t *testing.T) {
	a, b := Point{Lat: 0.45, Lng: 34.12}, Point{Lat: 0.63, Lng: 34.12} // due north, about 20 km
	beside := Point{Lat: 0.54, Lng: 34.12 + 3/111.195}                 // 3 km east of the middle
	if d := DistanceToSegmentKm(beside, a, b); math.Abs(d-3) > 0.05 {
		t.Fatalf("beside the route = %.3f km, want about 3", d)
	}
	past := Point{Lat: 0.63 + 4/111.195, Lng: 34.12} // 4 km beyond the far end
	if d := DistanceToSegmentKm(past, a, b); math.Abs(d-4) > 0.05 {
		t.Fatalf("beyond the end = %.3f km, want about 4", d)
	}
	if br := Bearing(a, b); br > 0.5 && br < 359.5 {
		t.Fatalf("north bearing = %.2f", br)
	}
	for _, c := range []struct {
		deg  float64
		name string
	}{{0, "north"}, {44, "north-east"}, {90, "east"}, {181, "south"}, {270, "west"}, {359, "north"}} {
		if got := Compass(c.deg); got != c.name {
			t.Errorf("Compass(%v) = %s, want %s", c.deg, got, c.name)
		}
	}
}
