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

	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/css"
	mhtml "github.com/tdewolff/minify/html"
	"golang.org/x/net/html"
)

func externalStylesheet(n *html.Node) (err error, href string, stylesheet bool) {
	href = ""
	stylesheet = false

	if n.Data != "link" {
		return nil, href, stylesheet
	}

	for _, a := range n.Attr {
		if a.Key == "rel" && a.Val == "stylesheet" {
			stylesheet = true
		}

		if a.Key == "href" {
			href = a.Val
		}
	}

	return nil, href, stylesheet
}

func inlineCss(n *html.Node, rootFile string) {
	if n.Type == html.ElementNode {
		err, href, stylesheet := externalStylesheet(n)
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

			parent := n.Parent
			// Remove the current node
			parent.RemoveChild(n)
			var buf bytes.Buffer
			err = m.Minify("text/css", &buf, reader)
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
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		inlineCss(c, rootFile)
	}
}

func minifyHtml(root *html.Node, w io.Writer) {
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
		inlineCss(z, filepath)
		minifyHtml(z, pw)
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
