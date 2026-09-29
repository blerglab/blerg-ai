// blerg-board: the deliberately small CLI — ls, get, search, new, move — for
// humans, scripts, and agents in harnesses without MCP configured. MCP is the
// primary agent surface (structured `fields` through argv is quoting hell).
//
// Env: BLERG_BOARD_URL (default http://localhost:8080), BLERG_BOARD_TOKEN, BLERG_BOARD_BOARD.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func usage() {
	fmt.Fprint(os.Stderr, `blerg-board — card board CLI

  blerg-board ls [boards]                 list cards on $BLERG_BOARD_BOARD (or boards)
  blerg-board get <#number|uuid>          show one card
  blerg-board search <words...>           search cards on $BLERG_BOARD_BOARD by text
  blerg-board new <title> [-b body] [-t type] [-p priority] [-k dedup_key] [-r repo]
  blerg-board move <#number|uuid> <column name>

Env: BLERG_BOARD_URL, BLERG_BOARD_TOKEN, BLERG_BOARD_BOARD (board uuid)
`)
	os.Exit(2)
}

type client struct {
	base, token string
}

func (c client) do(method, path string, body any) (json.RawMessage, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, &buf) //nolint:gosec // G704: CLI; the base URL is the operator's own BLERG_BOARD_URL
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: CLI; request target is the operator's own BLERG_BOARD_URL
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

type card struct {
	ID       string  `json:"id"`
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	Type     string  `json:"type"`
	Priority string  `json:"priority"`
	ColumnID *string `json:"column_id"`
	Version  int     `json:"version"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	c := client{base: envOr("BLERG_BOARD_URL", "http://localhost:8080"), token: os.Getenv("BLERG_BOARD_TOKEN")}
	board := os.Getenv("BLERG_BOARD_BOARD")
	need := func() {
		if board == "" {
			fatal("BLERG_BOARD_BOARD not set")
		}
	}

	switch os.Args[1] {
	case "ls":
		if len(os.Args) > 2 && os.Args[2] == "boards" {
			raw, err := c.do("GET", "/api/boards", nil)
			check(err)
			var boards []struct {
				ID, Name string
			}
			check(json.Unmarshal(raw, &boards))
			for _, b := range boards {
				fmt.Printf("%s  %s\n", b.ID, b.Name)
			}
			return
		}
		need()
		raw, err := c.do("GET", "/api/boards/"+board+"/cards", nil)
		check(err)
		var cards []card
		check(json.Unmarshal(raw, &cards))
		cols := columnNames(c, board)
		for _, cd := range cards {
			col := ""
			if cd.ColumnID != nil {
				col = cols[*cd.ColumnID]
			}
			fmt.Printf("#%-4d %-12s %-8s %s\n", cd.Number, col, cd.Type, cd.Title)
		}
	case "get":
		if len(os.Args) < 3 {
			usage()
		}
		cd := resolve(c, board, os.Args[2])
		raw, err := c.do("GET", "/api/cards/"+cd.ID, nil)
		check(err)
		var pretty bytes.Buffer
		check(json.Indent(&pretty, raw, "", "  "))
		fmt.Println(pretty.String())
	case "search":
		need()
		if len(os.Args) < 3 {
			usage()
		}
		q := strings.Join(os.Args[2:], " ")
		path := "/api/boards/" + board + "/cards/search?q=" + url.QueryEscape(q)
		raw, err := c.do("GET", path, nil)
		check(err)
		var cards []card
		check(json.Unmarshal(raw, &cards))
		if len(cards) == 0 {
			fmt.Println("no matches")
			return
		}
		cols := columnNames(c, board)
		for _, cd := range cards {
			col := ""
			if cd.ColumnID != nil {
				col = cols[*cd.ColumnID]
			}
			fmt.Printf("#%-4d %-12s %-8s %s\n", cd.Number, col, cd.Type, cd.Title)
		}
	case "new":
		need()
		if len(os.Args) < 3 {
			usage()
		}
		body := map[string]any{"title": os.Args[2]}
		args := os.Args[3:]
		for i := 0; i+1 < len(args); i += 2 {
			switch args[i] {
			case "-b":
				body["body"] = args[i+1]
			case "-t":
				body["type"] = args[i+1]
			case "-p":
				body["priority"] = args[i+1]
			case "-k":
				body["dedup_key"] = args[i+1]
			case "-r":
				body["repos"] = []string{args[i+1]}
			}
		}
		raw, err := c.do("POST", "/api/boards/"+board+"/cards", body)
		check(err)
		var cd card
		check(json.Unmarshal(raw, &cd))
		fmt.Printf("created #%d %s\n", cd.Number, cd.Title)
	case "move":
		need()
		if len(os.Args) < 4 {
			usage()
		}
		cd := resolve(c, board, os.Args[2])
		target := strings.Join(os.Args[3:], " ")
		colID := ""
		raw, err := c.do("GET", "/api/boards/"+board+"/columns", nil)
		check(err)
		var cols []struct{ ID, Name string }
		check(json.Unmarshal(raw, &cols))
		for _, col := range cols {
			if strings.EqualFold(col.Name, target) {
				colID = col.ID
			}
		}
		if colID == "" {
			fatal("no column named " + target)
		}
		_, err = c.do("POST", "/api/cards/"+cd.ID+"/move", map[string]any{"column_id": colID})
		check(err)
		fmt.Printf("moved #%d → %s\n", cd.Number, target)
	default:
		usage()
	}
}

func resolve(c client, board, ref string) card {
	if n, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil {
		if board == "" {
			fatal("BLERG_BOARD_BOARD needed to resolve #numbers")
		}
		raw, err := c.do("GET", "/api/boards/"+board+"/cards", nil)
		check(err)
		var cards []card
		check(json.Unmarshal(raw, &cards))
		for _, cd := range cards {
			if cd.Number == n {
				return cd
			}
		}
		fatal(fmt.Sprintf("no card #%d on this board", n))
	}
	return card{ID: ref}
}

func columnNames(c client, board string) map[string]string {
	out := map[string]string{}
	raw, err := c.do("GET", "/api/boards/"+board+"/columns", nil)
	if err != nil {
		return out
	}
	var cols []struct{ ID, Name string }
	_ = json.Unmarshal(raw, &cols)
	for _, col := range cols {
		out[col.ID] = col.Name
	}
	return out
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func check(err error) {
	if err != nil {
		fatal(err.Error())
	}
}
func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "blerg-board: "+msg)
	os.Exit(1)
}
