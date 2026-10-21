package inbox

import "testing"

func TestParseType(t *testing.T) {
	tests := []struct {
		name    string
		want    SubjectType
		wantErr bool
	}{
		{name: "pr", want: TypePullRequest},
		{name: "PR", want: TypePullRequest},
		{name: "ci", want: TypeCheckSuite},
		{name: "dependabot", want: TypeDependabot},
		{name: "PullRequest", wantErr: true},
		{name: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseType(tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseType(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseType(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestSubjectTypeShortName(t *testing.T) {
	for _, name := range TypeNames() {
		typ, err := ParseType(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := typ.ShortName(); got != name {
			t.Errorf("%q.ShortName() = %q, want %q", typ, got, name)
		}
	}
	if got := SubjectType("WorkflowRun").ShortName(); got != "WorkflowRun" {
		t.Errorf("ShortName() of an unnamed type = %q, want the API name", got)
	}
}

func TestParseState(t *testing.T) {
	for _, name := range []string{"open", "closed", "merged", "MERGED"} {
		if _, err := ParseState(name); err != nil {
			t.Errorf("ParseState(%q) error = %v", name, err)
		}
	}
	if _, err := ParseState("draft"); err == nil {
		t.Error("ParseState(\"draft\") succeeded, want an error")
	}
}
