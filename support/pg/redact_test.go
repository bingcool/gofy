package pg

import "testing"

func TestRedactDSN(t *testing.T) {
	cases := map[string]struct {
		dsn  string
		want string
	}{
		"url with password": {
			dsn:  "postgres://app:secret@127.0.0.1:5432/app?sslmode=disable",
			want: "postgres://app:***@127.0.0.1:5432/app?sslmode=disable",
		},
		"url without password": {
			dsn:  "postgres://app@127.0.0.1:5432/app",
			want: "postgres://app@127.0.0.1:5432/app",
		},
		"url without userinfo": {
			dsn:  "postgres://127.0.0.1:5432/app",
			want: "postgres://127.0.0.1:5432/app",
		},
		"keyword": {
			dsn:  "host=127.0.0.1 user=app password=secret dbname=app",
			want: "host=127.0.0.1 user=app password=*** dbname=app",
		},
		"keyword quoted": {
			dsn:  "host=127.0.0.1 password='secret with spaces' dbname=app",
			want: "host=127.0.0.1 password=*** dbname=app",
		},
		"keyword case insensitive": {
			dsn:  "host=127.0.0.1 PASSWORD=secret dbname=app",
			want: "host=127.0.0.1 PASSWORD=*** dbname=app",
		},
		"keyword without password": {
			dsn:  "host=127.0.0.1 user=app dbname=app",
			want: "host=127.0.0.1 user=app dbname=app",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := redactDSN(tc.dsn); got != tc.want {
				t.Fatalf("redactDSN(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}
