package webapp

mailer: {
	// Send outgoing email.
	enabled: bool | *false
	if enabled {
		smtp_addr!: string
		smtp_port:  #Port | *587
		from!:      =~"^[^@]+@[^@]+$"
	}
}
