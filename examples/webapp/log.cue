package webapp

log: {
	level: *"info" | "debug" | "warn" | "error"
	// Log outputs, in order.
	outputs: [...("console" | "file")] | *["console"]
	// Log file path, when the "file" output is used.
	file_path?: string
	// Optional size limit in MB; null disables it.
	max_size_mb: int | null | *null
}

log: {
	// Log level per module.
	filters: [...{
		module!: string
		level:   *"debug" | "info" | "warn"
	}]
}
