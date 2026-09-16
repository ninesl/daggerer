package envmerge

import (
	"reflect"
	"testing"
)

func TestMerge(t *testing.T) {
	tests := []struct {
		name     string
		base     []Variable
		override []Variable
		want     []Variable
	}{
		{"empty", nil, nil, nil},
		{"base only", []Variable{{Name: "A", Value: "one"}}, nil, []Variable{{Name: "A", Value: "one"}}},
		{"override only", nil, []Variable{{Name: "B", Value: "two"}}, []Variable{{Name: "B", Value: "two"}}},
		{"override wins", []Variable{{Name: "A", Value: "one"}, {Name: "B", Value: "two"}}, []Variable{{Name: "A", Value: "changed"}}, []Variable{{Name: "A", Value: "changed"}, {Name: "B", Value: "two"}}},
		{"empty override wins", []Variable{{Name: "A", Value: "one"}}, []Variable{{Name: "A", Value: ""}}, []Variable{{Name: "A", Value: ""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Merge(tt.base, tt.override); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     []Variable
		wantErr  bool
	}{
		{"empty", "", nil, false},
		{"dotenv syntax", "PLAIN=value\nQUOTED=\"two words\"\nEXPANDED=${PLAIN}-suffix\n", []Variable{{Name: "EXPANDED", Value: "value-suffix"}, {Name: "PLAIN", Value: "value"}, {Name: "QUOTED", Value: "two words"}}, false},
		{"comments and export", "# comment\nexport TOKEN=secret\n", []Variable{{Name: "TOKEN", Value: "secret"}}, false},
		{"invalid", "TOKEN='unterminated", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.contents)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCollision(t *testing.T) {
	tests := []struct {
		name    string
		public  []Variable
		private []Variable
		want    string
		found   bool
	}{
		{"none", []Variable{{Name: "APP_MODE", Value: "STAGING"}}, []Variable{{Name: "DATABASE_URL", Value: "private"}}, "", false},
		{"same value", []Variable{{Name: "TOKEN", Value: "same"}}, []Variable{{Name: "TOKEN", Value: "same"}}, "TOKEN", true},
		{"different value", []Variable{{Name: "TOKEN", Value: "public"}}, []Variable{{Name: "TOKEN", Value: "private"}}, "TOKEN", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := Collision(tt.public, tt.private)
			if got != tt.want || found != tt.found {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, found, tt.want, tt.found)
			}
		})
	}
}

func TestShellDotenv(t *testing.T) {
	tests := []struct {
		name    string
		values  []Variable
		want    string
		wantErr bool
	}{
		{"empty", nil, "", false},
		{"quotes without expansion", []Variable{{Name: "TOKEN", Value: "a'b $HOME"}}, "TOKEN='a'\"'\"'b $HOME'\n", false},
		{"invalid shell name", []Variable{{Name: "BAD-NAME", Value: "secret"}}, "", true},
		{"NUL value", []Variable{{Name: "TOKEN", Value: "before\x00after"}}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ShellDotenv(tt.values)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got (%q, %v), want (%q, error=%v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestShellExports(t *testing.T) {
	got, err := ShellExports([]Variable{{Name: "DATABASE_URL", Value: "postgres://example"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "export DATABASE_URL='postgres://example'\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
