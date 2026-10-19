package catalog

import (
	"bytes"
	"errors"
	"strings"

	"golang.org/x/net/html"
)

// docRow is one row of the OpenCode Go docs Endpoints table.
type docRow struct {
	Name     string
	ID       string
	Endpoint string
	Package  string
}

// docColumns are the header cells that identify the Endpoints table. The
// page carries several tables (pricing, limits); matching on headers keeps
// the parser independent of anchors, CSS classes, and table order.
var docColumns = [...]string{"Model", "Model ID", "Endpoint", "AI SDK Package"}

var (
	errDocTableMissing = errors.New("endpoints table not found")
	errDocTableEmpty   = errors.New("endpoints table has no rows")
)

// parseDocTable extracts the Endpoints table rows from the docs page HTML.
// A page without the table, or with an empty one, is an error: both mean
// the page shape drifted, never that the plan has no models.
func parseDocTable(body []byte) ([]docRow, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for table := range root.Descendants() {
		if table.Type != html.ElementNode || table.Data != "table" {
			continue
		}
		cols, ok := docColumnIndex(table)
		if !ok {
			continue
		}
		var rows []docRow
		for tr := range table.Descendants() {
			if tr.Type != html.ElementNode || tr.Data != "tr" {
				continue
			}
			cells := rowCells(tr, "td")
			if len(cells) == 0 {
				continue // header row
			}
			cell := func(i int) string {
				if i < len(cells) {
					return cells[i]
				}
				return ""
			}
			rows = append(rows, docRow{
				Name:     cell(cols[0]),
				ID:       cell(cols[1]),
				Endpoint: cell(cols[2]),
				Package:  cell(cols[3]),
			})
		}
		if len(rows) == 0 {
			return nil, errDocTableEmpty
		}
		return rows, nil
	}
	return nil, errDocTableMissing
}

// docColumnIndex reports the cell index of each docColumns header in table,
// and whether every one is present.
func docColumnIndex(table *html.Node) ([len(docColumns)]int, bool) {
	var idx [len(docColumns)]int
	for tr := range table.Descendants() {
		if tr.Type != html.ElementNode || tr.Data != "tr" {
			continue
		}
		headers := rowCells(tr, "th")
		if len(headers) == 0 {
			continue
		}
		for i, want := range docColumns {
			idx[i] = -1
			for j, h := range headers {
				if h == want {
					idx[i] = j
					break
				}
			}
			if idx[i] < 0 {
				return idx, false
			}
		}
		return idx, true
	}
	return idx, false
}

// rowCells returns the trimmed text of tr's direct cell children of the
// given tag (td or th).
func rowCells(tr *html.Node, tag string) []string {
	var cells []string
	for c := range tr.ChildNodes() {
		if c.Type == html.ElementNode && c.Data == tag {
			cells = append(cells, strings.TrimSpace(nodeText(c)))
		}
	}
	return cells
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			b.WriteString(d.Data)
		}
	}
	return b.String()
}
