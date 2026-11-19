package config_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"

	"github.com/Drathveloper/cuemenu/examples/webapp/config"
)

// The generated code must agree with CUE: on what is valid, and on the
// configuration it decodes, defaults included.
func TestAgreesWithCUE(t *testing.T) {
	t.Parallel()
	ctx := cuecontext.New()
	schema := ctx.BuildInstance(load.Instances([]string{"."}, &load.Config{Dir: ".."})[0])
	if err := schema.Err(); err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			val := schema.Unify(ctx.CompileString(input))
			cueErr := val.Validate(cue.Concrete(true))
			cfg, goErr := config.ParseJSON([]byte(input))
			if (cueErr == nil) != (goErr == nil) {
				t.Fatalf("CUE says %v, Go says %v", cueErr, goErr)
			}
			if cueErr != nil {
				return
			}
			want, err := val.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if w, g := normalize(t, want), normalize(t, got); !reflect.DeepEqual(w, g) {
				t.Fatalf("decoded differently:\nCUE %v\nGo  %v", w, g)
			}
		})
	}
}

var inputs = []string{
	`{}`,
	`{"app_name": "x", "run_mode": "dev"}`,
	`{"run_mode": "test"}`,
	`{"schema_version": 3}`,
	`{"schema_version": 4}`,
	`{"server": {"http_port": 8080}}`,
	`{"server": {"http_port": 0}}`,
	`{"server": {"http_port": 70000}}`,
	`{"server": {"domain": "ab"}}`,
	`{"server": {"domain": "añb"}}`,
	`{"server": {"protocol": "https"}}`,
	`{"server": {"protocol": "https", "cert_file": "c", "key_file": "k"}}`,
	`{"server": {"protocol": "https", "cert_file": "c", "key_file": "k", "tls": {"min_version": "1.3"}}}`,
	`{"server": {"protocol": "https", "cert_file": "c", "key_file": "k", "tls": {"min_version": "1.1"}}}`,
	`{"server": {"protocol": "https", "cert_file": "c", "key_file": "k", "tls": {"bogus": 1}}}`,
	`{"server": {"protocol": "unix"}}`,
	`{"server": {"protocol": "unix", "socket_path": "/tmp/s"}}`,
	`{"server": {"protocol": "ftp"}}`,
	`{"server": {"headers": {"X-Frame": "deny"}}}`,
	`{"server": {"headers": {"Frame": "deny"}}}`,
	`{"server": {"enable_gzip": true}}`,
	`{"auth": {"session_lifetime": "1h30m"}}`,
	`{"auth": {"session_lifetime": "forever"}}`,
	`{"auth": {"min_password_length": 7}}`,
	`{"auth": {"min_password_length": 8}}`,
	`{"auth": {"admins": ["root"]}}`,
	`{"auth": {"oauth2": {"gh": {"client_id": "a", "client_secret": "b"}}}}`,
	`{"auth": {"oauth2": {"gh": {"client_id": "a"}}}}`,
	`{"auth": {"oauth2": {"gh": {"client_id": "a", "client_secret": "b", "discovery_url": "https://x"}}}}`,
	`{"auth": {"oauth2": {"gh": {"client_id": "a", "client_secret": "b", "discovery_url": "http://x"}}}}`,
	`{"auth": {"oauth2": {"gh": {"client_id": "a", "client_secret": "b", "scopes": ["openid"]}}}}`,
	`{"cache": {"memory": {"size_mb": 128}}}`,
	`{"cache": {"memory": {"size_mb": 0}}}`,
	`{"cache": {"redis": {"addr": "localhost:6379"}}}`,
	`{"cache": {"redis": {"addr": "localhost:6379", "db": 2}}}`,
	`{"cache": {"redis": {}}}`,
	`{"cache": {"memory": {}, "redis": {"addr": "x"}}}`,
	`{"database": {"type": "sqlite3", "path": "/var/db"}}`,
	`{"database": {"type": "postgres"}}`,
	`{"database": {"type": "postgres", "user": "u", "password": "p"}}`,
	`{"database": {"type": "postgres", "user": "u", "password": "p", "ssl_mode": "require", "port": 6432}}`,
	`{"database": {"type": "postgres", "user": "u", "password": "p", "ssl_mode": "maybe"}}`,
	`{"database": {"type": "postgres", "user": "u", "password": "p", "bogus": 1}}`,
	`{"database": {"type": "mysql", "user": "u", "password": "p"}}`,
	`{"database": {"type": "oracle"}}`,
	`{"log": {"level": "debug", "outputs": ["console", "file"], "file_path": "/var/log/x"}}`,
	`{"log": {"outputs": ["syslog"]}}`,
	`{"log": {"max_size_mb": 10}}`,
	`{"log": {"max_size_mb": null}}`,
	`{"log": {"filters": [{"module": "db"}, {"module": "http", "level": "warn"}]}}`,
	`{"log": {"filters": [{"level": "info"}]}}`,
	`{"log": {"filters": [{"module": "db", "level": "error"}]}}`,
	`{"mailer": {"enabled": true}}`,
	`{"mailer": {"enabled": true, "smtp_addr": "smtp.x", "from": "a@b.c"}}`,
	`{"mailer": {"enabled": true, "smtp_addr": "smtp.x", "from": "nobody"}}`,
	`{"mailer": {"enabled": true, "smtp_addr": "smtp.x", "from": "a@b.c", "smtp_port": 25}}`,
	`null`,
	`{"server": null}`,
	`{"server": {"domain": null}}`,
	`{"server": {"protocol": "https", "cert_file": "c", "key_file": "k", "tls": null}}`,
	`{"auth": {"admins": [null]}}`,
	`{"auth": {"oauth2": {"gh": null}}}`,
	`{"database": null}`,
	`{"log": {"filters": [null]}}`,
	`{"log": {"outputs": null}}`,
	`{"server": {"http_port": "80"}}`,
	`{"server": {"enable_gzip": "yes"}}`,
	`{"auth": {"admins": "root"}}`,
	`{"cache": "memory"}`,
	`{"bogus": 1}`,
}

// normalize decodes a configuration for comparison, without what one
// side leaves out and the other writes empty: absent, null, [] and {}
// mean the same here. Go does not compute root_url, and it drops the
// fields the schema does not declare (bogus), which CUE keeps.
func normalize(t *testing.T, data []byte) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return prune(out)
}

func prune(val any) any {
	switch x := val.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range x {
			if item = prune(item); item != nil && key != "root_url" && key != "bogus" {
				out[key] = item
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(x) == 0 {
			return nil
		}
		for idx := range x {
			x[idx] = prune(x[idx])
		}
	}
	return val
}

// YAML goes through JSON: the same configuration, the same errors, and an
// empty document is the defaults.
func TestYAML(t *testing.T) {
	t.Parallel()
	fromYAML, err := config.ParseYAML([]byte("server:\n  protocol: https\n  cert_file: c\n  key_file: k\n" +
		"database:\n  type: postgres\n  user: u\n  password: p\n"))
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := config.ParseJSON([]byte(`{"server": {"protocol": "https", "cert_file": "c", "key_file": "k"},
		"database": {"type": "postgres", "user": "u", "password": "p"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML, fromJSON) {
		t.Fatalf("YAML %+v\nJSON %+v", fromYAML, fromJSON)
	}
	if _, ok := fromYAML.Database.(*config.Postgres); !ok {
		t.Fatalf("database is a %T", fromYAML.Database)
	}
	if _, bad := config.ParseYAML([]byte("server:\n  protocol: https\n")); bad == nil {
		t.Fatal("https without cert_file was accepted")
	}
	empty, err := config.ParseYAML(nil)
	if want := config.NewConfig(); err != nil || !reflect.DeepEqual(*empty, want) {
		t.Fatalf("%v: %+v", err, empty)
	}
}
