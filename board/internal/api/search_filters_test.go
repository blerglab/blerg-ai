package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestCardSearchFilters(t *testing.T) {
	srv, pool := testServer(t)
	token := adminToken(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Filters", FieldSchema: json.RawMessage(`[{"key":"severity","type":"text"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(t.Context(), pool, b.ID)
	if err != nil || len(cols) < 2 {
		t.Fatalf("columns: %+v, %v", cols, err)
	}
	other, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := db.ListColumns(t.Context(), pool, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	tags := []string{"0.5.1"}
	for n := range 30 {
		title := "Filter regression"
		typ, priority := "bug", "high"
		col := cols[n%2].ID
		card, err := db.CreateCard(t.Context(), pool, b.ID, db.CardParams{Title: &title, ColumnID: &col, Tags: &tags, Type: &typ, Priority: &priority, Fields: json.RawMessage(`{"severity":"sev1"}`)}, db.EventMeta{})
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			if _, err := db.ArchiveCard(t.Context(), pool, card.Card.ID, nil, db.EventMeta{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	supported := []string{"archived", "column", "cursor", "field.{key}", "include_archived", "limit", "page", "priority", "q", "tag", "type"}
	for _, endpoint := range []string{"cards", "cards/search"} {
		path := "/api/boards/" + b.ID + "/" + endpoint
		t.Run(endpoint, func(t *testing.T) {
			for _, tc := range []struct {
				query string
				want  int
			}{
				{"column=" + strings.ToUpper(cols[0].Name), 14},
				{"column=" + cols[0].ID, 14},
				{"tag=0.5.1&column=" + url.QueryEscape(cols[0].Name), 14},
				{"column=" + cols[0].ID + "&column=" + cols[1].ID + "&limit=100", 29},
				{"tag=absent&column=" + cols[0].ID, 0},
				{"q=unmatchedwords&column=" + cols[0].ID, 0},
				{"include_archived=1&column=" + cols[0].ID, 14},
				{"include_archived=1&limit=100", 30},
				{"type=bug&priority=high&field.severity=sev1&column=" + cols[0].ID, 14},
				{"field.severity=sev2&column=" + cols[0].ID, 0},
				{"type=task&column=" + cols[0].ID, 0},
				{"priority=low&column=" + cols[0].ID, 0},
				{"archived=1&include_archived=1&column=" + cols[0].ID, 0},
				{"archived=1&include_archived=1&tag=0.5.1", 1},
				{"archived=1", 1},
			} {
				t.Run(tc.query, func(t *testing.T) {
					resp := request(t, srv, "GET", path+"?"+tc.query, token, nil, nil)
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("status %d", resp.StatusCode)
					}
					var cards []db.Card
					decodeBody(t, resp, &cards)
					if len(cards) != tc.want {
						t.Fatalf("got %d cards, want %d", len(cards), tc.want)
					}
				})
			}
			for _, column := range []string{"unknown", foreign[0].ID, cols[0].Name + " suffix", ""} {
				t.Run("invalid-column-"+column, func(t *testing.T) {
					resp := request(t, srv, "GET", path+"?column="+url.QueryEscape(column), token, nil, nil)
					if resp.StatusCode != http.StatusBadRequest {
						t.Fatalf("status %d", resp.StatusCode)
					}
					var got struct {
						Error string `json:"error"`
					}
					decodeBody(t, resp, &got)
					if !strings.Contains(got.Error, column) {
						t.Fatalf("missing rejected column: %q", got.Error)
					}
					for _, col := range cols {
						if !strings.Contains(got.Error, col.Name) {
							t.Fatalf("missing column %s: %q", col.Name, got.Error)
						}
					}
				})
			}
			for _, bad := range []string{"-1", "0", "ten"} {
				if resp := request(t, srv, "GET", path+"?limit="+bad, token, nil, nil); resp.StatusCode != http.StatusBadRequest {
					t.Errorf("limit=%s: status %d", bad, resp.StatusCode)
				}
			}
			resp := request(t, srv, "GET", path+"?colum=inbox&unexpected=1", token, nil, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("unknown parameter: status %d", resp.StatusCode)
			}
			var got struct {
				Error     string   `json:"error"`
				Supported []string `json:"supported"`
			}
			decodeBody(t, resp, &got)
			if !slices.Equal(got.Supported, supported) || !strings.Contains(got.Error, "colum") || !strings.Contains(got.Error, "unexpected") {
				t.Errorf("unknown parameters: %+v", got)
			}

			// Interleaved columns and a small limit catch filtering after LIMIT/OFFSET.
			cursor := ""
			seen := map[string]bool{}
			for page := 0; page < 7; page++ {
				resp = request(t, srv, "GET", path+"?page=true&limit=2&tag=0.5.1&column="+cols[0].ID+"&cursor="+url.QueryEscape(cursor), token, nil, nil)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("page status %d", resp.StatusCode)
				}
				var got db.SearchPage
				decodeBody(t, resp, &got)
				if len(got.Cards) != 2 {
					t.Fatalf("page %d: %+v", page, got)
				}
				for _, card := range got.Cards {
					if card.ColumnID == nil || *card.ColumnID != cols[0].ID || seen[card.ID] {
						t.Fatalf("wrong/duplicate card: %+v", card)
					}
					seen[card.ID] = true
				}
				cursor = got.NextCursor
				if (cursor == "") != (page == 6) {
					t.Fatalf("page %d cursor %q", page, cursor)
				}
			}
		})
	}
	// The web app loads the whole board without a search limit.
	resp := request(t, srv, "GET", "/api/boards/"+b.ID+"/cards", token, nil, nil)
	var all []db.Card
	decodeBody(t, resp, &all)
	if len(all) != 29 {
		t.Fatalf("bare list truncated: %d", len(all))
	}
}

func TestOnboardDocumentsCardFilters(t *testing.T) {
	srv, _ := testServer(t)
	resp := request(t, srv, "GET", "/onboard", "", nil, nil)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"GET  /api/boards/{id}/cards ", "tag=0.5.1&column=inbox", "column=inbox&column=ready", "include_archived=1", "archived=1", "field.", "limit=", "page=true", "cursor=", "Unknown query parameters", "returns all matches", "single-valued", "search defaults to 20"} {
		if !strings.Contains(string(body), text) {
			t.Errorf("onboarding missing %q", text)
		}
	}
}

func TestFilteredCardListHasNoDefaultLimit(t *testing.T) {
	srv, pool := testServer(t)
	token := adminToken(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "All matches"})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(t.Context(), pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for n := range 136 {
		title := "Counting regression"
		tags := []string{"bulk"}
		if n < 35 || n == 135 {
			tags = []string{"0.5.1"}
		}
		column := cols[0].ID
		if n == 135 {
			column = cols[1].ID
		}
		if _, err := db.CreateCard(t.Context(), pool, b.ID, db.CardParams{Title: &title, Tags: &tags, ColumnID: &column}, db.EventMeta{}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/boards/" + b.ID
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/cards?tag=0.5.1&column=inbox", 35},
		{"/cards?column=inbox", 135},
		{"/cards?column=inbox&page=false", 135},
		{"/cards?column=inbox&limit=7", 7},
		{"/cards?column=inbox&limit=120", 120},
		{"/cards/search?tag=0.5.1&column=inbox", 20},
		{"/cards/search?column=inbox&limit=120", 100},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp := request(t, srv, "GET", base+tc.path, token, nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var cards []db.Card
			decodeBody(t, resp, &cards)
			if len(cards) != tc.want {
				t.Fatalf("got %d cards, want %d", len(cards), tc.want)
			}
			for _, card := range cards {
				if card.ColumnID == nil || *card.ColumnID != cols[0].ID {
					t.Fatalf("card outside inbox: %+v", card)
				}
			}
		})
	}
	for _, endpoint := range []string{"/cards", "/cards/search"} {
		t.Run(endpoint+"-paging", func(t *testing.T) {
			path := base + endpoint + "?tag=0.5.1&column=inbox&page=true"
			resp := request(t, srv, "GET", path, token, nil, nil)
			var first db.SearchPage
			decodeBody(t, resp, &first)
			if len(first.Cards) != 20 || first.NextCursor == "" {
				t.Fatalf("first page: %+v", first)
			}
			resp = request(t, srv, "GET", path+"&cursor="+url.QueryEscape(first.NextCursor), token, nil, nil)
			var last db.SearchPage
			decodeBody(t, resp, &last)
			if len(last.Cards) != 15 || last.NextCursor != "" {
				t.Fatalf("last page: %+v", last)
			}
		})
	}
}

func TestCardFiltersRejectRepeatedSingleValues(t *testing.T) {
	srv, pool := testServer(t)
	token := adminToken(t)
	b, err := db.CreateBoard(t.Context(), pool, db.BoardParams{Name: "Repeated filters"})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"cards", "cards/search"} {
		for _, key := range []string{"tag", "type", "priority", "field.severity", "q", "archived", "include_archived", "limit", "page", "cursor"} {
			for _, value := range []string{"value", ""} {
				t.Run(endpoint+"-"+key+"-"+value, func(t *testing.T) {
					q := url.Values{key: {value, value}}
					resp := request(t, srv, "GET", "/api/boards/"+b.ID+"/"+endpoint+"?"+q.Encode(), token, nil, nil)
					var got struct {
						Error string `json:"error"`
					}
					status := resp.StatusCode
					if status != http.StatusBadRequest {
						resp.Body.Close()
						t.Fatalf("repeated %s: status %d", key, status)
					}
					decodeBody(t, resp, &got)
					if !strings.Contains(got.Error, key) {
						t.Fatalf("missing repeated parameter %q: %+v", key, got)
					}
				})
			}
		}
	}
}
