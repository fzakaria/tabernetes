package geo

import "testing"

// TestPlaceOf checks what is kept of a database's record: the city's
// English name, the country's code, and the coordinates rounded to a
// tenth of a degree, which is about ten kilometres and is as near as
// the site is ever told.
func TestPlaceOf(t *testing.T) {
	var found record
	found.City.Names = map[string]string{"en": "Lisbon", "pt": "Lisboa"}
	found.Country.ISOCode = "PT"
	found.Location.Latitude = 38.7223
	found.Location.Longitude = -9.1393

	place, ok := placeOf(found)
	if !ok {
		t.Fatal("a record with coordinates gave no place")
	}
	if place.City != "Lisbon" || place.Country != "PT" {
		t.Errorf("place %+v", place)
	}
	if place.Latitude != 38.7 || place.Longitude != -9.1 {
		t.Errorf("coordinates %v, %v", place.Latitude, place.Longitude)
	}
}

// TestPlaceOfNowhere checks that a record with no coordinates, which
// is what a database gives for an address it does not know, is no
// place, and not a point in the sea off Africa.
func TestPlaceOfNowhere(t *testing.T) {
	if place, ok := placeOf(record{}); ok {
		t.Errorf("an empty record gave %+v", place)
	}
}

// TestLookupWithoutABook checks that asking where an address is costs
// nothing and finds nothing when no database was opened, and for text
// that is no address: the agent asks without checking for either.
func TestLookupWithoutABook(t *testing.T) {
	var none *Book
	if _, ok := none.Lookup("198.51.100.1"); ok {
		t.Error("no database found a place")
	}
	if _, ok := (&Book{}).Lookup("not an address"); ok {
		t.Error("text that is no address found a place")
	}
}
