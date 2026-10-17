package ticket

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func clean(t *testing.T, s string) Ticket {
	t.Helper()
	tk, err := Clean(json.RawMessage(s))
	if err != nil {
		t.Fatalf("Clean(%s): %v", s, err)
	}
	return tk
}

func refused(t *testing.T, s, want string) {
	t.Helper()
	_, err := Clean(json.RawMessage(s))
	if err == nil {
		t.Fatalf("Clean(%s) accepted", s)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Clean(%s) = %q, want it to mention %q", s, err, want)
	}
}

func TestCleanKeepsTheShapeAndDropsUnknownFields(t *testing.T) {
	tk := clean(t, `{"source":"jira","key":"ABC-1","url":"https://jira.example.test/browse/ABC-1","title":"Crash on save",
		"status":"In Progress","type":"Bug","priority":"High","assignee":"Ann","reporter":"Bo","labels":["ui","save"],
		"description":"Line 1\r\nLine 2\u0007","fields":[{"name":"Sprint","value":"42"}],
		"comments":[{"author":"Cy","at":"2026-10-01","body":"seen"}],"acceptance":["Saves","No crash"],
		"secret":{"nested":[[[1]]]}}`)
	if tk.Source != "jira" || tk.Key != "ABC-1" || tk.Title != "Crash on save" || tk.Status != "In Progress" ||
		tk.Type != "Bug" || tk.Priority != "High" || tk.Assignee != "Ann" || tk.Reporter != "Bo" {
		t.Fatalf("%+v", tk)
	}
	if tk.Description != "Line 1\nLine 2" {
		t.Fatalf("description %q", tk.Description)
	}
	if len(tk.Labels) != 2 || len(tk.Fields) != 1 || tk.Fields[0] != (Field{Name: "Sprint", Value: "42"}) ||
		len(tk.Comments) != 1 || len(tk.Acceptance) != 2 {
		t.Fatalf("%+v", tk)
	}
	raw, _ := json.Marshal(tk)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("unknown field kept: %s", raw)
	}
}

func TestCleanRequiresSourceKeyTitle(t *testing.T) {
	refused(t, `{"key":"A-1","title":"t"}`, "source")
	refused(t, `{"source":"jira","title":"t"}`, "key")
	refused(t, `{"source":"jira","key":"A-1"}`, "title")
	refused(t, `[]`, "object")
	refused(t, `"x"`, "object")
}

func TestCleanRefusesNonStrings(t *testing.T) {
	refused(t, `{"source":"jira","key":1,"title":"t"}`, "key")
	refused(t, `{"source":"jira","key":"A","title":"t","labels":"ui"}`, "labels")
	refused(t, `{"source":"jira","key":"A","title":"t","labels":[1]}`, "labels")
	refused(t, `{"source":"jira","key":"A","title":"t","fields":[{"name":"n","value":3}]}`, "fields")
	refused(t, `{"source":"jira","key":"A","title":"t","comments":[{"body":{"x":1}}]}`, "comments")
	refused(t, `{"source":"jira","key":"A","title":"t","acceptance":[["x"]]}`, "acceptance")
}

// A tracker's empty field often maps to null: it reads as absent.
func TestCleanReadsNullAsAbsent(t *testing.T) {
	tk := clean(t, `{"source":"jira","key":"A","title":"t","status":null,"labels":null,"fields":null}`)
	if tk.Status != "" || tk.Labels != nil || tk.Fields != nil {
		t.Fatalf("%+v", tk)
	}
}

func TestCleanURLMustBeHTTP(t *testing.T) {
	for _, u := range []string{"javascript:alert(1)", "mailto:a@example.test", "data:text/html,x", "//example.test/x", "ftp://example.test/x", "https://", "http://exa mple.test/"} {
		refused(t, `{"source":"jira","key":"A","title":"t","url":`+jsonString(u)+`}`, "url")
	}
	tk := clean(t, `{"source":"jira","key":"A","title":"t","url":"HTTP://example.test/a?b=c#d"}`)
	if tk.URL != "HTTP://example.test/a?b=c#d" {
		t.Fatal(tk.URL)
	}
	if clean(t, `{"source":"jira","key":"A","title":"t"}`).URL != "" {
		t.Fatal("no url is allowed")
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestCleanCapsEverything(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	list := func(n int, s string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = jsonString(s)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	objs := func(n int, s string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = s
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	tk := clean(t, `{"source":`+jsonString(long(40))+`,"key":`+jsonString(long(70))+`,"title":`+jsonString(long(400))+
		`,"status":`+jsonString(long(70))+`,"type":`+jsonString(long(70))+`,"priority":`+jsonString(long(70))+
		`,"assignee":`+jsonString(long(130))+`,"reporter":`+jsonString(long(130))+
		`,"labels":`+list(25, long(70))+`,"acceptance":`+list(35, long(600))+
		`,"description":`+jsonString(long(25000))+
		`,"fields":`+objs(35, `{"name":`+jsonString(long(90))+`,"value":`+jsonString(long(600))+`}`)+
		`,"comments":`+objs(8, `{"author":`+jsonString(long(130))+`,"at":`+jsonString(long(50))+`,"body":`+jsonString(long(2500))+`}`)+`}`)
	n := utf8.RuneCountInString
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"source", n(tk.Source), MaxSource}, {"key", n(tk.Key), MaxKey}, {"title", n(tk.Title), MaxTitle},
		{"status", n(tk.Status), MaxChip}, {"type", n(tk.Type), MaxChip}, {"priority", n(tk.Priority), MaxChip},
		{"assignee", n(tk.Assignee), MaxPerson}, {"reporter", n(tk.Reporter), MaxPerson},
		{"labels", len(tk.Labels), MaxLabels}, {"label", n(tk.Labels[0]), MaxLabel},
		{"acceptance", len(tk.Acceptance), MaxAcceptance}, {"acceptance item", n(tk.Acceptance[0]), MaxAcceptanceText},
		{"description", n(tk.Description), MaxDescription},
		{"fields", len(tk.Fields), MaxFields}, {"field name", n(tk.Fields[0].Name), MaxFieldName}, {"field value", n(tk.Fields[0].Value), MaxFieldValue},
		{"comments", len(tk.Comments), MaxComments}, {"author", n(tk.Comments[0].Author), MaxPerson},
		{"at", n(tk.Comments[0].At), MaxAt}, {"body", n(tk.Comments[0].Body), MaxCommentBody},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestCleanKeepsTheNewestComments(t *testing.T) {
	var parts []string
	for i := 0; i < 7; i++ {
		parts = append(parts, `{"author":"a","body":"`+string(rune('0'+i))+`"}`)
	}
	tk := clean(t, `{"source":"x","key":"A","title":"t","comments":[`+strings.Join(parts, ",")+`]}`)
	if len(tk.Comments) != 5 || tk.Comments[0].Body != "2" || tk.Comments[4].Body != "6" {
		t.Fatalf("%+v", tk.Comments)
	}
}

func TestCleanDropsEmptyEntries(t *testing.T) {
	tk := clean(t, `{"source":"x","key":"A","title":"t","labels":["", " "],"acceptance":["  "],"fields":[{"name":"","value":""}],"comments":[{"body":""}]}`)
	if tk.Labels != nil || tk.Acceptance != nil || tk.Fields != nil || tk.Comments != nil {
		t.Fatalf("%+v", tk)
	}
}

func TestPinnedFold(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	rows := []Row{
		{Seq: 1, EventID: "e1", Ts: t0, Action: "pin", Source: "jira", Key: "A-1", Title: "One", Status: "Open"},
		{Seq: 2, EventID: "e2", Ts: t0.Add(time.Minute), Action: "pin", Source: "bugzilla", Key: "A-1", Title: "Other"},
		{Seq: 3, EventID: "e3", Ts: t0.Add(2 * time.Minute), Action: "update", Source: "jira", Key: "A-1", Title: "One", Status: "Done"},
		{Seq: 4, EventID: "e4", Ts: t0.Add(3 * time.Minute), Action: "pin", Source: "github", Key: "9", Title: "Nine"},
		{Seq: 5, EventID: "e5", Ts: t0.Add(4 * time.Minute), Action: "unpin", Source: "github", Key: "9"},
	}
	got := Pinned(rows)
	if len(got) != 2 || got[0].Source != "jira" || got[0].Status != "Done" || got[0].EventID != "e3" || got[0].Seq != 3 ||
		got[1].Source != "bugzilla" || !got[0].UpdatedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("%+v", got)
	}
	if Pinned(nil) == nil {
		t.Fatal("nil list")
	}
}
