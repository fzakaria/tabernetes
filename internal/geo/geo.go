// Package geo says roughly where an address on the internet is, from a
// database on the agent's own machine. No visitor's address is sent
// anywhere to be looked up, and what comes back is a city and
// coordinates rounded to about ten kilometres: enough to put a dot on
// a globe, and no nearer.
package geo

import (
	"math"
	"net"

	"github.com/oschwald/maxminddb-golang"
)

// The language a city's name is taken in, and how many places past
// the point a coordinate keeps: one, which is about ten kilometres.
const (
	nameLanguage    = "en"
	coordinateScale = 10
)

// Place is roughly where an address is.
type Place struct {
	// City is the city's name in English, and Country the country's
	// two-letter code. Either may be empty.
	City    string
	Country string
	// Latitude and Longitude are in degrees, rounded.
	Latitude  float64
	Longitude float64
}

// record is what the database holds for an address, as far as a Place
// is made from it. The layout is MaxMind's for a city database, which
// DB-IP's free one follows.
type record struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

// Book is an open database of where addresses are.
type Book struct {
	reader *maxminddb.Reader
}

// Open returns the database in a file, in MaxMind's format.
func Open(path string) (*Book, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &Book{reader: reader}, nil
}

// Lookup returns roughly where an address is, and whether the database
// knows. A nil Book knows nothing, so a caller without a database asks
// all the same.
func (b *Book) Lookup(address string) (Place, bool) {
	if b == nil || b.reader == nil {
		return Place{}, false
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return Place{}, false
	}
	var found record
	if err := b.reader.Lookup(ip, &found); err != nil {
		return Place{}, false
	}
	return placeOf(found)
}

// placeOf returns the Place a record describes, or false for a record
// with no coordinates, which is what an unknown address gets.
func placeOf(found record) (Place, bool) {
	if found.Location.Latitude == 0 && found.Location.Longitude == 0 {
		return Place{}, false
	}
	return Place{
		City:      found.City.Names[nameLanguage],
		Country:   found.Country.ISOCode,
		Latitude:  math.Round(found.Location.Latitude*coordinateScale) / coordinateScale,
		Longitude: math.Round(found.Location.Longitude*coordinateScale) / coordinateScale,
	}, true
}
