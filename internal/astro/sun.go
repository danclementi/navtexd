// Package astro computes sun positions and sunrise / sunset times (NOAA low-precision algorithm, good to about a minute).
package astro

import (
	"math"
	"time"
)

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// julian returns the Julian day number of t.
func julian(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/8.64e13 + 2440587.5
}

// sunPos returns the solar declination (deg) and the equation of time (minutes) at t.
func sunPos(t time.Time) (decl, eqTime float64) {
	T := (julian(t) - 2451545.0) / 36525
	L0 := math.Mod(280.46646+T*(36000.76983+T*0.0003032), 360)
	M := 357.52911 + T*(35999.05029-0.0001537*T)
	e := 0.016708634 - T*(0.000042037+0.0000001267*T)
	Mr := rad(M)
	C := math.Sin(Mr)*(1.914602-T*(0.004817+0.000014*T)) + math.Sin(2*Mr)*(0.019993-0.000101*T) + math.Sin(3*Mr)*0.000289
	trueLong := L0 + C
	omega := 125.04 - 1934.136*T
	lambda := trueLong - 0.00569 - 0.00478*math.Sin(rad(omega))
	eps0 := 23 + (26+(21.448-T*(46.815+T*(0.00059-T*0.001813)))/60)/60
	eps := eps0 + 0.00256*math.Cos(rad(omega))
	decl = deg(math.Asin(math.Sin(rad(eps)) * math.Sin(rad(lambda))))
	y := math.Tan(rad(eps/2)) * math.Tan(rad(eps/2))
	L0r := rad(L0)
	eqTime = 4 * deg(y*math.Sin(2*L0r)-2*e*math.Sin(Mr)+4*e*y*math.Sin(Mr)*math.Cos(2*L0r)-0.5*y*y*math.Sin(4*L0r)-1.25*e*e*math.Sin(2*Mr))
	return
}

// Elevation returns the sun's elevation above the horizon in degrees (no refraction) at t for a position (north / east positive).
func Elevation(t time.Time, latDeg, lonDeg float64) float64 {
	decl, eq := sunPos(t)
	u := t.UTC()
	minutes := float64(u.Hour()*60+u.Minute()) + float64(u.Second())/60 + float64(u.Nanosecond())/6e10
	ha := math.Mod(minutes+eq+4*lonDeg, 1440)/4 - 180
	lat, d, h := rad(latDeg), rad(decl), rad(ha)
	return deg(math.Asin(math.Sin(lat)*math.Sin(d) + math.Cos(lat)*math.Cos(d)*math.Cos(h)))
}

// SunTimes returns the sunrise and sunset (UTC) on the UTC calendar day of date for a position. ok is false when the sun
// does not rise or set that day (polar day or night).
func SunTimes(date time.Time, latDeg, lonDeg float64) (rise, set time.Time, ok bool) {
	day := time.Date(date.UTC().Year(), date.UTC().Month(), date.UTC().Day(), 0, 0, 0, 0, time.UTC)
	noonGuess := day.Add(12*time.Hour - time.Duration(lonDeg/15*float64(time.Hour)))
	decl, eq := sunPos(noonGuess)
	cosH := (math.Cos(rad(90.833)) - math.Sin(rad(latDeg))*math.Sin(rad(decl))) / (math.Cos(rad(latDeg)) * math.Cos(rad(decl)))
	if cosH > 1 || cosH < -1 {
		return time.Time{}, time.Time{}, false
	}
	h := deg(math.Acos(cosH))
	riseMin := 720 - 4*(lonDeg+h) - eq
	setMin := 720 - 4*(lonDeg-h) - eq
	f := func(m float64) time.Time { return day.Add(time.Duration(m * float64(time.Minute))) }
	return f(riseMin), f(setMin), true
}
