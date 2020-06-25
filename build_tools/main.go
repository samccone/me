package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
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

// Partially lifted from @andybons
// https://gist.github.com/andybons/c074c2811f70db5fc3d3f89fbc69f81f

func shakeCSS(r io.Reader, doc *html.Node) (out bytes.Buffer, err error) {
	p := pcss.NewParser(r, false)
	var buf bytes.Buffer
	activeRules := 0

	for {
		gtype, ttype, data := p.Next()
		_, _ = ttype, data
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

			s := sb.String()
			// These selectors are not supported.
			s = strings.TrimSuffix(s, ":link")
			s = strings.TrimSuffix(s, ":hover")
			s = strings.TrimSuffix(s, ":active")
			s = strings.TrimSuffix(s, ":visited")

			matches, err := selectorMatches(s, doc)
			if err != nil {
				return buf, fmt.Errorf("selcss.Compile: %w", err)
			}
			if matches {
				activeRules++
				buf.WriteString(sb.String())
				if gtype == pcss.QualifiedRuleGrammar {
					buf.WriteString(",\n")
				} else if gtype == pcss.BeginRulesetGrammar {
					buf.WriteString(" {\n")
				}
			} else {
				fmt.Printf("Unused selector %q in CSS\n", s)
				if gtype == pcss.BeginRulesetGrammar {
					buf.WriteString(" {\n")
				}
			}
		case pcss.BeginAtRuleGrammar:
			buf.Write(data)
			for _, v := range p.Values() {
				buf.Write(v.Data)
			}
			buf.WriteString(" {\n")
		case pcss.DeclarationGrammar:
			if activeRules == 0 {
				// fmt.Println("No active rules to apply")
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
				// fmt.Println("No active rules to apply")
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

	return buf, err
}

func externalStylesheet(n *html.Node) (href string, stylesheet bool, err error) {
	href = ""
	stylesheet = false

	if n.Data != "link" {
		return href, stylesheet, nil
	}

	for _, a := range n.Attr {
		if a.Key == "rel" && a.Val == "stylesheet" {
			stylesheet = true
		}

		if a.Key == "href" {
			href = a.Val
		}
	}

	return href, stylesheet, nil
}

func inlineCSS(root *html.Node, cursor *html.Node, rootFile string) {
	if cursor.Type == html.ElementNode {
		href, stylesheet, err := externalStylesheet(cursor)
		if err != nil {
			log.Fatalf("unable to determine if stylesheet %v", err)
		}
		if stylesheet {
			m := minify.New()
			m.AddFunc("text/css", css.Minify)

			toLoad := filepath.Join(filepath.Dir(rootFile), href)
			reader, err := os.Open(toLoad)
			if err != nil {
				log.Fatalf("Unable to open file %v", err)
			}

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
	for c := cursor.FirstChild; c != nil; c = c.NextSibling {
		inlineCSS(root, c, rootFile)
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
	inline := false
	filepath := os.Args[1]
	if len(os.Args) > 2 && os.Args[1] == "-inline" {
		inline = true
		filepath = os.Args[2]
	}

	inputFile, err := os.Open(filepath)
	if err != nil {
		log.Fatalf("unable to open file %v", err)
	}

	contents, err := ioutil.ReadAll(inputFile)
	inputFile.Close()
	if err != nil {
		log.Fatalf("unable read file %v", err)
	}

	z, err := html.Parse(strings.NewReader(string(contents)))
	if err != nil {
		log.Fatalf("unable to parse document %v", err)
	}
	pr, pw := io.Pipe()
	go func() {
		inlineCSS(z, z, filepath)
		minifyHTML(z, pw)
		defer pw.Close()
	}()

	if inline {
		outputFile, err := os.Create(filepath)
		defer outputFile.Close()
		if err != nil {
			log.Fatalf("Error opening file for write  %v", err)
		}
		_, err = io.Copy(outputFile, pr)
		if err != nil {
			log.Fatalf("Error writing to file %v", err)
		}
	} else {
		io.Copy(os.Stdout, pr)
	}
}
