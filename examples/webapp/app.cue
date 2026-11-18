package webapp

// Display name of the instance.
app_name: string | *"My forge"

// Run mode.
run_mode: *"prod" | "dev"

// A valid TCP port.
#Port: int & >=1 & <=65535

// Version of the configuration format (fixed, not editable).
schema_version: 3
