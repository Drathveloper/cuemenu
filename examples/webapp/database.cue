package webapp

#SQLite: {
	type: "sqlite3"
	path: string | *"data/webapp.db"
}

#Network: {
	host:     string | *"127.0.0.1"
	port:     #Port
	name:     string | *"webapp"
	user!:    string
	password: string @menu(secret)
}

#Postgres: {
	#Network
	type:     "postgres"
	port:     _ | *5432
	ssl_mode: *"disable" | "require" | "verify-full"
}

#MySQL: {
	#Network
	type: "mysql"
	port: _ | *3306
}

// Database of the service.
database: *#SQLite | #Postgres | #MySQL
