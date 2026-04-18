package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	selcss "github.com/samccone/css"
	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/css"
	mhtml "github.com/tdewolff/minify/html"
	pcss "github.com/tdewolff/parse/v2/css"
	"golang.org/x/net/html"
)

func selectorMatches(sel string, root *html.Node) (bool, error) {
	s, err := selcss.Compile(sel)
	if err != nil {
		return false, fmt.Errorf("selcss.Compile: %s %w", sel, err)
	}
	return len(s.Select(root)) > 0, nil
}

// shakeCSS removes unused CSS rules based on the provided HTML document.
func shakeCSS(r io.Reader, doc *html.Node) (out bytes.Buffer, err error) {
	p := pcss.NewParser(r, false)
	var buf bytes.Buffer
	activeRules := 0
	var matchingSelectors []string

	for {
		gtype, _, data := p.Next()
		if err := p.Err(); err != nil {
			if err == io.EOF {
				break
			}
			return buf, err
		}
		switch gtype {
		case pcss.BeginRulesetGrammar,
			pcss.QualifiedRuleGrammar:
			var sb strings.Builder
			for _, v := range p.Values() {
				sb.Write(v.Data)
			}

			raw := sb.String()
			s := strings.TrimSpace(raw)
			s = strings.TrimSuffix(s, ",")
			s = strings.TrimSpace(s)

			// Simple pseudo-class stripping for matching purposes.
			// This handles cases like a:hover by checking for 'a'.
			matchSel := s
			if i := strings.Index(matchSel, ":"); i != -1 {
				matchSel = matchSel[:i]
			}
			// If stripping left us with empty string (e.g. ":root"), just use original or skip.
			if matchSel == "" {
				matchSel = s
			}

			matches := false
			if matchSel != "" {
				var err error
				matches, err = selectorMatches(matchSel, doc)
				if err != nil {
					// Fallback: if we can't compile/match, keep it to be safe.
					matches = true
				}
			} else {
				matches = true
			}

			if matches {
				matchingSelectors = append(matchingSelectors, strings.TrimSuffix(strings.TrimSpace(raw), ","))
			} else {
				fmt.Fprintf(os.Stderr, "Unused selector %q in CSS\n", s)
			}

			if gtype == pcss.BeginRulesetGrammar {
				if len(matchingSelectors) > 0 {
					activeRules = 1
					buf.WriteString(strings.Join(matchingSelectors, ", "))
					buf.WriteString(" {\n")
				} else {
					activeRules = 0
				}
				matchingSelectors = nil
			}

		case pcss.BeginAtRuleGrammar:
			buf.Write(data)
			for _, v := range p.Values() {
				buf.Write(v.Data)
			}
			buf.WriteString(" {\n")

		case pcss.DeclarationGrammar:
			if activeRules == 0 {
				continue
			}
			buf.Write(data)
			buf.WriteString(": ")
			for _, v := range p.Values() {
				buf.Write(v.Data)
			}
			buf.WriteString(";\n")

		case pcss.EndRulesetGrammar:
			if activeRules == 0 {
				continue
			}
			buf.Write(data)
			buf.WriteByte('\n')
			activeRules = 0

		case pcss.EndAtRuleGrammar:
			buf.Write(data)
			buf.WriteByte('\n')

		case pcss.CommentGrammar:
			continue

		default:
			return buf, fmt.Errorf("unsupported grammar: %v", gtype)
		}
	}

	return buf, nil
}

func externalStylesheet(n *html.Node) (href string, stylesheet bool) {
	if n.Data != "link" {
		return "", false
	}

	for _, a := range n.Attr {
		if a.Key == "rel" && a.Val == "stylesheet" {
			stylesheet = true
		}

		if a.Key == "href" {
			href = a.Val
		}
	}

	return href, stylesheet
}

func inlineCSS(root *html.Node, cursor *html.Node, rootFile string) {
	if cursor.Type == html.ElementNode {
		href, stylesheet := externalStylesheet(cursor)
		if stylesheet {
			m := minify.New()
			m.AddFunc("text/css", css.Minify)

			toLoad := filepath.Join(filepath.Dir(rootFile), href)
			reader, err := os.Open(toLoad)
			if err != nil {
				log.Fatalf("Unable to open file %v", err)
			}
			defer reader.Close()

			parent := cursor.Parent
			// Remove the current node
			parent.RemoveChild(cursor)
			var buf bytes.Buffer
			minified, err := shakeCSS(reader, root)
			if err != nil {
				log.Fatalf("Error shaking CSS %v", err)
			}
			err = m.Minify("text/css", &buf, strings.NewReader(minified.String()))
			if err != nil {
				log.Fatalf("Unable to minify css %v", err)
			}
			// inline css
			styleNode, err := html.ParseFragment(strings.NewReader(fmt.Sprintf("<style>%s</style>", buf.String())), parent)
			if err != nil {
				log.Fatalf("Unable to parse node %s", err)
			}
			parent.AppendChild(styleNode[0])
		}
	}
	// We need to be careful when iterating children if we are removing nodes.
	// However, externalStylesheet only matches <link> nodes which usually don't have children.
	for c := cursor.FirstChild; c != nil; {
		next := c.NextSibling
		inlineCSS(root, c, rootFile)
		c = next
	}
}

func minifyHTML(root *html.Node, w io.Writer) {
	m := minify.New()
	pr, pw := io.Pipe()
	m.AddFunc("text/html", mhtml.Minify)
	go func() {
		html.Render(pw, root)
		defer pw.Close()
	}()
	m.Minify("text/html", w, pr)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-inline] <file.html>\n", os.Args[0])
		os.Exit(1)
	}

	inline := false
	path := os.Args[1]
	if os.Args[1] == "-inline" && len(os.Args) > 2 {
		inline = true
		path = os.Args[2]
	}

	inputFile, err := os.Open(path)
	if err != nil {
		log.Fatalf("unable to open file %v", err)
	}

	contents, err := io.ReadAll(inputFile)
	inputFile.Close()
	if err != nil {
		log.Fatalf("unable read file %v", err)
	}

	z, err := html.Parse(strings.NewReader(string(contents)))
	if err != nil {
		log.Fatalf("unable to parse document %v", err)
	}

	inlineCSS(z, z, path)

	pr, pw := io.Pipe()
	go func() {
		minifyHTML(z, pw)
		defer pw.Close()
	}()

	if inline {
		outputFile, err := os.Create(path)
		if err != nil {
			log.Fatalf("Error opening file for write  %v", err)
		}
		_, err = io.Copy(outputFile, pr)
		outputFile.Close()
		if err != nil {
			log.Fatalf("Error writing to file %v", err)
		}
	} else {
		io.Copy(os.Stdout, pr)
	}
}
