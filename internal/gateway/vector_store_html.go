package gateway

import (
	"bytes"
	"errors"
	"strings"

	"golang.org/x/net/html"
)

func vectorStoreHTMLText(content []byte) ([]byte, error) {
	content, err := vectorStoreUTF8Text(content)
	if err != nil {
		return nil, err
	}
	if len(content) > maxVectorStoreParsedContentBytes {
		return nil, errors.New("HTML content exceeds 16 MiB")
	}
	document, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, errors.New("HTML content is invalid")
	}
	type frame struct {
		node *html.Node
		exit bool
	}
	stack := []frame{{node: document}}
	var text bytes.Buffer
	pendingSpace := false
	newline := func() {
		for text.Len() > 0 && text.Bytes()[text.Len()-1] == ' ' {
			text.Truncate(text.Len() - 1)
		}
		if text.Len() > 0 && text.Bytes()[text.Len()-1] != '\n' && text.Len() < maxVectorStoreParsedContentBytes {
			text.WriteByte('\n')
		}
		pendingSpace = false
	}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.exit {
			newline()
			continue
		}
		node := current.node
		if node.Type == html.ElementNode && vectorStoreHTMLExcluded(node.Data) {
			continue
		}
		if node.Type == html.TextNode {
			trimmed := strings.TrimSpace(node.Data)
			value := strings.Join(strings.Fields(trimmed), " ")
			if value != "" {
				separator := text.Len() > 0 && text.Bytes()[text.Len()-1] != '\n' && (pendingSpace || !strings.HasPrefix(node.Data, trimmed))
				separatorBytes := 0
				if separator {
					separatorBytes = 1
				}
				if text.Len()+len(value)+separatorBytes > maxVectorStoreParsedContentBytes {
					return nil, errors.New("HTML text exceeds 16 MiB")
				}
				if separator {
					text.WriteByte(' ')
				}
				text.WriteString(value)
				pendingSpace = !strings.HasSuffix(node.Data, trimmed)
			} else if text.Len() > 0 && text.Bytes()[text.Len()-1] != '\n' {
				pendingSpace = true
			}
		}
		if node.Type == html.ElementNode {
			if node.Data == "br" {
				newline()
			} else if vectorStoreHTMLBlock(node.Data) {
				newline()
				stack = append(stack, frame{node: node, exit: true})
			}
		}
		for child := node.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, frame{node: child})
		}
	}
	return bytes.TrimSpace(text.Bytes()), nil
}

func vectorStoreHTMLExcluded(name string) bool {
	switch name {
	case "head", "script", "style", "template", "noscript":
		return true
	default:
		return false
	}
}

func vectorStoreHTMLBlock(name string) bool {
	switch name {
	case "address", "article", "aside", "blockquote", "div", "dl", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "li", "main", "nav", "ol", "p", "pre", "section", "table", "tr", "ul":
		return true
	default:
		return false
	}
}
