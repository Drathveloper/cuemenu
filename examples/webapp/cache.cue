package webapp

#MemoryCache: {
	memory: {
		// Maximum size in MB.
		size_mb: int & >0 | *64
	}
}

#RedisCache: {
	redis: {
		addr!: string
		db:    int & >=0 | *0
	}
}

// Session cache: in memory or in Redis (no discriminator field).
cache: *#MemoryCache | #RedisCache
