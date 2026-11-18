// Package tls holds the TLS settings shared by several services.
package tls

#Settings: {
	// Minimum TLS version.
	min_version: *"1.2" | "1.3"
	// Allowed ciphers; empty = the library defaults.
	ciphers: [...string]
}
