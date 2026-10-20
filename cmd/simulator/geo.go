package main

import "math"

const (
	// Within this many metres of a stop counts as arrived.
	arrivalRadiusM = 150
	// Earth's mean radius, for the distance and bearing maths.
	earthRadiusM = 6_371_000
)

type position struct {
	Lat float64
	Lng float64
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// distanceM is the great-circle distance in metres.
func distanceM(a, b position) float64 {
	φ1, φ2 := rad(a.Lat), rad(b.Lat)
	dφ := rad(b.Lat - a.Lat)
	dλ := rad(b.Lng - a.Lng)

	h := math.Sin(dφ/2)*math.Sin(dφ/2) +
		math.Cos(φ1)*math.Cos(φ2)*math.Sin(dλ/2)*math.Sin(dλ/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(h))
}

// bearing is the initial compass heading from a to b, in radians.
func bearing(a, b position) float64 {
	φ1, φ2 := rad(a.Lat), rad(b.Lat)
	dλ := rad(b.Lng - a.Lng)

	y := math.Sin(dλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(dλ)
	return math.Atan2(y, x)
}

// moveToward returns the point `metres` along the great circle from a to b.
func moveToward(a, b position, metres float64) position {
	θ := bearing(a, b)
	δ := metres / earthRadiusM
	φ1, λ1 := rad(a.Lat), rad(a.Lng)

	φ2 := math.Asin(math.Sin(φ1)*math.Cos(δ) + math.Cos(φ1)*math.Sin(δ)*math.Cos(θ))
	λ2 := λ1 + math.Atan2(
		math.Sin(θ)*math.Sin(δ)*math.Cos(φ1),
		math.Cos(δ)-math.Sin(φ1)*math.Sin(φ2),
	)
	return position{Lat: deg(φ2), Lng: deg(λ2)}
}

// path is a route as a line of points, with the distance travelled along it.
type path struct {
	pts []position
	// cum[i] is the distance from pts[0] to pts[i], in metres.
	cum  []float64
	done float64
	// From the road router, or a straight line because it wasn't available.
	byRoad bool
}

func newPath(pts []position, byRoad bool) *path {
	cum := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		cum[i] = cum[i-1] + distanceM(pts[i-1], pts[i])
	}
	return &path{pts: pts, cum: cum, byRoad: byRoad}
}

func (p *path) length() float64 { return p.cum[len(p.cum)-1] }

func (p *path) remaining() float64 { return math.Max(0, p.length()-p.done) }

// advance moves `metres` further along and returns where that is.
func (p *path) advance(metres float64) position {
	p.done = math.Min(p.length(), p.done+metres)
	return p.at(p.done)
}

// at is the point `d` metres along the path.
func (p *path) at(d float64) position {
	if d <= 0 || len(p.pts) == 1 {
		return p.pts[0]
	}
	if d >= p.length() {
		return p.pts[len(p.pts)-1]
	}
	for i := 1; i < len(p.pts); i++ {
		if d <= p.cum[i] {
			seg := p.cum[i] - p.cum[i-1]
			if seg == 0 {
				return p.pts[i]
			}
			return moveToward(p.pts[i-1], p.pts[i], d-p.cum[i-1])
		}
	}
	return p.pts[len(p.pts)-1]
}
