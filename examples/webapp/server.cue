package webapp

import (
	"strings"

	tlscfg "example.com/webapp/tls"
)

server: {
	// Listening protocol.
	protocol: *"http" | "https" | "unix"

	// Public domain, without scheme.
	domain: string & strings.MinRunes(3) | *"localhost"

	if protocol != "unix" {
		// Listening port.
		http_port: #Port | *3000
	}
	if protocol == "https" {
		cert_file!: string
		key_file!:  string
		tls:        tlscfg.#Settings
	}
	if protocol == "unix" {
		socket_path: string | *"/run/webapp.sock"
	}

	// Public URL, computed.
	root_url: "\(protocol)://\(domain)/"

	// Enable gzip compression of responses.
	enable_gzip: bool | *false
}

server: {
	// HTTP headers added to every response; they must start with "X-".
	headers: [=~"^X-"]: string
}
