// Package geo holds tiny, dependency-free helpers shared across
// services — kept in pkg/ rather than internal/ since geo math has no
// dependency on this app's domain and would be reusable in another
// service (e.g. a future dispatch/matching microservice) without
// copy-pasting.
package geo

// MetersToKM converts a PostGIS ST_Distance/ST_DWithin result (always in
// meters for geography columns) into kilometers for API responses.
func MetersToKM(m float64) float64 {
	return m / 1000.0
}

// KMToMeters converts a client-supplied search radius in kilometers into
// meters for use in ST_DWithin.
func KMToMeters(km float64) float64 {
	return km * 1000.0
}
