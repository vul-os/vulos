package ddos

import (
	maxminddb "github.com/oschwald/maxminddb-golang"
)

// openMaxmindReader opens the MaxMind DB file at path using the
// oschwald/maxminddb-golang pure-Go reader.
func openMaxmindReader(path string) (maxminddbReader, error) {
	return maxminddb.Open(path)
}
