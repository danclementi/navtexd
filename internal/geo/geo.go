// Package geo has great-circle distance and bearing.
package geo

import "math"

const earthKm = 6371.0088

func rad(d float64) float64 { return d * math.Pi / 180 }

// DistanceKm is the great-circle distance between two points (degrees, north / east positive).
func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := rad(lat1), rad(lat2)
	dp, dl := p2-p1, rad(lon2-lon1)
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * earthKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// BearingDeg is the initial great-circle bearing from point 1 to point 2, 0..360 degrees clockwise from north.
func BearingDeg(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2, dl := rad(lat1), rad(lat2), rad(lon2-lon1)
	y := math.Sin(dl) * math.Cos(p2)
	x := math.Cos(p1)*math.Sin(p2) - math.Sin(p1)*math.Cos(p2)*math.Cos(dl)
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}
