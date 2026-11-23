package geo

import "testing"

func TestFenceCircle(t *testing.T) {
	f := Fence{Center: busia, RadiusM: 200}
	if c := f.Check(Point{Lat: busia.Lat + 0.001, Lng: busia.Lng}, 0); !c.Inside {
		t.Fatalf("~111 m away should be inside a 200 m fence: %+v", c)
	}
	far := Point{Lat: busia.Lat + 0.01, Lng: busia.Lng} // ~1.1 km
	c := f.Check(far, 0)
	if c.Inside || c.DistanceM < 800 || c.DistanceM > 1000 {
		t.Fatalf("expected ~912 m outside, got %+v", c)
	}
	if c2 := f.Check(far, 2000); !c2.Inside {
		t.Fatalf("tolerance should absorb the gap: %+v", c2)
	}
}

func TestFencePolygonWinsAndHoles(t *testing.T) {
	f := Fence{
		Center: Point{Lat: 10, Lng: 10}, RadiusM: 5, // ignored, ring wins
		Ring:  square(34.12, 0.45, 34.14, 0.46),
		Holes: [][][]float64{square(34.125, 0.452, 34.13, 0.455)},
	}
	if !f.Check(Point{Lat: 0.458, Lng: 34.135}, 0).Inside {
		t.Fatal("point in ring should be inside")
	}
	if f.Check(Point{Lat: 0.453, Lng: 34.127}, 0).Inside {
		t.Fatal("point in hole should be outside")
	}
	if c := f.Check(Point{Lat: 0.47, Lng: 34.13}, 0); c.Inside || c.DistanceM <= 0 {
		t.Fatalf("point north of ring should be outside with a distance: %+v", c)
	}
}

func TestFenceUnusableAndInvalid(t *testing.T) {
	if (Fence{}).Check(busia, 100).Inside {
		t.Fatal("empty fence must not report inside")
	}
	if (Fence{Center: busia, RadiusM: 100}).Check(Point{}, 100).Inside {
		t.Fatal("0,0 placeholder point must not report inside")
	}
}

func TestNearest(t *testing.T) {
	fences := []Fence{
		{},
		{Center: Point{Lat: 0.5, Lng: 34.2}, RadiusM: 100},
		{Center: busia, RadiusM: 100},
	}
	i, c, ok := Nearest(Point{Lat: busia.Lat + 0.002, Lng: busia.Lng}, fences, 0)
	if !ok || i != 2 || c.Inside {
		t.Fatalf("expected nearest index 2 outside, got %d %+v %v", i, c, ok)
	}
	i, c, ok = Nearest(busia, fences, 0)
	if !ok || i != 2 || !c.Inside {
		t.Fatalf("expected inside index 2, got %d %+v %v", i, c, ok)
	}
	if _, _, ok := Nearest(busia, []Fence{{}}, 0); ok {
		t.Fatal("no usable fence should report ok=false")
	}
}
