// Package timeutil centralizes business-timezone handling. Importing
// time/tzdata embeds the IANA database so Asia/Shanghai works on Windows and
// minimal containers even when the host has no zoneinfo files.
package timeutil

import (
	"time"
	_ "time/tzdata"
)

const BusinessZone = "Asia/Shanghai"

// BusinessLocation returns Asia/Shanghai, falling back to a fixed UTC+8 zone
// only if the embedded database is somehow unavailable.
func BusinessLocation() *time.Location {
	if loc, err := time.LoadLocation(BusinessZone); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*60*60)
}

// LoadLocation validates arbitrary constraint timezone names.
func LoadLocation(name string) (*time.Location, error) {
	return time.LoadLocation(name)
}
